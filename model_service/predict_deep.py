"""Prediction entry point for heterogeneous deep forecasting models."""
import argparse
import json
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Dict, List, Optional

import numpy as np
import pandas as pd

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from model_service import config
    from model_service import data_loader
    from model_service import deep_dataset
    from model_service import model_registry
else:
    from . import config
    from . import data_loader
    from . import deep_dataset
    from . import model_registry

UTILIZATION_METRICS = {"cpu_util", "gpu_util", "gpu_mem_util", "mem_util"}


def _import_torch():
    try:
        import torch

        from model_service.deep_models import HeteroSTGNNLoadNet, ITransformer, PatchTST
    except ModuleNotFoundError as exc:
        raise RuntimeError(
            "PyTorch is required for heterogeneous deep prediction. Install it with: "
            "python3 -m pip install torch"
        ) from exc
    return torch, PatchTST, ITransformer, HeteroSTGNNLoadNet


def _parse_list(value: str, cast=str) -> List:
    return [cast(item.strip()) for item in value.split(",") if item.strip()]


def _normalize_model_name(model_name: str) -> str:
    name = model_name.lower().replace("-", "_")
    if name in {"hetero_stgnn", "heterostgnn", "hetero_stgnn_loadnet"}:
        return "heterostgnn"
    return name


def _get_risk_thresholds(df: pd.DataFrame, rated_power: Optional[float]) -> Dict[str, float]:
    thresholds: Dict[str, float] = {}
    if rated_power and rated_power > 0:
        thresholds["node_power"] = rated_power * 0.85
    elif "node_power" in df.columns:
        valid = pd.to_numeric(df["node_power"], errors="coerce").dropna()
        if not valid.empty:
            thresholds["node_power"] = float(valid.quantile(0.95))
    thresholds.setdefault("node_power", 800.0)
    thresholds.setdefault("cpu_util", 80.0)
    thresholds.setdefault("gpu_util", 80.0)
    thresholds.setdefault("gpu_mem_util", 85.0)
    return thresholds


def _compute_risk_level(predictions: Dict[str, Optional[float]], thresholds: Dict[str, float]) -> str:
    max_ratio = 0.0
    for metric, value in predictions.items():
        if value is None or metric not in thresholds:
            continue
        max_ratio = max(max_ratio, float(value) / max(thresholds[metric], 1e-9))
    if max_ratio >= 0.9:
        return "HIGH"
    if max_ratio >= 0.7:
        return "MEDIUM"
    return "LOW"


def _uncertainty_config(metadata: Dict[str, object]) -> Dict[str, object]:
    return dict(metadata.get("uncertainty_config", {}))


def _quantiles_from_metadata(metadata: Dict[str, object]) -> List[float]:
    config_payload = _uncertainty_config(metadata)
    if not config_payload.get("enabled"):
        return []
    return [float(q) for q in config_payload.get("quantiles", [])]


def _risk_level_from_probability(probability: float) -> str:
    if probability >= 0.66:
        return "HIGH"
    if probability >= 0.33:
        return "MEDIUM"
    return "LOW"


def _deterministic_risk(value: Optional[float], threshold: float) -> float:
    if value is None:
        return 0.0
    ratio = float(value) / max(float(threshold), 1e-9)
    return float(np.clip((ratio - 0.7) / 0.3, 0.0, 1.0))


def _metric_payload(values: Dict[str, Optional[float]]) -> Dict[str, Optional[float]]:
    return {
        metric: None if value is None else round(_clip_metric_value(metric, float(value)), 2)
        for metric, value in values.items()
    }


def _clip_metric_value(metric: str, value: float) -> float:
    if metric in UTILIZATION_METRICS:
        return float(np.clip(value, 0.0, 100.0))
    if metric == "node_power":
        return max(float(value), 0.0)
    return float(value)


def _inverse_quantiles(q_scaled: np.ndarray, scaler, target_indices: List[int]) -> np.ndarray:
    target_mean = scaler.mean[list(target_indices)].reshape(1, 1, -1, 1)
    target_std = scaler.std[list(target_indices)].reshape(1, 1, -1, 1)
    return q_scaled * target_std + target_mean


def _confidence_interval(
    point_values: Dict[str, float],
    q_values: Dict[str, Dict[str, float]],
    metadata: Dict[str, object],
    horizon: int,
    target_columns: List[str],
) -> Dict[str, Dict[str, Optional[float]]]:
    quantile_labels = sorted(q_values)
    lower_values: Dict[str, Optional[float]] = {}
    upper_values: Dict[str, Optional[float]] = {}
    calibration = dict(_uncertainty_config(metadata).get("calibration", {}))
    conformal = dict(calibration.get("conformal_abs_error_p90", {}))

    for target in target_columns:
        lower = None
        upper = None
        if "p10" in q_values and target in q_values["p10"]:
            lower = q_values["p10"][target]
        elif quantile_labels and target in q_values[quantile_labels[0]]:
            lower = q_values[quantile_labels[0]][target]
        if "p90" in q_values and target in q_values["p90"]:
            upper = q_values["p90"][target]
        elif quantile_labels and target in q_values[quantile_labels[-1]]:
            upper = q_values[quantile_labels[-1]][target]

        residual = float(conformal.get(f"{target}_h{horizon}", 0.0) or 0.0)
        point = float(point_values[target])
        lower = point - residual if lower is None else min(float(lower), point - residual)
        upper = point + residual if upper is None else max(float(upper), point + residual)
        lower_values[target] = round(_clip_metric_value(target, float(lower)), 2)
        upper_values[target] = round(_clip_metric_value(target, float(upper)), 2)

    return {"lower": lower_values, "upper": upper_values}


def _compute_risk_outputs(
    metrics_pred: Dict[str, float],
    quantile_values: Dict[str, Dict[str, float]],
    confidence_interval: Dict[str, Dict[str, float]],
    thresholds: Dict[str, float],
) -> Dict[str, object]:
    overload_risk: Dict[str, float] = {}
    for metric, value in metrics_pred.items():
        threshold = thresholds.get(metric)
        if threshold is None:
            continue
        quantile_metric_values = [
            float(values[metric])
            for values in quantile_values.values()
            if values.get(metric) is not None
        ]
        if quantile_metric_values:
            overload_risk[metric] = round(float(np.mean(np.asarray(quantile_metric_values) >= threshold)), 4)
        else:
            upper = confidence_interval.get("upper", {}).get(metric)
            risk_value = _deterministic_risk(value, threshold)
            if upper is not None and upper >= threshold:
                risk_value = max(risk_value, 0.5)
            overload_risk[metric] = round(risk_value, 4)

    slo_violation_risk = max(overload_risk.values(), default=0.0)
    return {
        "overload_risk": overload_risk,
        "slo_violation_risk": round(float(slo_violation_risk), 4),
        "risk_level": _risk_level_from_probability(slo_violation_risk),
    }


def _make_model(model_name: str, metadata: Dict[str, object], model_classes):
    PatchTST, ITransformer, HeteroSTGNNLoadNet = model_classes
    params = dict(metadata["model_params"])
    context_payload = dict(metadata.get("context_encoder", {}))
    cat_cardinalities = context_payload.get("cat_cardinalities", [])
    n_numeric_context = len(context_payload.get("numeric_columns", []))
    common = {
        "n_features": len(metadata["feature_columns"]),
        "target_indices": list(metadata["target_indices"]),
        "pred_len": int(metadata["pred_len"]),
        "d_model": int(params["d_model"]),
        "n_heads": int(params["n_heads"]),
        "e_layers": int(params["e_layers"]),
        "d_ff": int(params["d_ff"]),
        "dropout": float(params["dropout"]),
        "cat_cardinalities": cat_cardinalities,
        "n_numeric_context": n_numeric_context,
        "quantiles": _quantiles_from_metadata(metadata),
    }
    if model_name == "patchtst":
        return PatchTST(
            **common,
            patch_len=int(params["patch_len"]),
            patch_stride=int(params["patch_stride"]),
        )
    if model_name == "itransformer":
        return ITransformer(seq_len=int(metadata["seq_len"]), **common)
    if model_name == "heterostgnn":
        return HeteroSTGNNLoadNet(
            seq_len=int(metadata["seq_len"]),
            graph_layers=int(params.get("graph_layers", 2)),
            **common,
        )
    raise ValueError(f"Unsupported model: {model_name}")


def predict_for_node(node_uuid: str, cluster_uuid: str, horizons: List[int], metadata: Dict[str, object], model, device, torch):
    seq_len = int(metadata["seq_len"])
    resample_freq = str(metadata["resample_freq"])
    max_missing_ratio = float(metadata["max_missing_ratio"])
    feature_columns = list(metadata["feature_columns"])
    target_columns = list(metadata["target_columns"])
    target_indices = list(metadata["target_indices"])
    horizon_steps = {int(k): int(v) for k, v in dict(metadata["horizon_steps"]).items()}
    scaler = deep_dataset.TimeSeriesScaler.from_metadata(dict(metadata["scaler"]))
    context_encoder = deep_dataset.ContextFeatureEncoder.from_metadata(dict(metadata.get("context_encoder", {})))
    quantiles = _quantiles_from_metadata(metadata)
    uncertainty_enabled = bool(quantiles)

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
    lookback_minutes = max(seq_len * 24, seq_len + max(horizons) + 30)
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
    if df.empty:
        return {"error": f"数据不足，缺少节点 {node_uuid} 的近期数据"}

    df_node = df[df["node_uuid"] == node_uuid].copy()
    series = deep_dataset.prepare_node_series(df_node, feature_columns, resample_freq, max_missing_ratio)
    if len(series) < seq_len:
        return {"error": f"重采样后数据不足，仅有 {len(series)} 条，需要 {seq_len} 条"}

    context_cat = np.empty((1, 0), dtype=np.int64)
    context_num = np.empty((1, 0), dtype=np.float32)
    if context_encoder.categorical_columns or context_encoder.numeric_columns:
        context_series = deep_dataset.prepare_node_context_series(
            df_node,
            context_encoder.categorical_columns,
            context_encoder.numeric_columns,
            resample_freq,
        )
        context_series = context_series.set_index("metric_time").reindex(pd.to_datetime(series["metric_time"]))
        context_series = context_series.ffill().bfill()
        latest_context = context_series.iloc[-1]
        raw_cat = np.asarray(
            [[latest_context.get(col, deep_dataset.UNKNOWN_CATEGORY) for col in context_encoder.categorical_columns]],
            dtype=object,
        )
        raw_num = np.asarray(
            [[latest_context.get(col, 0.0) for col in context_encoder.numeric_columns]],
            dtype=np.float32,
        )
        context_cat = context_encoder.transform_cat(raw_cat)
        context_num = context_encoder.transform_num(raw_num)

    rated_power = None
    if "rated_power" in df_node.columns:
        rated_vals = pd.to_numeric(df_node["rated_power"], errors="coerce").dropna()
        if not rated_vals.empty:
            rated_power = float(rated_vals.iloc[-1])

    X = series[feature_columns].tail(seq_len).to_numpy(dtype=np.float32)[None, :, :]
    X_scaled = scaler.transform_X(X)
    latest_time = pd.Timestamp(series["metric_time"].iloc[-1])

    model.eval()
    with torch.no_grad():
        output = model(
            torch.from_numpy(X_scaled).to(device),
            torch.from_numpy(context_cat).to(device),
            torch.from_numpy(context_num).to(device),
            return_quantiles=uncertainty_enabled,
        )
        if isinstance(output, dict):
            pred_scaled = output["point"].detach().cpu().numpy()
            q_scaled = output.get("quantiles")
            quantile_pred = _inverse_quantiles(q_scaled.detach().cpu().numpy(), scaler, target_indices)[0] if q_scaled is not None else None
        else:
            pred_scaled = output.detach().cpu().numpy()
            quantile_pred = None
    pred = scaler.inverse_y(pred_scaled, target_indices)[0]

    thresholds = _get_risk_thresholds(df_node, rated_power)
    predictions = []
    for horizon in horizons:
        step = horizon_steps.get(horizon)
        if not step:
            return {"error": f"模型未训练 horizon={horizon}"}
        idx = step - 1
        if idx >= pred.shape[0]:
            return {"error": f"horizon={horizon} 超出模型预测长度"}

        metrics_pred = {
            target: round(_clip_metric_value(target, float(pred[idx, col_idx])), 2)
            for col_idx, target in enumerate(target_columns)
        }
        quantile_values: Dict[str, Dict[str, float]] = {}
        if quantile_pred is not None:
            for q_idx, quantile in enumerate(quantiles):
                label = f"p{int(round(quantile * 100))}"
                quantile_values[label] = _metric_payload({
                    target: float(quantile_pred[idx, col_idx, q_idx])
                    for col_idx, target in enumerate(target_columns)
                })
        confidence_interval = _confidence_interval(
            metrics_pred,
            quantile_values,
            metadata,
            horizon,
            target_columns,
        ) if uncertainty_enabled else {}
        risk_outputs = _compute_risk_outputs(
            metrics_pred,
            quantile_values,
            confidence_interval,
            thresholds,
        )
        forecast_time = (latest_time + timedelta(minutes=horizon)).strftime("%Y-%m-%d %H:%M:%S")
        item = {
            "horizon": horizon,
            "forecast_time": forecast_time,
            "metrics": metrics_pred,
            "risk_level": risk_outputs["risk_level"] if uncertainty_enabled else _compute_risk_level(metrics_pred, thresholds),
        }
        if uncertainty_enabled:
            item["quantiles"] = quantile_values
            item["confidence_interval"] = confidence_interval
            item["overload_risk"] = risk_outputs["overload_risk"]
            item["slo_violation_risk"] = risk_outputs["slo_violation_risk"]
        predictions.append(item)

    return {"predictions": predictions}


def main():
    parser = argparse.ArgumentParser(description="Predict using heterogeneous PatchTST/iTransformer/Hetero-STGNN models")
    parser.add_argument("--model", choices=["patchtst", "itransformer", "heterostgnn", "hetero_stgnn"], default="patchtst")
    parser.add_argument("--cluster-uuid", required=True)
    parser.add_argument("--node-uuids", required=True, help="comma-separated node UUIDs")
    parser.add_argument("--horizons", default=None)
    parser.add_argument("--device", default="auto", choices=["auto", "cpu", "cuda", "mps"])
    args = parser.parse_args()
    args.model = _normalize_model_name(args.model)

    try:
        torch, PatchTST, ITransformer, HeteroSTGNNLoadNet = _import_torch()
    except RuntimeError as exc:
        print(f"[ERROR] {exc}", file=sys.stderr)
        sys.exit(1)
    metadata = model_registry.load_deep_metadata(args.model)
    horizons = _parse_list(args.horizons, int) if args.horizons else [int(h) for h in metadata["horizons"]]
    node_uuids = _parse_list(args.node_uuids)

    if args.device == "auto":
        if torch.cuda.is_available():
            device = torch.device("cuda")
        elif getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
            device = torch.device("mps")
        else:
            device = torch.device("cpu")
    else:
        device = torch.device(args.device)

    model = _make_model(args.model, metadata, (PatchTST, ITransformer, HeteroSTGNNLoadNet)).to(device)
    checkpoint_path = model_registry.get_deep_model_path(args.model)
    if not checkpoint_path.exists():
        raise FileNotFoundError(f"Deep model checkpoint not found: {checkpoint_path}")
    checkpoint = torch.load(checkpoint_path, map_location=device)
    model.load_state_dict(checkpoint["model_state"])

    results = []
    for node_uuid in node_uuids:
        print(f"[INFO] Predicting with {args.model} for node {node_uuid}", file=sys.stderr)
        result = predict_for_node(node_uuid, args.cluster_uuid, horizons, metadata, model, device, torch)
        if "error" in result:
            results.append({"node_uuid": node_uuid, "error": result["error"]})
        else:
            results.append({"node_uuid": node_uuid, "predictions": result["predictions"]})

    output = {
        "cluster_uuid": args.cluster_uuid,
        "horizons": horizons,
        "model_version": metadata.get("model_version", f"{args.model}_v0.2"),
        "model_name": args.model,
        "results": results,
    }
    print(json.dumps(output, ensure_ascii=False))


if __name__ == "__main__":
    main()
