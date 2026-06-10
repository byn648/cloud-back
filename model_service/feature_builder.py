"""Sliding-window feature builder for node metrics.

For each node, resamples to fixed frequency, forward-fills short gaps,
drops windows with excessive missing data, then builds statistical features
(mean/max/min/std/last/trend) from the history window to predict targets
at future horizons.
"""
import sys
from typing import List, Tuple, Optional
from datetime import datetime, timedelta

import numpy as np
import pandas as pd

from . import config

RAW_FEATURES = ["cpu_util", "gpu_util", "gpu_mem_util", "mem_util", "node_power", "disk_io", "net_io"]
TARGET_COLS = ["cpu_util", "gpu_util", "gpu_mem_util", "node_power"]


def _parse_freq(freq: str) -> int:
    return int(pd.to_timedelta(freq).total_seconds())


def _resample_and_fill(
    df: pd.DataFrame,
    freq: str,
    max_missing_ratio: float,
) -> pd.DataFrame:
    df = df.set_index("metric_time").sort_index()

    numeric_cols = [c for c in RAW_FEATURES if c in df.columns]
    df_numeric = df[numeric_cols] if numeric_cols else pd.DataFrame(index=df.index)

    resampled = df_numeric.resample(freq).mean()

    missing_before = resampled.isnull().sum(axis=1) / max(len(numeric_cols), 1)
    resampled["_missing_ratio"] = missing_before

    resampled["_orig_index"] = resampled.index
    forward_filled = resampled.ffill()

    gap_mask = forward_filled["_missing_ratio"] <= max_missing_ratio
    forward_filled = forward_filled[gap_mask]
    forward_filled = forward_filled.drop(columns=["_missing_ratio", "_orig_index"])

    return forward_filled.reset_index().rename(columns={"metric_time": "metric_time"})


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


def _build_features_for_window(
    window_data: pd.DataFrame,
    history_steps: int,
) -> Optional[dict]:
    stats = {}
    for col in RAW_FEATURES:
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
    return stats


def build_samples(
    df: pd.DataFrame,
    history_window: int,
    horizons: List[int],
    resample_freq: str = "1min",
    max_missing_ratio: float = 0.3,
) -> Tuple[pd.DataFrame, pd.DataFrame]:
    freq_seconds = _parse_freq(resample_freq)
    history_steps = int(history_window * 60 / freq_seconds)

    node_groups = df.groupby("node_uuid")
    all_samples = []
    all_targets = []

    for node_uuid, group in node_groups:
        resampled = _resample_and_fill(group, resample_freq, max_missing_ratio)
        if resampled.empty or len(resampled) < history_steps + max(horizons):
            print(f"[WARN] Node {node_uuid}: insufficient data after resampling, skipping", file=sys.stderr)
            continue

        resampled = resampled.reset_index(drop=True)
        timestamps = resampled["metric_time"].values

        for i in range(len(resampled) - history_steps - max(horizons) + 1):
            window = resampled.iloc[i : i + history_steps]
            window_times = timestamps[i : i + history_steps]

            window_start_time = pd.Timestamp(window_times[0])
            sample_time = pd.Timestamp(window_times[-1])

            feat = _build_features_for_window(window, history_steps)
            if feat is None:
                continue

            feat["window_start_time"] = window_start_time
            feat["sample_time"] = sample_time
            feat["node_uuid"] = node_uuid
            all_samples.append(feat)

            for horizon in horizons:
                target_idx = i + history_steps + horizon - 1
                if target_idx >= len(resampled):
                    continue
                row = {"window_start_time": window_start_time, "sample_time": sample_time}
                row["target_time"] = pd.Timestamp(timestamps[target_idx])
                row["node_uuid"] = node_uuid
                row["horizon"] = horizon
                for col in TARGET_COLS:
                    if col in resampled.columns:
                        row[f"{col}_target"] = resampled.iloc[target_idx][col]
                    else:
                        row[f"{col}_target"] = np.nan
                all_targets.append(row)

    X_df = pd.DataFrame(all_samples)
    y_df = pd.DataFrame(all_targets)

    return X_df, y_df


def get_feature_columns(X_df: pd.DataFrame) -> List[str]:
    exclude = {"window_start_time", "sample_time", "node_uuid", "metric_time"}
    return [c for c in sorted(X_df.columns) if c not in exclude]


def get_target_columns() -> List[str]:
    return TARGET_COLS


if __name__ == "__main__":
    import argparse
    import json
    import data_loader

    parser = argparse.ArgumentParser()
    parser.add_argument("--cluster-uuid", required=True)
    parser.add_argument("--start-time", default=None)
    parser.add_argument("--end-time", default=None)
    parser.add_argument("--history-window", type=int, default=60)
    parser.add_argument("--horizons", default="15,30,60")
    parser.add_argument("--resample-freq", default="1min")
    args = parser.parse_args()

    horizons = [int(h) for h in args.horizons.split(",")]

    df = data_loader.load_metrics(
        cluster_uuid=args.cluster_uuid,
        start_time=args.start_time,
        end_time=args.end_time,
    )

    if df.empty:
        print("[ERROR] No data loaded, cannot build features", file=sys.stderr)
        sys.exit(1)

    X_df, y_df = build_samples(
        df, args.history_window, horizons, args.resample_freq
    )

    print(f"[INFO] Built {len(X_df)} samples, {len(y_df)} target records", file=sys.stderr)
    result = {
        "samples": X_df.to_dict(orient="records"),
        "targets": y_df.to_dict(orient="records"),
        "feature_columns": get_feature_columns(X_df),
        "target_columns": TARGET_COLS,
    }
    print(json.dumps(result))
