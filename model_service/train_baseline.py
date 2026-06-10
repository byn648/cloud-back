"""LightGBM baseline training script.

Trains separate models for cpu_util, gpu_util, gpu_mem_util, node_power
across multiple horizons. Train/valid/test split is strictly chronological.
"""
import sys
import argparse
import json
from datetime import datetime
from typing import List, Dict, Any

import numpy as np
import pandas as pd
import lightgbm as lgb

from . import config
from . import data_loader
from . import feature_builder
from . import evaluator
from . import model_registry

TARGET_COLS = ["cpu_util", "gpu_util", "gpu_mem_util", "node_power"]
LGBM_PARAMS = {
    "objective": "regression",
    "metric": "mae",
    "boosting_type": "gbdt",
    "num_leaves": 31,
    "learning_rate": 0.05,
    "feature_fraction": 0.8,
    "bagging_fraction": 0.8,
    "bagging_freq": 5,
    "verbose": -1,
    "n_jobs": -1,
}


def _split_chronological(df: pd.DataFrame, ratios=(0.7, 0.15, 0.15)):
    n = len(df)
    t1 = int(n * ratios[0])
    t2 = t1 + int(n * ratios[1])
    train = df.iloc[:t1].copy()
    valid = df.iloc[t1:t2].copy()
    test = df.iloc[t2:].copy()
    return train, valid, test


def _train_one_target(
    X: pd.DataFrame,
    y: pd.Series,
    sample_times: pd.Series,
    feature_cols: List[str],
    target: str,
    horizon: int,
    lgbm_params: Dict[str, Any],
) -> Dict[str, Any]:
    y_clean = y.dropna()
    valid_idx = y_clean.index.intersection(X.dropna(subset=feature_cols).index)

    if len(valid_idx) < 50:
        return {"target": target, "horizon": horizon, "skipped": True, "reason": "insufficient samples"}

    target_col = f"{target}_target"
    mask = ~y.isna()
    X_valid = X.loc[mask, feature_cols].values
    y_valid = y.loc[mask].values
    times_valid = sample_times.loc[mask].values

    order = np.argsort(times_valid)
    X_ordered = X_valid[order]
    y_ordered = y_valid[order]
    times_ordered = times_valid[order]

    n = len(y_ordered)
    t1 = int(n * 0.7)
    t2 = t1 + int(n * 0.15)
    X_train, X_valid_split, X_test = X_ordered[:t1], X_ordered[t1:t2], X_ordered[t2:]
    y_train, y_valid_split, y_test = y_ordered[:t1], y_ordered[t1:t2], y_ordered[t2:]

    for arr in [X_train, X_valid_split, X_test]:
        if arr.size == 0:
            return {"target": target, "horizon": horizon, "skipped": True, "reason": f"split produced empty set for {target}_h{horizon}"}

    model = lgb.LGBMRegressor(**lgbm_params, n_estimators=200)
    model.fit(
        X_train, y_train,
        eval_set=[(X_valid_split, y_valid_split)],
    )

    y_pred_test = model.predict(X_test)
    metrics = evaluator.evaluate_all(y_test, y_pred_test)

    model_path = model_registry.save_model(model, target, horizon)

    return {
        "target": target,
        "horizon": horizon,
        "skipped": False,
        "model_path": model_path,
        "metrics": {k: round(v, 4) if v is not None and not np.isnan(v) else None for k, v in metrics.items()},
        "train_samples": len(y_train),
        "test_samples": len(y_test),
    }


def main():
    parser = argparse.ArgumentParser(description="Train LightGBM baseline models")
    parser.add_argument("--cluster-uuid", required=True)
    parser.add_argument("--start-time", required=True)
    parser.add_argument("--end-time", required=True)
    parser.add_argument("--horizons", default="15,30,60")
    parser.add_argument("--target-cols", default=",".join(TARGET_COLS))
    parser.add_argument("--history-window", type=int, default=60)
    parser.add_argument("--resample-freq", default="1min")
    parser.add_argument("--device-type", choices=["cpu", "gpu", "cuda"], default="cpu")
    parser.add_argument("--gpu-platform-id", type=int, default=None)
    parser.add_argument("--gpu-device-id", type=int, default=None)
    args = parser.parse_args()

    horizons = [int(h) for h in args.horizons.split(",")]
    target_cols = [item.strip() for item in args.target_cols.split(",") if item.strip()]
    lgbm_params = dict(LGBM_PARAMS)
    if args.device_type != "cpu":
        lgbm_params["device_type"] = args.device_type
        if args.gpu_platform_id is not None:
            lgbm_params["gpu_platform_id"] = args.gpu_platform_id
        if args.gpu_device_id is not None:
            lgbm_params["gpu_device_id"] = args.gpu_device_id

    print(f"[INFO] Loading data for cluster {args.cluster_uuid}", file=sys.stderr)
    df = data_loader.load_metrics(
        cluster_uuid=args.cluster_uuid,
        start_time=args.start_time,
        end_time=args.end_time,
    )

    if df.empty:
        print("[ERROR] No data loaded from green_node_metrics", file=sys.stderr)
        sys.exit(1)

    print(f"[INFO] Building samples: history_window={args.history_window}, horizons={horizons}", file=sys.stderr)
    X_df, y_df = feature_builder.build_samples(
        df,
        history_window=args.history_window,
        horizons=horizons,
        resample_freq=args.resample_freq,
    )

    if X_df.empty or y_df.empty:
        print("[ERROR] No samples generated", file=sys.stderr)
        sys.exit(1)

    feature_cols = feature_builder.get_feature_columns(X_df)
    print(f"[INFO] Feature columns ({len(feature_cols)}): {feature_cols[:5]}...", file=sys.stderr)

    warnings = []
    all_results = {}
    model_files = []

    for target in target_cols:
        target_col = f"{target}_target"
        if target_col not in y_df.columns:
            warnings.append(f"{target}: column '{target_col}' not found in y_df, skipping")
            continue

        for horizon in horizons:
            subset = y_df[y_df["horizon"] == horizon].copy()
            if subset.empty:
                warnings.append(f"{target}_h{horizon}: no samples for this horizon")
                continue

            X_subset = X_df.reset_index(drop=True)
            if len(X_subset) != len(subset):
                warnings.append(
                    f"{target}_h{horizon}: feature/target sample count mismatch "
                    f"({len(X_subset)} vs {len(subset)}), skipping"
                )
                continue

            sample_times = subset["sample_time"].reset_index(drop=True)
            y_target = subset[target_col].reset_index(drop=True)

            if y_target.isna().all():
                warnings.append(f"{target}_h{horizon}: all target values are NaN, skipping")
                continue

            result = _train_one_target(
                X_subset, y_target, sample_times,
                feature_cols, target, horizon, lgbm_params,
            )

            key = f"{target}_h{horizon}"
            all_results[key] = result

            if not result.get("skipped"):
                model_files.append(result["model_path"])
                print(f"[INFO] Trained {key}: MAE={result['metrics'].get('mae')}, RMSE={result['metrics'].get('rmse')}", file=sys.stderr)
            else:
                warnings.append(f"{target}_h{horizon}: {result.get('reason', 'unknown')}")

    train_start = args.start_time
    train_end = args.end_time
    created_at = datetime.now().isoformat()

    metadata = {
        "feature_columns": feature_cols,
        "target_columns": target_cols,
        "horizons": horizons,
        "resample_freq": args.resample_freq,
        "history_window": args.history_window,
        "model_version": config.MODEL_VERSION,
        "model_files": model_files,
        "train_data_range": {"start": train_start, "end": train_end},
        "created_at": created_at,
        "metrics": {
            k: v.get("metrics", {}) for k, v in all_results.items() if not v.get("skipped")
        },
        "warnings": warnings,
    }

    model_registry.save_metadata(metadata)
    print(f"[INFO] Metadata saved. {len(model_files)} models trained. {len(warnings)} warnings.", file=sys.stderr)
    print(json.dumps(metadata, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
