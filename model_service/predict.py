"""Prediction logic for LightGBM models.

Loads trained models and metadata, constructs features from recent node data,
and outputs JSON predictions. Only writes to stdout; all logs go to stderr.
Single node failure does not affect other nodes.
"""
import sys
import argparse
import json
from datetime import datetime, timedelta
from typing import List, Dict, Any, Optional

import numpy as np
import pandas as pd
import pymysql

if __package__ in (None, ""):
    from pathlib import Path

    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from model_service import config
    from model_service import data_loader
    from model_service import feature_builder
    from model_service import model_registry
else:
    from . import config
    from . import data_loader
    from . import feature_builder
    from . import model_registry

TARGET_COLS = ["cpu_util", "gpu_util", "gpu_mem_util", "node_power"]


def _resample_and_fill(df: pd.DataFrame, freq: str, max_missing_ratio: float) -> pd.DataFrame:
    df = df.set_index("metric_time").sort_index()
    raw_features = ["cpu_util", "gpu_util", "gpu_mem_util", "mem_util", "node_power", "disk_io", "net_io"]
    numeric_cols = [c for c in raw_features if c in df.columns]
    df_numeric = df[numeric_cols] if numeric_cols else pd.DataFrame(index=df.index)
    resampled = df_numeric.resample(freq).mean()
    missing_before = resampled.isnull().sum(axis=1) / max(len(numeric_cols), 1)
    resampled["_missing_ratio"] = missing_before
    resampled = resampled[resampled["_missing_ratio"] <= max_missing_ratio]
    resampled = resampled.drop(columns=["_missing_ratio"])
    return resampled.reset_index().rename(columns={"metric_time": "metric_time"})


def _compute_trend(series: pd.Series) -> float:
    values = series.dropna().values
    if len(values) < 2:
        return 0.0
    x = np.arange(len(values))
    if np.std(values) < 1e-9:
        return 0.0
    try:
        coeffs = np.polyfit(x, values, 1)
        return float(coeffs[0])
    except Exception:
        return 0.0


def _build_features(window_data: pd.DataFrame, feature_cols: List[str]) -> Optional[pd.Series]:
    raw_features = ["cpu_util", "gpu_util", "gpu_mem_util", "mem_util", "node_power", "disk_io", "net_io"]
    stats = {}
    for col in raw_features:
        if col not in window_data.columns:
            continue
        series = window_data[col].values
        valid = series[~np.isnan(series)]
        if len(valid) == 0:
            stats[f"{col}_mean"] = np.nan
            stats[f"{col}_max"] = np.nan
            stats[f"{col}_min"] = np.nan
            stats[f"{col}_std"] = np.nan
            stats[f"{col}_last"] = np.nan
            stats[f"{col}_trend"] = np.nan
        else:
            stats[f"{col}_mean"] = np.mean(valid)
            stats[f"{col}_max"] = np.max(valid)
            stats[f"{col}_min"] = np.min(valid)
            stats[f"{col}_std"] = np.std(valid) if len(valid) > 1 else 0.0
            stats[f"{col}_last"] = valid[-1]
            stats[f"{col}_trend"] = _compute_trend(pd.Series(valid))

    ordered = []
    for col in feature_cols:
        if col in stats:
            ordered.append(stats[col])
        else:
            ordered.append(np.nan)
    return pd.Series(ordered, index=feature_cols)


def _parse_freq(freq: str) -> int:
    return int(pd.to_timedelta(freq).total_seconds())


def _get_risk_thresholds(df: pd.DataFrame, node_uuid: str, rated_power: Optional[float]) -> Dict[str, float]:
    thresholds = {}
    if rated_power and rated_power > 0:
        thresholds["node_power"] = rated_power * 0.85
    elif "node_power" in df.columns:
        valid = df["node_power"].dropna()
        if len(valid) > 0:
            thresholds["node_power"] = valid.quantile(0.95)
    thresholds.setdefault("node_power", 800.0)
    thresholds.setdefault("cpu_util", 80.0)
    thresholds.setdefault("gpu_util", 80.0)
    thresholds.setdefault("gpu_mem_util", 85.0)
    return thresholds


def _compute_risk_level(predictions: Dict[str, float], thresholds: Dict[str, float]) -> str:
    max_ratio = 0.0
    for metric, value in predictions.items():
        if value is None:
            continue
        if metric == "node_power" and metric in thresholds:
            ratio = value / max(thresholds[metric], 1e-9)
        elif metric in thresholds:
            ratio = value / max(thresholds[metric], 1e-9)
        else:
            continue
        max_ratio = max(max_ratio, ratio)
    if max_ratio >= 0.9:
        return "HIGH"
    elif max_ratio >= 0.7:
        return "MEDIUM"
    return "LOW"


def predict_for_node(
    node_uuid: str,
    cluster_uuid: str,
    horizons: List[int],
    metadata: Dict[str, Any],
) -> Dict[str, Any]:
    history_window = metadata.get("history_window", 60)
    resample_freq = metadata.get("resample_freq", "1min")
    feature_cols = metadata.get("feature_columns", [])
    target_cols = metadata.get("target_columns") or TARGET_COLS

    freq_seconds = _parse_freq(resample_freq)
    history_steps = int(history_window * 60 / freq_seconds)

    latest_df = data_loader.load_metrics(
        cluster_uuid=cluster_uuid,
        start_time=None,
        end_time=None,
        node_uuids=[node_uuid],
    )
    if latest_df.empty:
        return {"error": f"数据不足，缺少节点 {node_uuid} 的历史数据"}

    latest_times = pd.to_datetime(latest_df["metric_time"], errors="coerce").dropna()
    if latest_times.empty:
        return {"error": f"数据不足，节点 {node_uuid} 缺少有效 metric_time"}

    latest_metric_time = latest_times.max().to_pydatetime()
    end_time = latest_metric_time.strftime("%Y-%m-%d %H:%M:%S")
    lookback_minutes = max(history_window + max(horizons) + 30, history_window * 24)
    start_time = (latest_metric_time - timedelta(minutes=lookback_minutes)).strftime("%Y-%m-%d %H:%M:%S")

    df = latest_df[
        (pd.to_datetime(latest_df["metric_time"], errors="coerce") >= pd.Timestamp(start_time))
        & (pd.to_datetime(latest_df["metric_time"], errors="coerce") <= pd.Timestamp(end_time))
    ].copy()
    if df.empty:
        df = data_loader.load_metrics(
            cluster_uuid=cluster_uuid,
            start_time=start_time,
            end_time=end_time,
            node_uuids=[node_uuid],
        )

    if df.empty or len(df) < history_steps:
        return {"error": f"数据不足，缺少最近 {history_window} 分钟数据"}

    df_node = df[df["node_uuid"] == node_uuid].copy()
    if len(df_node) < history_steps:
        return {"error": f"数据不足，仅有 {len(df_node)} 条记录，需要 {history_steps} 条"}

    rated_power = None
    if "rated_power" in df_node.columns:
        rated_power_vals = df_node["rated_power"].dropna()
        if not rated_power_vals.empty:
            rated_power = float(rated_power_vals.iloc[-1])

    resampled = _resample_and_fill(df_node, resample_freq, config.DEFAULT_MAX_MISSING_RATIO)
    if len(resampled) < history_steps:
        return {"error": f"重采样后数据不足，仅有 {len(resampled)} 条，需要 {history_steps} 条"}

    latest_time = pd.Timestamp(resampled["metric_time"].iloc[-1])

    window = resampled.tail(history_steps)
    X = _build_features(window, feature_cols)
    if X is None or X.isna().all():
        return {"error": "特征构造失败，历史窗口全为 NaN"}

    thresholds = _get_risk_thresholds(df_node, node_uuid, rated_power)

    predictions = []
    for horizon in horizons:
        model_key = None
        for target in target_cols:
            path = config.CHECKPOINTS_DIR / f"lightgbm_{target}_h{horizon}.pkl"
            if not path.exists():
                continue
            model_key = f"{target}_h{horizon}"
            break

        if model_key is None:
            return {"error": f"未找到 horizon={horizon} 的模型文件"}

        target_model = {}
        for target in target_cols:
            path = config.CHECKPOINTS_DIR / f"lightgbm_{target}_h{horizon}.pkl"
            if path.exists():
                target_model[target] = model_registry.load_model(target, horizon)

        metrics_pred = {}
        for target, model in target_model.items():
            try:
                pred_val = model.predict(X.values.reshape(1, -1))[0]
                metrics_pred[target] = round(float(pred_val), 2)
            except Exception as e:
                print(f"[WARN] Prediction failed for {target}_h{horizon}: {e}", file=sys.stderr)
                metrics_pred[target] = None

        forecast_time = (latest_time + timedelta(minutes=horizon)).strftime("%Y-%m-%d %H:%M:%S")
        risk_level = _compute_risk_level(metrics_pred, thresholds)

        predictions.append({
            "horizon": horizon,
            "forecast_time": forecast_time,
            "metrics": metrics_pred,
            "risk_level": risk_level,
        })

    return {"predictions": predictions}


def main():
    parser = argparse.ArgumentParser(description="Predict using trained LightGBM models")
    parser.add_argument("--cluster-uuid", required=True)
    parser.add_argument("--node-uuids", required=True, help="comma-separated node UUIDs")
    parser.add_argument("--horizons", default="15,30,60")
    args = parser.parse_args()

    node_uuids = [n.strip() for n in args.node_uuids.split(",") if n.strip()]
    horizons = [int(h) for h in args.horizons.split(",")]

    try:
        metadata = model_registry.load_metadata()
    except FileNotFoundError as e:
        print(f"[ERROR] {e}", file=sys.stderr)
        print(json.dumps({"error": f"模型未训练，请先运行 train_baseline.py: {e}"}))
        sys.exit(1)

    model_version = metadata.get("model_version", config.MODEL_VERSION)
    feature_cols = metadata.get("feature_columns", [])

    if not feature_cols:
        print("[ERROR] metadata.json has no feature_columns", file=sys.stderr)
        sys.exit(1)

    results = []
    for node_uuid in node_uuids:
        print(f"[INFO] Predicting for node {node_uuid}", file=sys.stderr)
        result = predict_for_node(node_uuid, args.cluster_uuid, horizons, metadata)
        if "error" in result:
            results.append({"node_uuid": node_uuid, "error": result["error"]})
        else:
            results.append({"node_uuid": node_uuid, "predictions": result["predictions"]})

    output = {
        "cluster_uuid": args.cluster_uuid,
        "horizons": horizons,
        "model_version": model_version,
        "results": results,
    }

    print(json.dumps(output, ensure_ascii=False))


if __name__ == "__main__":
    main()
