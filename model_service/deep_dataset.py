"""Sequence dataset utilities for deep multivariate forecasting.

This module keeps the second-stage models close to the raw time series:
history windows are shaped as [seq_len, n_features], and labels are future
multi-target series shaped as [pred_len, n_targets].
"""
from dataclasses import dataclass
from typing import Dict, Iterable, List, Mapping, Sequence, Tuple

import numpy as np
import pandas as pd


DEFAULT_INPUT_FEATURES = [
    "cpu_util",
    "gpu_util",
    "gpu_mem_util",
    "mem_util",
    "node_power",
    "disk_io",
    "net_io",
]
DEFAULT_TARGET_COLS = ["cpu_util", "gpu_util", "gpu_mem_util", "node_power"]
DEFAULT_CONTEXT_CATEGORICAL_COLS = [
    "cpu_model",
    "gpu_model",
    "node_type",
    "task_type",
    "schedule_policy",
]
DEFAULT_CONTEXT_NUMERIC_COLS = [
    "gpu_count",
    "gpu_mem_gb",
    "rated_power",
    "replicas",
    "requests",
    "limits",
    "batch_size",
    "concurrency",
    "request_rate",
]
UNKNOWN_CATEGORY = "__unknown__"


@dataclass
class SequenceSamples:
    X: np.ndarray
    y: np.ndarray
    sample_times: List[pd.Timestamp]
    target_times: List[pd.Timestamp]
    node_uuids: List[str]
    feature_columns: List[str]
    target_columns: List[str]
    target_indices: List[int]
    context_cat: np.ndarray
    context_num: np.ndarray
    context_cat_columns: List[str]
    context_num_columns: List[str]


@dataclass
class TimeSeriesScaler:
    mean: np.ndarray
    std: np.ndarray
    feature_columns: List[str]

    @classmethod
    def fit(cls, X: np.ndarray, feature_columns: Sequence[str]) -> "TimeSeriesScaler":
        flat = X.reshape(-1, X.shape[-1])
        mean = np.nanmean(flat, axis=0)
        std = np.nanstd(flat, axis=0)
        mean = np.nan_to_num(mean, nan=0.0)
        std = np.nan_to_num(std, nan=1.0)
        std[std < 1e-6] = 1.0
        return cls(mean=mean, std=std, feature_columns=list(feature_columns))

    @classmethod
    def from_metadata(cls, payload: Dict[str, object]) -> "TimeSeriesScaler":
        return cls(
            mean=np.asarray(payload["mean"], dtype=np.float32),
            std=np.asarray(payload["std"], dtype=np.float32),
            feature_columns=list(payload["feature_columns"]),
        )

    def to_metadata(self) -> Dict[str, object]:
        return {
            "mean": self.mean.tolist(),
            "std": self.std.tolist(),
            "feature_columns": self.feature_columns,
        }

    def transform_X(self, X: np.ndarray) -> np.ndarray:
        return ((X - self.mean) / self.std).astype(np.float32)

    def transform_y(self, y: np.ndarray, target_indices: Sequence[int]) -> np.ndarray:
        target_mean = self.mean[list(target_indices)]
        target_std = self.std[list(target_indices)]
        return ((y - target_mean) / target_std).astype(np.float32)

    def inverse_y(self, y_scaled: np.ndarray, target_indices: Sequence[int]) -> np.ndarray:
        target_mean = self.mean[list(target_indices)]
        target_std = self.std[list(target_indices)]
        return y_scaled * target_std + target_mean


@dataclass
class ContextFeatureEncoder:
    categorical_columns: List[str]
    numeric_columns: List[str]
    category_maps: Dict[str, Dict[str, int]]
    numeric_mean: np.ndarray
    numeric_std: np.ndarray

    @classmethod
    def fit(
        cls,
        cat_values: np.ndarray,
        num_values: np.ndarray,
        categorical_columns: Sequence[str],
        numeric_columns: Sequence[str],
    ) -> "ContextFeatureEncoder":
        category_maps: Dict[str, Dict[str, int]] = {}
        for idx, col in enumerate(categorical_columns):
            values = [_clean_category(value) for value in cat_values[:, idx]] if len(cat_values) else []
            unique_values = sorted(value for value in set(values) if value != UNKNOWN_CATEGORY)
            category_maps[col] = {value: code for code, value in enumerate(unique_values, start=1)}

        if len(numeric_columns):
            numeric = np.asarray(num_values, dtype=np.float32)
            mean = np.nanmean(numeric, axis=0) if len(numeric) else np.zeros(len(numeric_columns), dtype=np.float32)
            std = np.nanstd(numeric, axis=0) if len(numeric) else np.ones(len(numeric_columns), dtype=np.float32)
            mean = np.nan_to_num(mean, nan=0.0).astype(np.float32)
            std = np.nan_to_num(std, nan=1.0).astype(np.float32)
            std[std < 1e-6] = 1.0
        else:
            mean = np.empty((0,), dtype=np.float32)
            std = np.empty((0,), dtype=np.float32)

        return cls(
            categorical_columns=list(categorical_columns),
            numeric_columns=list(numeric_columns),
            category_maps=category_maps,
            numeric_mean=mean,
            numeric_std=std,
        )

    @classmethod
    def from_metadata(cls, payload: Mapping[str, object]) -> "ContextFeatureEncoder":
        return cls(
            categorical_columns=list(payload.get("categorical_columns", [])),
            numeric_columns=list(payload.get("numeric_columns", [])),
            category_maps={
                str(col): {str(key): int(value) for key, value in dict(mapping).items()}
                for col, mapping in dict(payload.get("category_maps", {})).items()
            },
            numeric_mean=np.asarray(payload.get("numeric_mean", []), dtype=np.float32),
            numeric_std=np.asarray(payload.get("numeric_std", []), dtype=np.float32),
        )

    @property
    def cat_cardinalities(self) -> List[int]:
        return [max(mapping.values(), default=0) + 1 for mapping in self.category_maps.values()]

    def to_metadata(self) -> Dict[str, object]:
        return {
            "categorical_columns": self.categorical_columns,
            "numeric_columns": self.numeric_columns,
            "category_maps": self.category_maps,
            "numeric_mean": self.numeric_mean.tolist(),
            "numeric_std": self.numeric_std.tolist(),
            "cat_cardinalities": self.cat_cardinalities,
        }

    def transform_cat(self, cat_values: np.ndarray) -> np.ndarray:
        if not self.categorical_columns:
            return np.empty((len(cat_values), 0), dtype=np.int64)
        encoded = np.zeros((len(cat_values), len(self.categorical_columns)), dtype=np.int64)
        for idx, col in enumerate(self.categorical_columns):
            mapping = self.category_maps.get(col, {})
            for row_idx, value in enumerate(cat_values[:, idx]):
                encoded[row_idx, idx] = mapping.get(_clean_category(value), 0)
        return encoded

    def transform_num(self, num_values: np.ndarray) -> np.ndarray:
        if not self.numeric_columns:
            return np.empty((len(num_values), 0), dtype=np.float32)
        numeric = np.asarray(num_values, dtype=np.float32)
        numeric = np.nan_to_num(numeric, nan=0.0, posinf=0.0, neginf=0.0)
        return ((numeric - self.numeric_mean) / self.numeric_std).astype(np.float32)


def _clean_category(value: object) -> str:
    if value is None or (isinstance(value, float) and np.isnan(value)):
        return UNKNOWN_CATEGORY
    text = str(value).strip()
    return text if text else UNKNOWN_CATEGORY


def parse_freq_seconds(freq: str) -> int:
    return int(pd.to_timedelta(freq).total_seconds())


def horizons_to_steps(horizons: Iterable[int], resample_freq: str) -> Dict[int, int]:
    freq_seconds = parse_freq_seconds(resample_freq)
    steps = {}
    for horizon in horizons:
        horizon_seconds = int(horizon) * 60
        step = int(round(horizon_seconds / freq_seconds))
        steps[int(horizon)] = max(step, 1)
    return steps


def prepare_node_series(
    group: pd.DataFrame,
    feature_columns: Sequence[str],
    resample_freq: str,
    max_missing_ratio: float,
) -> pd.DataFrame:
    frame = group.copy()
    frame["metric_time"] = pd.to_datetime(frame["metric_time"])
    frame = frame.set_index("metric_time").sort_index()

    numeric = pd.DataFrame(index=frame.index)
    for col in feature_columns:
        if col in frame.columns:
            numeric[col] = pd.to_numeric(frame[col], errors="coerce")
        else:
            numeric[col] = np.nan

    resampled = numeric.resample(resample_freq).mean()
    missing_ratio = resampled.isna().sum(axis=1) / max(len(feature_columns), 1)
    resampled = resampled.ffill()
    resampled = resampled[missing_ratio <= max_missing_ratio]
    resampled = resampled.replace([np.inf, -np.inf], np.nan)
    resampled = resampled.ffill().bfill().fillna(0.0)
    return resampled.reset_index()


def prepare_node_context_series(
    group: pd.DataFrame,
    categorical_columns: Sequence[str],
    numeric_columns: Sequence[str],
    resample_freq: str,
) -> pd.DataFrame:
    frame = group.copy()
    frame["metric_time"] = pd.to_datetime(frame["metric_time"])
    frame = frame.set_index("metric_time").sort_index()

    pieces = []
    if categorical_columns:
        categorical = pd.DataFrame(index=frame.index)
        for col in categorical_columns:
            if col in frame.columns:
                categorical[col] = frame[col].map(_clean_category)
            else:
                categorical[col] = UNKNOWN_CATEGORY
        pieces.append(categorical.resample(resample_freq).ffill().bfill().fillna(UNKNOWN_CATEGORY))

    if numeric_columns:
        numeric = pd.DataFrame(index=frame.index)
        for col in numeric_columns:
            if col in frame.columns:
                numeric[col] = pd.to_numeric(frame[col], errors="coerce")
            else:
                numeric[col] = np.nan
        pieces.append(numeric.resample(resample_freq).mean().ffill().bfill().fillna(0.0))

    if not pieces:
        return pd.DataFrame({"metric_time": pd.to_datetime(frame.index.unique()).sort_values()})

    context = pd.concat(pieces, axis=1)
    context = context.replace([np.inf, -np.inf], np.nan)
    for col in categorical_columns:
        if col in context.columns:
            context[col] = context[col].map(_clean_category)
    for col in numeric_columns:
        if col in context.columns:
            context[col] = pd.to_numeric(context[col], errors="coerce").fillna(0.0)
    return context.reset_index()


def build_sequence_samples(
    df: pd.DataFrame,
    seq_len: int,
    pred_len: int,
    feature_columns: Sequence[str] = DEFAULT_INPUT_FEATURES,
    target_columns: Sequence[str] = DEFAULT_TARGET_COLS,
    resample_freq: str = "1min",
    max_missing_ratio: float = 0.3,
    stride: int = 1,
    context_categorical_columns: Sequence[str] = DEFAULT_CONTEXT_CATEGORICAL_COLS,
    context_numeric_columns: Sequence[str] = DEFAULT_CONTEXT_NUMERIC_COLS,
) -> SequenceSamples:
    feature_columns = list(feature_columns)
    target_columns = list(target_columns)
    missing_targets = [col for col in target_columns if col not in feature_columns]
    if missing_targets:
        raise ValueError(f"target columns must be in feature columns: {missing_targets}")

    target_indices = [feature_columns.index(col) for col in target_columns]
    context_categorical_columns = list(context_categorical_columns)
    context_numeric_columns = list(context_numeric_columns)
    X_rows: List[np.ndarray] = []
    y_rows: List[np.ndarray] = []
    context_cat_rows: List[List[str]] = []
    context_num_rows: List[np.ndarray] = []
    sample_times: List[pd.Timestamp] = []
    target_times: List[pd.Timestamp] = []
    node_uuids: List[str] = []

    for node_uuid, group in df.groupby("node_uuid"):
        series = prepare_node_series(group, feature_columns, resample_freq, max_missing_ratio)
        if len(series) < seq_len + pred_len:
            continue
        context = prepare_node_context_series(
            group,
            context_categorical_columns,
            context_numeric_columns,
            resample_freq,
        )
        context = context.set_index("metric_time").reindex(pd.to_datetime(series["metric_time"]))
        context = context.ffill().bfill()
        for col in context_categorical_columns:
            if col not in context.columns:
                context[col] = UNKNOWN_CATEGORY
            context[col] = context[col].map(_clean_category)
        for col in context_numeric_columns:
            if col not in context.columns:
                context[col] = 0.0
            context[col] = pd.to_numeric(context[col], errors="coerce").fillna(0.0)

        values = series[feature_columns].to_numpy(dtype=np.float32)
        targets = series[target_columns].to_numpy(dtype=np.float32)
        context_cat_values = context[context_categorical_columns].to_numpy(dtype=object)
        context_num_values = context[context_numeric_columns].to_numpy(dtype=np.float32)
        times = pd.to_datetime(series["metric_time"]).to_list()
        last_start = len(series) - seq_len - pred_len

        for start in range(0, last_start + 1, max(stride, 1)):
            hist_end = start + seq_len
            fut_end = hist_end + pred_len
            X_rows.append(values[start:hist_end])
            y_rows.append(targets[hist_end:fut_end])
            context_cat_rows.append([_clean_category(value) for value in context_cat_values[hist_end - 1]])
            context_num_rows.append(context_num_values[hist_end - 1])
            sample_times.append(pd.Timestamp(times[hist_end - 1]))
            target_times.append(pd.Timestamp(times[fut_end - 1]))
            node_uuids.append(str(node_uuid))

    if not X_rows:
        empty_X = np.empty((0, seq_len, len(feature_columns)), dtype=np.float32)
        empty_y = np.empty((0, pred_len, len(target_columns)), dtype=np.float32)
        empty_cat = np.empty((0, len(context_categorical_columns)), dtype=object)
        empty_num = np.empty((0, len(context_numeric_columns)), dtype=np.float32)
        return SequenceSamples(
            empty_X,
            empty_y,
            [],
            [],
            [],
            feature_columns,
            target_columns,
            target_indices,
            empty_cat,
            empty_num,
            context_categorical_columns,
            context_numeric_columns,
        )

    return SequenceSamples(
        X=np.stack(X_rows).astype(np.float32),
        y=np.stack(y_rows).astype(np.float32),
        sample_times=sample_times,
        target_times=target_times,
        node_uuids=node_uuids,
        feature_columns=feature_columns,
        target_columns=target_columns,
        target_indices=target_indices,
        context_cat=np.asarray(context_cat_rows, dtype=object),
        context_num=np.stack(context_num_rows).astype(np.float32),
        context_cat_columns=context_categorical_columns,
        context_num_columns=context_numeric_columns,
    )


def chronological_split_indices(
    sample_times: Sequence[pd.Timestamp],
    train_ratio: float = 0.7,
    valid_ratio: float = 0.15,
) -> Tuple[np.ndarray, np.ndarray, np.ndarray]:
    order = np.argsort(np.asarray(sample_times, dtype="datetime64[ns]"))
    n = len(order)
    train_end = int(n * train_ratio)
    valid_end = train_end + int(n * valid_ratio)
    return order[:train_end], order[train_end:valid_end], order[valid_end:]
