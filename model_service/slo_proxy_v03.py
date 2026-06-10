"""Stage V0.3 SLO proxy model.

The proxy model estimates how a service-level resource configuration will affect
future P95/P99 latency and SLO violation probability. It includes the V0.3
LightGBM engineering baseline, GRU/TCN sequence models, and a GNN predictor in
the "SLO budget -> resource config -> SLO risk" loop.
"""
import argparse
import json
import math
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Dict, Iterable, List, Tuple

import joblib
import numpy as np
import pandas as pd
import pymysql

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from model_service import config
else:
    from . import config


FEATURE_COLUMNS = [
    "qps_forecast",
    "qps_per_replica",
    "replica_count",
    "cpu_request",
    "gpu_request",
    "memory_request_gb",
    "p95_latency",
    "p99_latency",
    "error_rate",
    "p99_latency_budget",
    "error_rate_budget",
    "predicted_cpu_util",
    "predicted_gpu_util",
    "horizon_minutes",
    "latency_contribution",
    "budget_ratio",
    "critical_path",
    "risk_weight",
]
MODEL_PATH = config.CHECKPOINTS_DIR / "slo_proxy_v03.pkl"
METADATA_PATH = config.CHECKPOINTS_DIR / "slo_proxy_v03_metadata.json"
SEQUENCE_MODEL_TYPES = {"gru", "tcn"}
GNN_MODEL_TYPE = "gnn"
SCHEMA_PATHS = [
    config.BASE_DIR.parent / "sql" / "slo_v01.sql",
    config.BASE_DIR.parent / "sql" / "slo_v02.sql",
    config.BASE_DIR.parent / "sql" / "slo_v03.sql",
]
DEFAULT_OBJECTIVE_ID = "checkout-flow"
DEFAULT_BUSINESS_FLOW = "下单流程"


def _log_progress(message: str) -> None:
    print(f"[{datetime.now().strftime('%H:%M:%S')}] {message}", file=sys.stderr, flush=True)


def _sequence_model_path(model_type: str) -> Path:
    return config.CHECKPOINTS_DIR / f"slo_proxy_v03_{model_type.lower()}.pt"


def _sequence_metadata_path(model_type: str) -> Path:
    return config.CHECKPOINTS_DIR / f"slo_proxy_v03_{model_type.lower()}_metadata.json"


def _import_torch():
    try:
        import torch
        from torch import nn
        from torch.utils.data import DataLoader, TensorDataset
    except ModuleNotFoundError as exc:
        raise RuntimeError("torch is required for GRU/TCN models: python -m pip install torch") from exc
    return torch, nn, DataLoader, TensorDataset


def _torch_load(path: Path, device: Any) -> Dict[str, Any]:
    torch, _, _, _ = _import_torch()
    try:
        return torch.load(path, map_location=device, weights_only=False)
    except TypeError:
        return torch.load(path, map_location=device)


def _resolve_device(torch: Any, requested: str) -> Any:
    if requested == "auto":
        return torch.device("cuda" if torch.cuda.is_available() else "cpu")
    if requested.startswith("cuda") and not torch.cuda.is_available():
        raise RuntimeError("CUDA device requested but torch.cuda.is_available() is false")
    return torch.device(requested)


def _build_sequence_model(
    model_type: str,
    input_dim: int,
    hidden_dim: int,
    num_layers: int,
    dropout: float,
    kernel_size: int,
):
    torch, nn, _, _ = _import_torch()
    model_type = model_type.lower()

    class SLOSequenceGRU(nn.Module):
        def __init__(self) -> None:
            super().__init__()
            self.gru = nn.GRU(
                input_size=input_dim,
                hidden_size=hidden_dim,
                num_layers=num_layers,
                batch_first=True,
                dropout=dropout if num_layers > 1 else 0.0,
            )
            self.head = nn.Sequential(
                nn.LayerNorm(hidden_dim),
                nn.Linear(hidden_dim, hidden_dim),
                nn.ReLU(),
                nn.Dropout(dropout),
                nn.Linear(hidden_dim, 3),
            )

        def forward(self, x):
            out, _ = self.gru(x)
            return self.head(out[:, -1, :])

    class TemporalBlock(nn.Module):
        def __init__(self, in_channels: int, out_channels: int, dilation: int) -> None:
            super().__init__()
            padding = (kernel_size - 1) * dilation
            self.padding = padding
            self.conv1 = nn.Conv1d(in_channels, out_channels, kernel_size, padding=padding, dilation=dilation)
            self.conv2 = nn.Conv1d(out_channels, out_channels, kernel_size, padding=padding, dilation=dilation)
            self.residual = nn.Conv1d(in_channels, out_channels, 1) if in_channels != out_channels else nn.Identity()
            self.net = nn.Sequential(nn.ReLU(), nn.Dropout(dropout))

        def _crop(self, x):
            if self.padding == 0:
                return x
            return x[:, :, :-self.padding]

        def forward(self, x):
            out = self._crop(self.conv1(x))
            out = self.net(out)
            out = self._crop(self.conv2(out))
            out = self.net(out)
            return out + self.residual(x)

    class SLOSequenceTCN(nn.Module):
        def __init__(self) -> None:
            super().__init__()
            layers = []
            in_channels = input_dim
            for layer_idx in range(num_layers):
                dilation = 2 ** layer_idx
                layers.append(TemporalBlock(in_channels, hidden_dim, dilation))
                in_channels = hidden_dim
            self.network = nn.Sequential(*layers)
            self.head = nn.Sequential(
                nn.LayerNorm(hidden_dim),
                nn.Linear(hidden_dim, hidden_dim),
                nn.ReLU(),
                nn.Dropout(dropout),
                nn.Linear(hidden_dim, 3),
            )

        def forward(self, x):
            out = self.network(x.transpose(1, 2))
            return self.head(out[:, :, -1])

    if model_type == "gru":
        return SLOSequenceGRU()
    if model_type == "tcn":
        return SLOSequenceTCN()
    raise ValueError(f"Unsupported sequence model type: {model_type}")


def _build_gnn_model(
    input_dim: int,
    hidden_dim: int,
    num_layers: int,
    dropout: float,
    graph_layers: int,
):
    torch, nn, _, _ = _import_torch()

    class SLOGNNPredictor(nn.Module):
        def __init__(self) -> None:
            super().__init__()
            self.temporal = nn.GRU(
                input_size=input_dim,
                hidden_size=hidden_dim,
                num_layers=num_layers,
                batch_first=True,
                dropout=dropout if num_layers > 1 else 0.0,
            )
            self.graph_layers = nn.ModuleList([nn.Linear(hidden_dim, hidden_dim) for _ in range(graph_layers)])
            self.norms = nn.ModuleList([nn.LayerNorm(hidden_dim) for _ in range(graph_layers)])
            self.dropout = nn.Dropout(dropout)
            self.head = nn.Sequential(
                nn.LayerNorm(hidden_dim),
                nn.Linear(hidden_dim, hidden_dim),
                nn.ReLU(),
                nn.Dropout(dropout),
                nn.Linear(hidden_dim, 3),
            )

        def forward(self, x, adjacency):
            batch_size, node_count, seq_len, feature_dim = x.shape
            temporal_in = x.reshape(batch_size * node_count, seq_len, feature_dim)
            temporal_out, _ = self.temporal(temporal_in)
            h = temporal_out[:, -1, :].reshape(batch_size, node_count, -1)
            if adjacency.dim() == 2:
                adjacency_batch = adjacency.unsqueeze(0).expand(batch_size, -1, -1)
            else:
                adjacency_batch = adjacency
            for layer, norm in zip(self.graph_layers, self.norms):
                residual = h
                h = torch.bmm(adjacency_batch, h)
                h = layer(h)
                h = self.dropout(torch.relu(h))
                h = norm(h + residual)
            return self.head(h)

    return SLOGNNPredictor()


def _connect():
    return pymysql.connect(**config.DB_CONFIG)


def ensure_schema() -> None:
    with _connect() as conn:
        with conn.cursor() as cur:
            for schema_path in SCHEMA_PATHS:
                if not schema_path.exists():
                    raise RuntimeError(f"SLO schema file not found: {schema_path}")
                lines = []
                for line in schema_path.read_text(encoding="utf-8").splitlines():
                    stripped = line.strip()
                    if stripped.startswith("--"):
                        continue
                    lines.append(line)
                statements = [stmt.strip() for stmt in "\n".join(lines).split(";") if stmt.strip()]
                for stmt in statements:
                    cur.execute(stmt)
        conn.commit()


def _parse_horizons(value: str | Iterable[int]) -> List[int]:
    if isinstance(value, str):
        return [int(v.strip()) for v in value.split(",") if v.strip()]
    return [int(v) for v in value]


def _num(value, default: float = 0.0) -> float:
    if value is None:
        return default
    try:
        if pd.isna(value):
            return default
    except TypeError:
        pass
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def _int(value, default: int = 0) -> int:
    return int(round(_num(value, float(default))))


def _sigmoid(value: float) -> float:
    return 1.0 / (1.0 + math.exp(-value))


def _service_key(service_id: str, api_id: str) -> Tuple[str, str]:
    return str(service_id or ""), str(api_id or "")


def _service_name(service_id: str) -> str:
    if service_id == "api-gw":
        return "API 网关"
    if service_id == "model-inference":
        return "模型推理"
    if service_id == "data-process":
        return "数据处理"
    return service_id


def load_metrics(start_time: str | None, end_time: str | None, limit: int | None = None) -> pd.DataFrame:
    ensure_schema()
    sql = "SELECT * FROM slo_metric_ts WHERE 1=1"
    params: List[object] = []
    if start_time:
        sql += " AND metric_time >= %s"
        params.append(start_time)
    if end_time:
        sql += " AND metric_time <= %s"
        params.append(end_time)
    sql += " ORDER BY service_id ASC, api_id ASC, metric_time ASC"
    if limit:
        sql += " LIMIT %s"
        params.append(limit)
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.execute(sql, params)
            rows = cur.fetchall()
    frame = pd.DataFrame(rows)
    if not frame.empty:
        frame["metric_time"] = pd.to_datetime(frame["metric_time"])
    return frame


def load_latest_metrics() -> pd.DataFrame:
    ensure_schema()
    sql = """
SELECT m.*
FROM slo_metric_ts m
JOIN (
  SELECT cluster_uuid, service_id, api_id, MAX(metric_time) AS latest_time
  FROM slo_metric_ts
  GROUP BY cluster_uuid, service_id, api_id
) latest
  ON latest.cluster_uuid = m.cluster_uuid
 AND latest.service_id = m.service_id
 AND latest.api_id = m.api_id
 AND latest.latest_time = m.metric_time
ORDER BY m.service_id ASC, m.api_id ASC
"""
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.execute(sql)
            rows = cur.fetchall()
    frame = pd.DataFrame(rows)
    if not frame.empty:
        frame["metric_time"] = pd.to_datetime(frame["metric_time"])
    return frame


def load_allocations() -> pd.DataFrame:
    ensure_schema()
    sql = """
SELECT ba.*
FROM slo_budget_allocation ba
JOIN (
  SELECT objective_id, service_id, api_id, MAX(allocated_at) AS latest_time
  FROM slo_budget_allocation
  GROUP BY objective_id, service_id, api_id
) latest
  ON latest.objective_id = ba.objective_id
 AND latest.service_id = ba.service_id
 AND latest.api_id = ba.api_id
 AND latest.latest_time = ba.allocated_at
ORDER BY ba.objective_id ASC, ba.service_id ASC, ba.api_id ASC
"""
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.execute(sql)
            rows = cur.fetchall()
    frame = pd.DataFrame(rows)
    if not frame.empty:
        frame["allocated_at"] = pd.to_datetime(frame["allocated_at"])
    return frame


def load_dependencies() -> pd.DataFrame:
    ensure_schema()
    sql = """
SELECT *
FROM slo_service_dependency
ORDER BY objective_id ASC, path_order ASC, service_id ASC, api_id ASC
"""
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.execute(sql)
            rows = cur.fetchall()
    return pd.DataFrame(rows)


def synth_allocations(metrics: pd.DataFrame) -> pd.DataFrame:
    if metrics.empty:
        return pd.DataFrame()
    grouped = metrics.groupby(["cluster_uuid", "service_id", "service_name", "api_id"], dropna=False)
    aggs = grouped.agg(
        qps=("qps", "mean"),
        qps_std=("qps", "std"),
        p99_latency=("p99_latency", "mean"),
        error_rate=("error_rate", "mean"),
    ).reset_index()
    aggs["qps_std"] = aggs["qps_std"].fillna(0.0)
    total_latency = max(float(aggs["p99_latency"].clip(lower=1).sum()), 1.0)
    rows = []
    for _, row in aggs.iterrows():
        contribution = max(_num(row["p99_latency"], 1.0), 1.0) / total_latency
        service_id = str(row["service_id"])
        critical = 1 if service_id in {"api-gw", "model-inference"} else 0
        qps = _num(row["qps"])
        qps_cv = _num(row["qps_std"]) / max(qps, 1e-9)
        error_rate = _num(row["error_rate"])
        risk_weight = (1 + min(qps_cv, 1.0) * 0.35) * (1 + min(error_rate / 0.1, 3.0) * 0.2 + critical * 0.25)
        rows.append({
            "objective_id": DEFAULT_OBJECTIVE_ID,
            "cluster_uuid": row.get("cluster_uuid", ""),
            "business_flow": DEFAULT_BUSINESS_FLOW,
            "service_id": service_id,
            "service_name": row.get("service_name") or _service_name(service_id),
            "api_id": row.get("api_id", ""),
            "allocation_method": "risk_weighted",
            "latency_contribution": contribution,
            "qps": qps,
            "qps_cv": qps_cv,
            "error_rate": error_rate,
            "critical_path": critical,
            "risk_weight": risk_weight,
            "budget_ratio": contribution,
            "p99_latency_budget": 1000.0 * contribution,
            "error_rate_budget": 0.1 / max(len(aggs), 1),
            "allocated_at": datetime.now(),
        })
    return pd.DataFrame(rows)


def _budget_lookup(allocations: pd.DataFrame) -> Dict[Tuple[str, str], Dict[str, object]]:
    lookup: Dict[Tuple[str, str], Dict[str, object]] = {}
    if allocations.empty:
        return lookup
    for _, row in allocations.iterrows():
        lookup[_service_key(row.get("service_id", ""), row.get("api_id", ""))] = row.to_dict()
    return lookup


def _pick_budget(row: pd.Series, lookup: Dict[Tuple[str, str], Dict[str, object]]) -> Dict[str, object]:
    key = _service_key(row.get("service_id", ""), row.get("api_id", ""))
    if key in lookup:
        return lookup[key]
    for (service_id, _), budget in lookup.items():
        if service_id == key[0]:
            return budget
    return {
        "objective_id": DEFAULT_OBJECTIVE_ID,
        "cluster_uuid": row.get("cluster_uuid", ""),
        "business_flow": DEFAULT_BUSINESS_FLOW,
        "service_id": row.get("service_id", ""),
        "service_name": row.get("service_name", ""),
        "api_id": row.get("api_id", ""),
        "latency_contribution": 0.33,
        "qps_cv": 0.0,
        "critical_path": 0,
        "risk_weight": 1.0,
        "budget_ratio": 0.33,
        "p99_latency_budget": row.get("p99_latency_target", 500.0),
        "error_rate_budget": row.get("error_rate_target", 0.1),
    }


def _resource_features(current: pd.Series, budget: Dict[str, object], horizon: int, future: pd.Series | None = None) -> Dict[str, float]:
    base_qps = max(_num(current.get("qps"), _num(budget.get("qps"), 1.0)), 1.0)
    qps_cv = max(_num(budget.get("qps_cv")), 0.0)
    risk_weight = max(_num(budget.get("risk_weight"), 1.0), 0.2)
    growth = 1 + horizon / 60.0 * 0.08 + min(qps_cv, 1.0) * 0.08 + min(max(risk_weight - 1, 0), 1.5) * 0.05
    qps_forecast = _num(future.get("qps"), base_qps * growth) if future is not None else base_qps * growth
    replicas = max(_int(current.get("replica_count"), 1), 1)
    cpu_request = max(qps_forecast / 700.0, 0.5)
    if _int(budget.get("critical_path"), 0):
        cpu_request *= 1.1
    service_text = f"{budget.get('service_id', '')} {budget.get('service_name', '')}".lower()
    gpu_request = 0.0
    if "model" in service_text or "模型" in str(budget.get("service_name", "")):
        gpu_request = max(1.0, math.ceil(qps_forecast / 900.0))
    elif "data" in service_text or "数据" in str(budget.get("service_name", "")):
        gpu_request = 0.25
    memory_request_gb = 4.0 + cpu_request * 1.25 + gpu_request * 12.0
    if future is not None:
        predicted_cpu = _num(future.get("cpu_util"), _num(current.get("cpu_util")))
        predicted_gpu = _num(future.get("gpu_util"), _num(current.get("gpu_util")))
    else:
        predicted_cpu = min(max(_num(current.get("cpu_util")) * growth + cpu_request * 1.5, 5.0), 98.0)
        predicted_gpu = 0.0
        if gpu_request > 0:
            predicted_gpu = min(max(_num(current.get("gpu_util")) * growth + gpu_request * 2.0, 5.0), 99.0)
    return {
        "qps_forecast": qps_forecast,
        "qps_per_replica": qps_forecast / replicas,
        "replica_count": float(replicas),
        "cpu_request": cpu_request,
        "gpu_request": gpu_request,
        "memory_request_gb": memory_request_gb,
        "predicted_cpu_util": predicted_cpu,
        "predicted_gpu_util": predicted_gpu,
    }


def _feature_record(current: pd.Series, budget: Dict[str, object], horizon: int, future: pd.Series | None = None) -> Dict[str, object]:
    rec = _resource_features(current, budget, horizon, future=future)
    rec.update({
        "cluster_uuid": current.get("cluster_uuid", budget.get("cluster_uuid", "")),
        "objective_id": budget.get("objective_id", DEFAULT_OBJECTIVE_ID),
        "business_flow": budget.get("business_flow", DEFAULT_BUSINESS_FLOW),
        "service_id": current.get("service_id", budget.get("service_id", "")),
        "service_name": current.get("service_name", budget.get("service_name", "")),
        "api_id": current.get("api_id", budget.get("api_id", "")),
        "p95_latency": _num(current.get("p95_latency")),
        "p99_latency": _num(current.get("p99_latency")),
        "error_rate": _num(current.get("error_rate")),
        "p99_latency_budget": max(_num(budget.get("p99_latency_budget"), _num(current.get("p99_latency_target"), 500.0)), 1.0),
        "error_rate_budget": max(_num(budget.get("error_rate_budget"), _num(current.get("error_rate_target"), 0.1)), 0.0001),
        "horizon_minutes": float(horizon),
        "latency_contribution": _num(budget.get("latency_contribution"), 0.33),
        "budget_ratio": _num(budget.get("budget_ratio"), 0.33),
        "critical_path": float(_int(budget.get("critical_path"), 0)),
        "risk_weight": _num(budget.get("risk_weight"), 1.0),
        "request_mix": "critical-path" if _int(budget.get("critical_path"), 0) else "default",
    })
    return rec


def _prepare_X(frame: pd.DataFrame) -> pd.DataFrame:
    out = frame.copy()
    for col in FEATURE_COLUMNS:
        if col not in out.columns:
            out[col] = 0.0
        out[col] = pd.to_numeric(out[col], errors="coerce").fillna(0.0)
    return out[FEATURE_COLUMNS].replace([np.inf, -np.inf], 0.0).fillna(0.0)


def _history_window(group: pd.DataFrame, end_idx: int, seq_len: int) -> pd.DataFrame:
    start_idx = max(0, end_idx - seq_len + 1)
    hist = group.iloc[start_idx:end_idx + 1].copy()
    if hist.empty:
        return hist
    if len(hist) < seq_len:
        padding = pd.concat([hist.iloc[[0]].copy() for _ in range(seq_len - len(hist))], ignore_index=True)
        hist = pd.concat([padding, hist], ignore_index=True)
    return hist.reset_index(drop=True)


def _sequence_features(rows: Iterable[Dict[str, object]]) -> np.ndarray:
    return _prepare_X(pd.DataFrame(rows)).to_numpy(dtype=np.float32)


def _group_columns(metrics: pd.DataFrame) -> List[str]:
    return [col for col in ["cluster_uuid", "service_id", "api_id"] if col in metrics.columns]


def build_sequence_training_data(
    metrics: pd.DataFrame,
    allocations: pd.DataFrame,
    horizons: List[int],
    seq_len: int,
    stride: int,
) -> Dict[str, Any]:
    if metrics.empty:
        return {"X": np.empty((0, seq_len, len(FEATURE_COLUMNS)), dtype=np.float32)}

    seq_len = max(int(seq_len), 1)
    stride = max(int(stride), 1)
    budgets = allocations if not allocations.empty else synth_allocations(metrics)
    lookup = _budget_lookup(budgets)
    group_cols = _group_columns(metrics)
    metrics = metrics.sort_values(group_cols + ["metric_time"]).reset_index(drop=True)

    sequences: List[np.ndarray] = []
    y_reg: List[List[float]] = []
    y_cls: List[float] = []
    sample_times: List[pd.Timestamp] = []

    for _, group in metrics.groupby(group_cols, dropna=False):
        group = group.reset_index(drop=True)
        times = group["metric_time"].to_numpy(dtype="datetime64[ns]")
        for end_idx in range(0, len(group), stride):
            current = group.iloc[end_idx]
            budget = _pick_budget(current, lookup)
            hist = _history_window(group, end_idx, seq_len)
            if hist.empty:
                continue
            for horizon in horizons:
                target_time = np.datetime64(current["metric_time"]) + np.timedelta64(horizon, "m")
                target_idx = int(np.searchsorted(times, target_time, side="left"))
                if target_idx >= len(group):
                    continue
                future = group.iloc[target_idx]
                rec = _feature_record(current, budget, horizon, future=future)
                seq_rows = [_feature_record(hist_row, budget, horizon) for _, hist_row in hist.iterrows()]
                sequences.append(_sequence_features(seq_rows))
                y_reg.append([_num(future.get("p95_latency")), _num(future.get("p99_latency"))])
                y_cls.append(float(
                    _num(future.get("p99_latency")) > rec["p99_latency_budget"]
                    or _num(future.get("error_rate")) > rec["error_rate_budget"]
                ))
                sample_times.append(pd.Timestamp(current["metric_time"]))

    fallback_used = False
    if not sequences:
        fallback_used = True
        for _, group in metrics.groupby(group_cols, dropna=False):
            group = group.reset_index(drop=True)
            for end_idx in range(0, len(group), stride):
                current = group.iloc[end_idx]
                budget = _pick_budget(current, lookup)
                hist = _history_window(group, end_idx, seq_len)
                if hist.empty:
                    continue
                for horizon in horizons:
                    rec = _feature_record(current, budget, horizon)
                    seq_rows = [_feature_record(hist_row, budget, horizon) for _, hist_row in hist.iterrows()]
                    sequences.append(_sequence_features(seq_rows))
                    y_reg.append([_num(current.get("p95_latency")), _num(current.get("p99_latency"))])
                    y_cls.append(float(
                        _num(current.get("p99_latency")) > rec["p99_latency_budget"]
                        or _num(current.get("error_rate")) > rec["error_rate_budget"]
                    ))
                    sample_times.append(pd.Timestamp(current["metric_time"]))

    if not sequences:
        return {"X": np.empty((0, seq_len, len(FEATURE_COLUMNS)), dtype=np.float32)}

    return {
        "X": np.stack(sequences).astype(np.float32),
        "y_reg": np.asarray(y_reg, dtype=np.float32),
        "y_cls": np.asarray(y_cls, dtype=np.float32),
        "sample_times": pd.to_datetime(sample_times).to_numpy(dtype="datetime64[ns]"),
        "fallback_used": fallback_used,
    }


def _fit_sequence_scaler(X: np.ndarray) -> Dict[str, List[float]]:
    flat = X.reshape(-1, X.shape[-1])
    mean = flat.mean(axis=0)
    std = flat.std(axis=0)
    std = np.where(std < 1e-6, 1.0, std)
    return {"mean": mean.astype(float).tolist(), "std": std.astype(float).tolist()}


def _apply_sequence_scaler(X: np.ndarray, scaler: Dict[str, List[float]]) -> np.ndarray:
    mean = np.asarray(scaler["mean"], dtype=np.float32).reshape(1, 1, -1)
    std = np.asarray(scaler["std"], dtype=np.float32).reshape(1, 1, -1)
    return ((X.astype(np.float32) - mean) / std).astype(np.float32)


def _fit_target_scaler(y: np.ndarray) -> Dict[str, List[float]]:
    mean = y.mean(axis=0)
    std = y.std(axis=0)
    std = np.where(std < 1e-6, 1.0, std)
    return {"mean": mean.astype(float).tolist(), "std": std.astype(float).tolist()}


def _apply_target_scaler(y: np.ndarray, scaler: Dict[str, List[float]]) -> np.ndarray:
    mean = np.asarray(scaler["mean"], dtype=np.float32).reshape(1, -1)
    std = np.asarray(scaler["std"], dtype=np.float32).reshape(1, -1)
    return ((y.astype(np.float32) - mean) / std).astype(np.float32)


def _inverse_target_scaler(y: np.ndarray, scaler: Dict[str, List[float]]) -> np.ndarray:
    mean = np.asarray(scaler["mean"], dtype=np.float32).reshape(1, -1)
    std = np.asarray(scaler["std"], dtype=np.float32).reshape(1, -1)
    return (y.astype(np.float32) * std + mean).astype(np.float32)


def _chronological_splits(sample_times: np.ndarray) -> Tuple[np.ndarray, np.ndarray, np.ndarray]:
    n = len(sample_times)
    order = np.argsort(sample_times)
    if n < 3:
        return order, order, order
    train_end = max(1, int(n * 0.7))
    valid_end = max(train_end + 1, int(n * 0.85))
    if valid_end >= n:
        valid_end = max(train_end, n - 1)
    train_idx = order[:train_end]
    valid_idx = order[train_end:valid_end]
    test_idx = order[valid_end:]
    if len(valid_idx) == 0:
        valid_idx = train_idx[-1:]
    if len(test_idx) == 0:
        test_idx = valid_idx
    return train_idx, valid_idx, test_idx


def _sigmoid_array(values: np.ndarray) -> np.ndarray:
    return 1.0 / (1.0 + np.exp(-np.clip(values, -50.0, 50.0)))


def _predict_sequence_array(model: Any, X: np.ndarray, batch_size: int, device: Any, torch: Any) -> np.ndarray:
    if len(X) == 0:
        return np.empty((0, 3), dtype=np.float32)
    model.eval()
    outputs = []
    with torch.no_grad():
        for start in range(0, len(X), batch_size):
            batch = torch.from_numpy(X[start:start + batch_size]).to(device)
            outputs.append(model(batch).detach().cpu().numpy())
    return np.concatenate(outputs, axis=0).astype(np.float32)


def _sequence_eval_metrics(
    y_reg: np.ndarray,
    y_cls: np.ndarray,
    pred_reg: np.ndarray,
    pred_prob: np.ndarray,
) -> Dict[str, float]:
    pred_reg = pred_reg.copy()
    pred_reg[:, 0] = np.clip(pred_reg[:, 0], 0.0, None)
    pred_reg[:, 1] = np.maximum(np.clip(pred_reg[:, 1], 0.0, None), pred_reg[:, 0] * 1.05)
    error = pred_reg - y_reg
    cls_pred = (pred_prob >= 0.5).astype(np.float32)
    return {
        "p95_mae": float(np.mean(np.abs(error[:, 0]))),
        "p99_mae": float(np.mean(np.abs(error[:, 1]))),
        "p95_rmse": float(np.sqrt(np.mean(np.square(error[:, 0])))),
        "p99_rmse": float(np.sqrt(np.mean(np.square(error[:, 1])))),
        "violation_accuracy": float(np.mean(cls_pred == y_cls.astype(np.float32))),
        "violation_rate": float(np.mean(y_cls.astype(np.float32))),
    }


def _masked_gnn_eval_metrics(
    y_reg: np.ndarray,
    y_cls: np.ndarray,
    mask: np.ndarray,
    pred_reg: np.ndarray,
    pred_prob: np.ndarray,
) -> Dict[str, float]:
    active = mask.astype(bool)
    if not np.any(active):
        return {
            "p95_mae": 0.0,
            "p99_mae": 0.0,
            "p95_rmse": 0.0,
            "p99_rmse": 0.0,
            "violation_accuracy": 0.0,
            "violation_rate": 0.0,
        }
    true_reg = y_reg[active]
    true_cls = y_cls[active].astype(np.float32)
    pred_reg = pred_reg.copy()
    pred_reg[:, :, 0] = np.clip(pred_reg[:, :, 0], 0.0, None)
    pred_reg[:, :, 1] = np.maximum(np.clip(pred_reg[:, :, 1], 0.0, None), pred_reg[:, :, 0] * 1.05)
    selected_reg = pred_reg[active]
    selected_prob = pred_prob[active]
    error = selected_reg - true_reg
    cls_pred = (selected_prob >= 0.5).astype(np.float32)
    return {
        "p95_mae": float(np.mean(np.abs(error[:, 0]))),
        "p99_mae": float(np.mean(np.abs(error[:, 1]))),
        "p95_rmse": float(np.sqrt(np.mean(np.square(error[:, 0])))),
        "p99_rmse": float(np.sqrt(np.mean(np.square(error[:, 1])))),
        "violation_accuracy": float(np.mean(cls_pred == true_cls)),
        "violation_rate": float(np.mean(true_cls)),
    }


def _graph_node_key(row: pd.Series | Dict[str, object]) -> Tuple[str, str, str]:
    return (
        str(row.get("cluster_uuid", "") or ""),
        str(row.get("service_id", "") or ""),
        str(row.get("api_id", "") or ""),
    )


def _encode_node_key(key: Tuple[str, str, str]) -> Dict[str, str]:
    return {"cluster_uuid": key[0], "service_id": key[1], "api_id": key[2]}


def _decode_node_keys(raw_keys: Iterable[Dict[str, str] | Iterable[str]]) -> List[Tuple[str, str, str]]:
    keys = []
    for raw in raw_keys:
        if isinstance(raw, dict):
            keys.append((str(raw.get("cluster_uuid", "")), str(raw.get("service_id", "")), str(raw.get("api_id", ""))))
        else:
            values = list(raw)
            keys.append((str(values[0]), str(values[1]), str(values[2])))
    return keys


def _build_graph_nodes(metrics: pd.DataFrame) -> Tuple[List[Tuple[str, str, str]], Dict[Tuple[str, str, str], Dict[str, object]]]:
    if metrics.empty:
        return [], {}
    cols = [col for col in ["cluster_uuid", "service_id", "service_name", "api_id"] if col in metrics.columns]
    unique_nodes = metrics[cols].drop_duplicates().sort_values([col for col in ["cluster_uuid", "service_id", "api_id"] if col in cols])
    node_keys: List[Tuple[str, str, str]] = []
    metadata: Dict[Tuple[str, str, str], Dict[str, object]] = {}
    for _, row in unique_nodes.iterrows():
        key = _graph_node_key(row)
        node_keys.append(key)
        metadata[key] = {
            "cluster_uuid": key[0],
            "service_id": key[1],
            "service_name": row.get("service_name", _service_name(key[1])),
            "api_id": key[2],
        }
    return node_keys, metadata


def _build_node_groups(metrics: pd.DataFrame) -> Dict[Tuple[str, str, str], pd.DataFrame]:
    groups: Dict[Tuple[str, str, str], pd.DataFrame] = {}
    group_cols = _group_columns(metrics)
    for _, group in metrics.groupby(group_cols, dropna=False):
        group = group.sort_values("metric_time").reset_index(drop=True)
        groups[_graph_node_key(group.iloc[0])] = group
    return groups


def _build_graph_adjacency(
    node_keys: List[Tuple[str, str, str]],
    dependencies: pd.DataFrame,
) -> Tuple[np.ndarray, int]:
    node_count = len(node_keys)
    adjacency = np.eye(node_count, dtype=np.float32)
    if node_count == 0:
        return adjacency, 0

    service_index: Dict[Tuple[str, str], List[int]] = {}
    for idx, key in enumerate(node_keys):
        service_index.setdefault((key[0], key[1]), []).append(idx)

    edge_pairs = set()
    if not dependencies.empty:
        clusters = sorted({key[0] for key in node_keys})
        for _, dep in dependencies.iterrows():
            service_id = str(dep.get("service_id", "") or "")
            neighbor_services = [
                str(dep.get("upstream_service_id", "") or ""),
                str(dep.get("downstream_service_id", "") or ""),
            ]
            for cluster_uuid in clusters:
                src_indices = service_index.get((cluster_uuid, service_id), [])
                for neighbor_service in neighbor_services:
                    if not neighbor_service:
                        continue
                    dst_indices = service_index.get((cluster_uuid, neighbor_service), [])
                    for src in src_indices:
                        for dst in dst_indices:
                            if src == dst:
                                continue
                            adjacency[src, dst] = 1.0
                            adjacency[dst, src] = 1.0
                            edge_pairs.add(tuple(sorted((src, dst))))

    degree = adjacency.sum(axis=1)
    degree = np.where(degree <= 0, 1.0, degree)
    norm = adjacency / np.sqrt(np.outer(degree, degree))
    return norm.astype(np.float32), len(edge_pairs)


def _metric_position_at_or_before(group: pd.DataFrame, metric_time: pd.Timestamp) -> int:
    times = group["metric_time"].to_numpy(dtype="datetime64[ns]")
    pos = int(np.searchsorted(times, np.datetime64(metric_time), side="right")) - 1
    if pos < 0:
        return 0
    if pos >= len(group):
        return len(group) - 1
    return pos


def _fallback_metric_row(key: Tuple[str, str, str]) -> pd.Series:
    return pd.Series({
        "cluster_uuid": key[0],
        "service_id": key[1],
        "service_name": _service_name(key[1]),
        "api_id": key[2],
        "qps": 1.0,
        "p95_latency": 0.0,
        "p99_latency": 0.0,
        "error_rate": 0.0,
        "replica_count": 1,
        "cpu_util": 0.0,
        "gpu_util": 0.0,
        "p99_latency_target": 500.0,
        "error_rate_target": 0.1,
    })


def _current_row_for_node(
    node_groups: Dict[Tuple[str, str, str], pd.DataFrame],
    key: Tuple[str, str, str],
    metric_time: pd.Timestamp,
) -> pd.Series:
    group = node_groups.get(key)
    if group is None or group.empty:
        return _fallback_metric_row(key)
    pos = _metric_position_at_or_before(group, metric_time)
    return group.iloc[pos]


def _graph_snapshot_features(
    node_groups: Dict[Tuple[str, str, str], pd.DataFrame],
    node_keys: List[Tuple[str, str, str]],
    lookup: Dict[Tuple[str, str], Dict[str, object]],
    metric_time: pd.Timestamp,
    horizon: int,
    seq_len: int,
) -> np.ndarray:
    node_features: List[np.ndarray] = []
    for key in node_keys:
        group = node_groups.get(key)
        if group is None or group.empty:
            row = _fallback_metric_row(key)
            budget = _pick_budget(row, lookup)
            seq_rows = [_feature_record(row, budget, horizon) for _ in range(seq_len)]
        else:
            pos = _metric_position_at_or_before(group, metric_time)
            row = group.iloc[pos]
            budget = _pick_budget(row, lookup)
            hist = _history_window(group, pos, seq_len)
            seq_rows = [_feature_record(hist_row, budget, horizon) for _, hist_row in hist.iterrows()]
        node_features.append(_sequence_features(seq_rows))
    return np.stack(node_features).astype(np.float32)


def _sequence_windows(feature_matrix: np.ndarray, seq_len: int) -> np.ndarray:
    if len(feature_matrix) == 0:
        return np.empty((0, seq_len, len(FEATURE_COLUMNS)), dtype=np.float32)
    if seq_len <= 1:
        return feature_matrix[:, None, :].astype(np.float32)
    padding = np.repeat(feature_matrix[:1], seq_len - 1, axis=0)
    padded = np.concatenate([padding, feature_matrix], axis=0)
    return np.stack([padded[idx:idx + seq_len] for idx in range(len(feature_matrix))]).astype(np.float32)


def _precompute_graph_sequence_features(
    node_groups: Dict[Tuple[str, str, str], pd.DataFrame],
    node_keys: List[Tuple[str, str, str]],
    lookup: Dict[Tuple[str, str], Dict[str, object]],
    horizons: List[int],
    seq_len: int,
) -> Tuple[Dict[Tuple[Tuple[str, str, str], int], np.ndarray], Dict[Tuple[str, str, str], np.ndarray]]:
    feature_sequences: Dict[Tuple[Tuple[str, str, str], int], np.ndarray] = {}
    group_times: Dict[Tuple[str, str, str], np.ndarray] = {}
    for key in node_keys:
        group = node_groups.get(key)
        if group is None or group.empty:
            group = pd.DataFrame([_fallback_metric_row(key)])
            group["metric_time"] = pd.to_datetime([datetime.now()])
        group = group.sort_values("metric_time").reset_index(drop=True)
        group_times[key] = group["metric_time"].to_numpy(dtype="datetime64[ns]")
        for horizon in horizons:
            rows = []
            for _, row in group.iterrows():
                budget = _pick_budget(row, lookup)
                rows.append(_feature_record(row, budget, horizon))
            feature_matrix = _sequence_features(rows)
            feature_sequences[(key, horizon)] = _sequence_windows(feature_matrix, seq_len)
    return feature_sequences, group_times


def _position_in_times(times: np.ndarray, metric_time: pd.Timestamp) -> int:
    pos = int(np.searchsorted(times, np.datetime64(metric_time), side="right")) - 1
    if pos < 0:
        return 0
    if pos >= len(times):
        return len(times) - 1
    return pos


def _graph_snapshot_features_from_cache(
    feature_sequences: Dict[Tuple[Tuple[str, str, str], int], np.ndarray],
    group_times: Dict[Tuple[str, str, str], np.ndarray],
    node_keys: List[Tuple[str, str, str]],
    metric_time: pd.Timestamp,
    horizon: int,
) -> np.ndarray:
    node_features: List[np.ndarray] = []
    for key in node_keys:
        seqs = feature_sequences[(key, horizon)]
        pos = _position_in_times(group_times[key], metric_time)
        node_features.append(seqs[pos])
    return np.stack(node_features).astype(np.float32)


def build_gnn_training_data(
    metrics: pd.DataFrame,
    allocations: pd.DataFrame,
    dependencies: pd.DataFrame,
    horizons: List[int],
    seq_len: int,
    stride: int,
) -> Dict[str, Any]:
    if metrics.empty:
        return {"X": np.empty((0, 0, seq_len, len(FEATURE_COLUMNS)), dtype=np.float32)}

    seq_len = max(int(seq_len), 1)
    stride = max(int(stride), 1)
    budgets = allocations if not allocations.empty else synth_allocations(metrics)
    lookup = _budget_lookup(budgets)
    group_cols = _group_columns(metrics)
    metrics = metrics.sort_values(group_cols + ["metric_time"]).reset_index(drop=True)
    node_keys, node_metadata = _build_graph_nodes(metrics)
    node_index = {key: idx for idx, key in enumerate(node_keys)}
    node_groups = _build_node_groups(metrics)
    adjacency, dependency_edges = _build_graph_adjacency(node_keys, dependencies)
    node_count = len(node_keys)
    _log_progress(f"GNN train: precomputing sequence tensors for node_count={node_count}")
    feature_sequences, group_times = _precompute_graph_sequence_features(node_groups, node_keys, lookup, horizons, seq_len)

    graph_samples: List[np.ndarray] = []
    y_reg: List[np.ndarray] = []
    y_cls: List[np.ndarray] = []
    y_mask: List[np.ndarray] = []
    sample_times: List[pd.Timestamp] = []

    unique_times = pd.to_datetime(metrics["metric_time"].drop_duplicates().sort_values()).tolist()[::stride]
    for metric_time in unique_times:
        metric_time = pd.Timestamp(metric_time)
        for horizon in horizons:
            reg_targets = np.zeros((node_count, 2), dtype=np.float32)
            cls_targets = np.zeros((node_count,), dtype=np.float32)
            mask = np.zeros((node_count,), dtype=np.float32)
            target_time = np.datetime64(metric_time) + np.timedelta64(horizon, "m")
            for key, group in node_groups.items():
                node_idx = node_index.get(key)
                if node_idx is None:
                    continue
                pos = _metric_position_at_or_before(group, metric_time)
                current = group.iloc[pos]
                if pd.Timestamp(current["metric_time"]) != metric_time:
                    continue
                times = group["metric_time"].to_numpy(dtype="datetime64[ns]")
                future_idx = int(np.searchsorted(times, target_time, side="left"))
                if future_idx >= len(group):
                    continue
                future = group.iloc[future_idx]
                budget = _pick_budget(current, lookup)
                rec = _feature_record(current, budget, horizon, future=future)
                reg_targets[node_idx] = [_num(future.get("p95_latency")), _num(future.get("p99_latency"))]
                cls_targets[node_idx] = float(
                    _num(future.get("p99_latency")) > rec["p99_latency_budget"]
                    or _num(future.get("error_rate")) > rec["error_rate_budget"]
                )
                mask[node_idx] = 1.0
            if mask.sum() <= 0:
                continue
            graph_samples.append(_graph_snapshot_features_from_cache(feature_sequences, group_times, node_keys, metric_time, horizon))
            y_reg.append(reg_targets)
            y_cls.append(cls_targets)
            y_mask.append(mask)
            sample_times.append(metric_time)

    fallback_used = False
    if not graph_samples:
        fallback_used = True
        for metric_time in unique_times:
            metric_time = pd.Timestamp(metric_time)
            for horizon in horizons:
                reg_targets = np.zeros((node_count, 2), dtype=np.float32)
                cls_targets = np.zeros((node_count,), dtype=np.float32)
                mask = np.zeros((node_count,), dtype=np.float32)
                for key, group in node_groups.items():
                    node_idx = node_index.get(key)
                    if node_idx is None:
                        continue
                    pos = _metric_position_at_or_before(group, metric_time)
                    current = group.iloc[pos]
                    budget = _pick_budget(current, lookup)
                    rec = _feature_record(current, budget, horizon)
                    reg_targets[node_idx] = [_num(current.get("p95_latency")), _num(current.get("p99_latency"))]
                    cls_targets[node_idx] = float(
                        _num(current.get("p99_latency")) > rec["p99_latency_budget"]
                        or _num(current.get("error_rate")) > rec["error_rate_budget"]
                    )
                    mask[node_idx] = 1.0
                if mask.sum() <= 0:
                    continue
                graph_samples.append(_graph_snapshot_features_from_cache(feature_sequences, group_times, node_keys, metric_time, horizon))
                y_reg.append(reg_targets)
                y_cls.append(cls_targets)
                y_mask.append(mask)
                sample_times.append(metric_time)

    if not graph_samples:
        return {"X": np.empty((0, len(node_keys), seq_len, len(FEATURE_COLUMNS)), dtype=np.float32)}

    return {
        "X": np.stack(graph_samples).astype(np.float32),
        "y_reg": np.stack(y_reg).astype(np.float32),
        "y_cls": np.stack(y_cls).astype(np.float32),
        "y_mask": np.stack(y_mask).astype(np.float32),
        "sample_times": pd.to_datetime(sample_times).to_numpy(dtype="datetime64[ns]"),
        "node_keys": node_keys,
        "node_metadata": node_metadata,
        "adjacency": adjacency,
        "dependency_edges": int(dependency_edges),
        "fallback_used": fallback_used,
    }


def _predict_gnn_array(model: Any, X: np.ndarray, adjacency: Any, batch_size: int, device: Any, torch: Any) -> np.ndarray:
    if len(X) == 0:
        return np.empty((0, 0, 3), dtype=np.float32)
    model.eval()
    outputs = []
    with torch.no_grad():
        for start in range(0, len(X), batch_size):
            batch = torch.from_numpy(X[start:start + batch_size]).to(device)
            outputs.append(model(batch, adjacency).detach().cpu().numpy())
    return np.concatenate(outputs, axis=0).astype(np.float32)


def train_gnn(
    start_time: str | None,
    end_time: str | None,
    horizons: str,
    seq_len: int,
    stride: int,
    epochs: int,
    batch_size: int,
    learning_rate: float,
    hidden_dim: int,
    num_layers: int,
    graph_layers: int,
    dropout: float,
    violation_loss_weight: float,
    device: str,
) -> Dict[str, object]:
    torch, nn, DataLoader, TensorDataset = _import_torch()
    device_obj = _resolve_device(torch, device)
    horizon_values = _parse_horizons(horizons)
    _log_progress(f"GNN train: loading metrics from DB, device={device_obj}")
    metrics = load_metrics(start_time, end_time)
    if metrics.empty:
        raise RuntimeError("No rows found in slo_metric_ts")
    _log_progress(f"GNN train: loaded metric_rows={len(metrics)}")
    allocations = load_allocations()
    dependencies = load_dependencies()
    _log_progress(
        f"GNN train: building graph samples, horizons={horizon_values}, seq_len={seq_len}, "
        f"allocations={len(allocations)}, dependencies={len(dependencies)}"
    )
    graph_data = build_gnn_training_data(metrics, allocations, dependencies, horizon_values, seq_len=seq_len, stride=stride)
    X = graph_data.get("X")
    if X is None or len(X) == 0:
        raise RuntimeError("No graph training samples could be built for SLO proxy model")

    y_reg = graph_data["y_reg"]
    y_cls = graph_data["y_cls"]
    y_mask = graph_data["y_mask"]
    sample_times = graph_data["sample_times"]
    _log_progress(
        f"GNN train: built graph_samples={len(X)}, node_count={X.shape[1]}, "
        f"active_targets={int(y_mask.sum())}, dependency_edges={graph_data.get('dependency_edges', 0)}"
    )
    train_idx, valid_idx, test_idx = _chronological_splits(sample_times)
    feature_scaler = _fit_sequence_scaler(X[train_idx])
    train_target_mask = y_mask[train_idx].astype(bool)
    if not np.any(train_target_mask):
        raise RuntimeError("No active train targets could be built for GNN model")
    target_scaler = _fit_target_scaler(y_reg[train_idx][train_target_mask])
    X_scaled = _apply_sequence_scaler(X, feature_scaler)
    y_reg_scaled = _apply_target_scaler(y_reg, target_scaler)

    torch.manual_seed(47)
    if torch.cuda.is_available():
        torch.cuda.manual_seed_all(47)

    model = _build_gnn_model(
        input_dim=len(FEATURE_COLUMNS),
        hidden_dim=max(int(hidden_dim), 1),
        num_layers=max(int(num_layers), 1),
        dropout=max(float(dropout), 0.0),
        graph_layers=max(int(graph_layers), 1),
    ).to(device_obj)
    adjacency = torch.from_numpy(graph_data["adjacency"]).to(device_obj)

    dataset = TensorDataset(
        torch.from_numpy(X_scaled[train_idx]),
        torch.from_numpy(y_reg_scaled[train_idx]),
        torch.from_numpy(y_cls[train_idx].astype(np.float32)),
        torch.from_numpy(y_mask[train_idx].astype(np.float32)),
    )
    loader = DataLoader(dataset, batch_size=max(int(batch_size), 1), shuffle=True)
    optimizer = torch.optim.AdamW(model.parameters(), lr=learning_rate, weight_decay=1e-4)
    reg_loss_fn = nn.SmoothL1Loss(reduction="none")
    cls_loss_fn = nn.BCEWithLogitsLoss(reduction="none")
    loss_history: List[float] = []

    _log_progress(f"GNN train: starting epochs={max(int(epochs), 1)}, batch_size={max(int(batch_size), 1)}")
    model.train()
    for epoch_idx in range(max(int(epochs), 1)):
        epoch_loss = 0.0
        seen = 0
        for batch_x, batch_y_reg, batch_y_cls, batch_mask in loader:
            batch_x = batch_x.to(device_obj)
            batch_y_reg = batch_y_reg.to(device_obj)
            batch_y_cls = batch_y_cls.to(device_obj)
            batch_mask = batch_mask.to(device_obj)
            optimizer.zero_grad()
            out = model(batch_x, adjacency)
            active = batch_mask.sum().clamp_min(1.0)
            reg_loss = (reg_loss_fn(out[:, :, :2], batch_y_reg).mean(dim=2) * batch_mask).sum() / active
            cls_loss = (cls_loss_fn(out[:, :, 2], batch_y_cls) * batch_mask).sum() / active
            loss = reg_loss + float(violation_loss_weight) * cls_loss
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 5.0)
            optimizer.step()
            active_count = float(batch_mask.sum().detach().cpu())
            epoch_loss += float(loss.detach().cpu()) * active_count
            seen += int(active_count)
        loss_history.append(epoch_loss / max(seen, 1))
        _log_progress(f"GNN train: epoch {epoch_idx + 1}/{max(int(epochs), 1)} loss={loss_history[-1]:.6f}")

    def evaluate(indices: np.ndarray) -> Dict[str, float]:
        out = _predict_gnn_array(model, X_scaled[indices], adjacency, batch_size, device_obj, torch)
        pred_reg = _inverse_target_scaler(out[:, :, :2], target_scaler)
        pred_prob = _sigmoid_array(out[:, :, 2])
        return _masked_gnn_eval_metrics(y_reg[indices], y_cls[indices], y_mask[indices], pred_reg, pred_prob)

    _log_progress("GNN train: evaluating splits")
    train_metrics = evaluate(train_idx)
    valid_metrics = evaluate(valid_idx)
    test_metrics = evaluate(test_idx)

    model_path = _sequence_model_path(GNN_MODEL_TYPE)
    checkpoint = {
        "model_type": GNN_MODEL_TYPE,
        "model_state": model.state_dict(),
        "input_dim": len(FEATURE_COLUMNS),
        "feature_columns": FEATURE_COLUMNS,
        "horizons": horizon_values,
        "seq_len": int(seq_len),
        "hidden_dim": int(hidden_dim),
        "num_layers": int(num_layers),
        "graph_layers": int(graph_layers),
        "dropout": float(dropout),
        "feature_scaler": feature_scaler,
        "target_scaler": target_scaler,
        "node_keys": [_encode_node_key(key) for key in graph_data["node_keys"]],
        "adjacency": graph_data["adjacency"].astype(float).tolist(),
    }
    torch.save(checkpoint, model_path)

    metadata = {
        "model_name": "slo_proxy_gnn",
        "model_version": "v0.3_gnn_predictor",
        "created_at": datetime.now().isoformat(),
        "feature_columns": FEATURE_COLUMNS,
        "horizons": horizon_values,
        "seq_len": int(seq_len),
        "stride": int(stride),
        "epochs": int(epochs),
        "batch_size": int(batch_size),
        "learning_rate": float(learning_rate),
        "hidden_dim": int(hidden_dim),
        "num_layers": int(num_layers),
        "graph_layers": int(graph_layers),
        "dropout": float(dropout),
        "device": str(device_obj),
        "metric_rows": int(len(metrics)),
        "allocation_rows": int(len(allocations)),
        "dependency_rows": int(len(dependencies)),
        "dependency_edges": int(graph_data.get("dependency_edges", 0)),
        "node_count": int(len(graph_data["node_keys"])),
        "train_rows": int(len(X)),
        "active_targets": int(y_mask.sum()),
        "split_rows": {
            "train": int(len(train_idx)),
            "valid": int(len(valid_idx)),
            "test": int(len(test_idx)),
        },
        "fallback_used": bool(graph_data.get("fallback_used", False)),
        "loss_history": loss_history[-10:],
        "metrics": {
            "train": train_metrics,
            "valid": valid_metrics,
            "test": test_metrics,
        },
        "model_file": str(model_path),
    }
    _sequence_metadata_path(GNN_MODEL_TYPE).write_text(json.dumps(metadata, indent=2, ensure_ascii=False))
    _log_progress(f"GNN train: saved checkpoint={model_path}")
    return metadata


def train_sequence(
    model_type: str,
    start_time: str | None,
    end_time: str | None,
    horizons: str,
    seq_len: int,
    stride: int,
    epochs: int,
    batch_size: int,
    learning_rate: float,
    hidden_dim: int,
    num_layers: int,
    dropout: float,
    kernel_size: int,
    violation_loss_weight: float,
    device: str,
) -> Dict[str, object]:
    model_type = model_type.lower()
    if model_type not in SEQUENCE_MODEL_TYPES:
        raise RuntimeError(f"Unsupported sequence model type: {model_type}")

    torch, nn, DataLoader, TensorDataset = _import_torch()
    device_obj = _resolve_device(torch, device)
    horizon_values = _parse_horizons(horizons)
    metrics = load_metrics(start_time, end_time)
    if metrics.empty:
        raise RuntimeError("No rows found in slo_metric_ts")
    allocations = load_allocations()
    seq_data = build_sequence_training_data(metrics, allocations, horizon_values, seq_len=seq_len, stride=stride)
    X = seq_data.get("X")
    if X is None or len(X) == 0:
        raise RuntimeError("No sequence training samples could be built for SLO proxy model")

    y_reg = seq_data["y_reg"]
    y_cls = seq_data["y_cls"]
    sample_times = seq_data["sample_times"]
    train_idx, valid_idx, test_idx = _chronological_splits(sample_times)

    feature_scaler = _fit_sequence_scaler(X[train_idx])
    target_scaler = _fit_target_scaler(y_reg[train_idx])
    X_scaled = _apply_sequence_scaler(X, feature_scaler)
    y_reg_scaled = _apply_target_scaler(y_reg, target_scaler)

    torch.manual_seed(43)
    if torch.cuda.is_available():
        torch.cuda.manual_seed_all(43)

    model = _build_sequence_model(
        model_type=model_type,
        input_dim=len(FEATURE_COLUMNS),
        hidden_dim=max(int(hidden_dim), 1),
        num_layers=max(int(num_layers), 1),
        dropout=max(float(dropout), 0.0),
        kernel_size=max(int(kernel_size), 2),
    ).to(device_obj)

    dataset = TensorDataset(
        torch.from_numpy(X_scaled[train_idx]),
        torch.from_numpy(y_reg_scaled[train_idx]),
        torch.from_numpy(y_cls[train_idx].astype(np.float32)),
    )
    loader = DataLoader(dataset, batch_size=max(int(batch_size), 1), shuffle=True)
    optimizer = torch.optim.AdamW(model.parameters(), lr=learning_rate, weight_decay=1e-4)
    reg_loss_fn = nn.SmoothL1Loss()
    cls_loss_fn = nn.BCEWithLogitsLoss()
    loss_history: List[float] = []

    model.train()
    for _ in range(max(int(epochs), 1)):
        epoch_loss = 0.0
        seen = 0
        for batch_x, batch_y_reg, batch_y_cls in loader:
            batch_x = batch_x.to(device_obj)
            batch_y_reg = batch_y_reg.to(device_obj)
            batch_y_cls = batch_y_cls.to(device_obj)
            optimizer.zero_grad()
            out = model(batch_x)
            reg_loss = reg_loss_fn(out[:, :2], batch_y_reg)
            cls_loss = cls_loss_fn(out[:, 2], batch_y_cls)
            loss = reg_loss + float(violation_loss_weight) * cls_loss
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 5.0)
            optimizer.step()
            epoch_loss += float(loss.detach().cpu()) * len(batch_x)
            seen += len(batch_x)
        loss_history.append(epoch_loss / max(seen, 1))

    def evaluate(indices: np.ndarray) -> Dict[str, float]:
        out = _predict_sequence_array(model, X_scaled[indices], batch_size, device_obj, torch)
        pred_reg = _inverse_target_scaler(out[:, :2], target_scaler)
        pred_prob = _sigmoid_array(out[:, 2])
        return _sequence_eval_metrics(y_reg[indices], y_cls[indices], pred_reg, pred_prob)

    train_metrics = evaluate(train_idx)
    valid_metrics = evaluate(valid_idx)
    test_metrics = evaluate(test_idx)

    checkpoint = {
        "model_type": model_type,
        "model_state": model.state_dict(),
        "input_dim": len(FEATURE_COLUMNS),
        "feature_columns": FEATURE_COLUMNS,
        "horizons": horizon_values,
        "seq_len": int(seq_len),
        "hidden_dim": int(hidden_dim),
        "num_layers": int(num_layers),
        "dropout": float(dropout),
        "kernel_size": int(kernel_size),
        "feature_scaler": feature_scaler,
        "target_scaler": target_scaler,
    }
    model_path = _sequence_model_path(model_type)
    torch.save(checkpoint, model_path)

    metadata = {
        "model_name": f"slo_proxy_sequence_{model_type}",
        "model_version": f"v0.3_{model_type}_sequence",
        "created_at": datetime.now().isoformat(),
        "feature_columns": FEATURE_COLUMNS,
        "horizons": horizon_values,
        "seq_len": int(seq_len),
        "stride": int(stride),
        "epochs": int(epochs),
        "batch_size": int(batch_size),
        "learning_rate": float(learning_rate),
        "hidden_dim": int(hidden_dim),
        "num_layers": int(num_layers),
        "dropout": float(dropout),
        "kernel_size": int(kernel_size),
        "device": str(device_obj),
        "metric_rows": int(len(metrics)),
        "allocation_rows": int(len(allocations)),
        "train_rows": int(len(X)),
        "split_rows": {
            "train": int(len(train_idx)),
            "valid": int(len(valid_idx)),
            "test": int(len(test_idx)),
        },
        "fallback_used": bool(seq_data.get("fallback_used", False)),
        "loss_history": loss_history[-10:],
        "metrics": {
            "train": train_metrics,
            "valid": valid_metrics,
            "test": test_metrics,
        },
        "model_file": str(model_path),
    }
    _sequence_metadata_path(model_type).write_text(json.dumps(metadata, indent=2, ensure_ascii=False))
    return metadata


def build_training_frame(metrics: pd.DataFrame, allocations: pd.DataFrame, horizons: List[int]) -> pd.DataFrame:
    if metrics.empty:
        return pd.DataFrame()
    budgets = allocations if not allocations.empty else synth_allocations(metrics)
    lookup = _budget_lookup(budgets)
    records: List[Dict[str, object]] = []
    metrics = metrics.sort_values(["service_id", "api_id", "metric_time"])
    for _, group in metrics.groupby(["service_id", "api_id"], dropna=False):
        group = group.reset_index(drop=True)
        times = group["metric_time"].to_numpy(dtype="datetime64[ns]")
        for idx, row in group.iterrows():
            budget = _pick_budget(row, lookup)
            for horizon in horizons:
                target_time = np.datetime64(row["metric_time"]) + np.timedelta64(horizon, "m")
                target_idx = int(np.searchsorted(times, target_time, side="left"))
                if target_idx >= len(group):
                    continue
                future = group.iloc[target_idx]
                rec = _feature_record(row, budget, horizon, future=future)
                rec["target_p95_latency"] = _num(future.get("p95_latency"))
                rec["target_p99_latency"] = _num(future.get("p99_latency"))
                rec["target_violation"] = int(
                    _num(future.get("p99_latency")) > rec["p99_latency_budget"]
                    or _num(future.get("error_rate")) > rec["error_rate_budget"]
                )
                records.append(rec)
    if records:
        return pd.DataFrame(records)

    # Small demo datasets may not have enough future horizon. Use current samples
    # as a weak baseline so the command still produces a usable model artifact.
    fallback = []
    for _, row in metrics.iterrows():
        budget = _pick_budget(row, lookup)
        for horizon in horizons:
            rec = _feature_record(row, budget, horizon)
            rec["target_p95_latency"] = _num(row.get("p95_latency"))
            rec["target_p99_latency"] = _num(row.get("p99_latency"))
            rec["target_violation"] = int(
                _num(row.get("p99_latency")) > rec["p99_latency_budget"]
                or _num(row.get("error_rate")) > rec["error_rate_budget"]
            )
            fallback.append(rec)
    return pd.DataFrame(fallback)


def train(start_time: str | None, end_time: str | None, horizons: str) -> Dict[str, object]:
    try:
        from lightgbm import LGBMClassifier, LGBMRegressor
    except ModuleNotFoundError as exc:
        raise RuntimeError("lightgbm is required: python -m pip install lightgbm") from exc

    horizon_values = _parse_horizons(horizons)
    metrics = load_metrics(start_time, end_time)
    if metrics.empty:
        raise RuntimeError("No rows found in slo_metric_ts")
    allocations = load_allocations()
    train_df = build_training_frame(metrics, allocations, horizon_values)
    if train_df.empty:
        raise RuntimeError("No training rows could be built for SLO proxy model")

    X = _prepare_X(train_df)
    p95_model = LGBMRegressor(
        n_estimators=160,
        learning_rate=0.05,
        max_depth=-1,
        min_child_samples=1,
        min_data_in_bin=1,
        verbose=-1,
        random_state=43,
    )
    p99_model = LGBMRegressor(
        n_estimators=160,
        learning_rate=0.05,
        max_depth=-1,
        min_child_samples=1,
        min_data_in_bin=1,
        verbose=-1,
        random_state=44,
    )
    p95_model.fit(X, train_df["target_p95_latency"])
    p99_model.fit(X, train_df["target_p99_latency"])

    y = train_df["target_violation"].astype(int)
    violation_model = None
    violation_constant = None
    if len(y.unique()) > 1:
        violation_model = LGBMClassifier(
            objective="binary",
            n_estimators=120,
            learning_rate=0.05,
            max_depth=-1,
            min_child_samples=1,
            min_data_in_bin=1,
            verbose=-1,
            random_state=45,
        )
        violation_model.fit(X, y)
    else:
        violation_constant = float(y.iloc[0])

    bundle = {
        "model_name": "slo_proxy_lgbm",
        "model_version": "v0.3_lgbm_baseline",
        "feature_columns": FEATURE_COLUMNS,
        "horizons": horizon_values,
        "p95_model": p95_model,
        "p99_model": p99_model,
        "violation_model": violation_model,
        "violation_constant": violation_constant,
    }
    joblib.dump(bundle, MODEL_PATH)

    metadata = {
        "model_name": bundle["model_name"],
        "model_version": bundle["model_version"],
        "created_at": datetime.now().isoformat(),
        "feature_columns": FEATURE_COLUMNS,
        "horizons": horizon_values,
        "metric_rows": int(len(metrics)),
        "train_rows": int(len(train_df)),
        "allocation_rows": int(len(allocations)),
        "violation_rate": float(y.mean()),
        "model_file": str(MODEL_PATH),
    }
    METADATA_PATH.write_text(json.dumps(metadata, indent=2, ensure_ascii=False))
    return metadata


def _heuristic_predict(row: Dict[str, object]) -> Tuple[float, float, float]:
    growth = max(_num(row.get("qps_forecast")) / max(_num(row.get("qps_forecast")) - _num(row.get("qps_per_replica")) * 0.02, 1.0), 1.0)
    cpu_pressure = max(_num(row.get("predicted_cpu_util")) - 70.0, 0.0)
    gpu_pressure = max(_num(row.get("predicted_gpu_util")) - 82.0, 0.0)
    resource_factor = 1 + cpu_pressure / 100.0 + gpu_pressure / 140.0
    p95 = (_num(row.get("p95_latency")) * math.pow(growth, 0.45) + _num(row.get("horizon_minutes")) * 0.2) * resource_factor
    spread = max(_num(row.get("p99_latency")) - _num(row.get("p95_latency")), _num(row.get("p95_latency")) * 0.25)
    p99 = p95 + spread * math.sqrt(growth) + cpu_pressure * 1.4 + gpu_pressure * 1.8
    latency_pressure = (p99 - _num(row.get("p99_latency_budget"), 1.0)) / max(_num(row.get("p99_latency_budget"), 1.0), 1e-9)
    error_pressure = (_num(row.get("error_rate")) - _num(row.get("error_rate_budget"), 0.1)) / max(_num(row.get("error_rate_budget"), 0.1), 1e-9)
    violation = min(max(_sigmoid(latency_pressure * 4 + error_pressure * 1.4) - 0.08, 0.01), 0.99)
    return p95, p99, violation


def build_prediction_features(horizons: List[int], limit: int | None = None) -> pd.DataFrame:
    latest = load_latest_metrics()
    if latest.empty:
        raise RuntimeError("No rows found in slo_metric_ts")
    if limit:
        latest = latest.head(limit)
    allocations = load_allocations()
    if allocations.empty:
        allocations = synth_allocations(latest)
    lookup = _budget_lookup(allocations)
    rows: List[Dict[str, object]] = []

    if not allocations.empty:
        metric_lookup = {_service_key(r.get("service_id", ""), r.get("api_id", "")): r for _, r in latest.iterrows()}
        for _, budget_row in allocations.iterrows():
            key = _service_key(budget_row.get("service_id", ""), budget_row.get("api_id", ""))
            metric = metric_lookup.get(key)
            if metric is None:
                service_matches = [r for k, r in metric_lookup.items() if k[0] == key[0]]
                metric = service_matches[0] if service_matches else pd.Series({
                    "cluster_uuid": budget_row.get("cluster_uuid", ""),
                    "service_id": budget_row.get("service_id", ""),
                    "service_name": budget_row.get("service_name", ""),
                    "api_id": budget_row.get("api_id", ""),
                    "qps": budget_row.get("qps", 1.0),
                    "p95_latency": _num(budget_row.get("p99_latency_budget"), 500.0) * 0.55,
                    "p99_latency": _num(budget_row.get("p99_latency_budget"), 500.0) * 0.75,
                    "error_rate": budget_row.get("error_rate", 0.0),
                    "replica_count": 1,
                    "cpu_util": 30,
                    "gpu_util": 0,
                })
            for horizon in horizons:
                rows.append(_feature_record(metric, budget_row.to_dict(), horizon))
    else:
        for _, metric in latest.iterrows():
            budget = _pick_budget(metric, lookup)
            for horizon in horizons:
                rows.append(_feature_record(metric, budget, horizon))
    return pd.DataFrame(rows)


def build_sequence_prediction_data(
    horizons: List[int],
    seq_len: int,
    limit: int | None = None,
) -> Tuple[pd.DataFrame, np.ndarray]:
    metrics = load_metrics(None, None)
    if metrics.empty:
        raise RuntimeError("No rows found in slo_metric_ts")
    allocations = load_allocations()
    if allocations.empty:
        allocations = synth_allocations(metrics)
    lookup = _budget_lookup(allocations)
    group_cols = _group_columns(metrics)
    metrics = metrics.sort_values(group_cols + ["metric_time"]).reset_index(drop=True)

    rows: List[Dict[str, object]] = []
    sequences: List[np.ndarray] = []
    grouped = list(metrics.groupby(group_cols, dropna=False))
    if limit:
        grouped = grouped[:limit]

    for _, group in grouped:
        group = group.reset_index(drop=True)
        current = group.iloc[-1]
        budget = _pick_budget(current, lookup)
        hist = _history_window(group, len(group) - 1, seq_len)
        if hist.empty:
            continue
        for horizon in horizons:
            rows.append(_feature_record(current, budget, horizon))
            seq_rows = [_feature_record(hist_row, budget, horizon) for _, hist_row in hist.iterrows()]
            sequences.append(_sequence_features(seq_rows))

    if not sequences:
        return pd.DataFrame(), np.empty((0, seq_len, len(FEATURE_COLUMNS)), dtype=np.float32)
    return pd.DataFrame(rows), np.stack(sequences).astype(np.float32)


def build_gnn_prediction_data(
    horizons: List[int],
    seq_len: int,
    node_keys: List[Tuple[str, str, str]],
    limit: int | None = None,
) -> Tuple[pd.DataFrame, np.ndarray, np.ndarray, np.ndarray]:
    metrics = load_metrics(None, None)
    if metrics.empty:
        raise RuntimeError("No rows found in slo_metric_ts")
    allocations = load_allocations()
    if allocations.empty:
        allocations = synth_allocations(metrics)
    lookup = _budget_lookup(allocations)
    node_groups = _build_node_groups(metrics)
    latest_time = pd.Timestamp(metrics["metric_time"].max())

    selected_node_indices = list(range(len(node_keys)))
    if limit:
        selected_node_indices = selected_node_indices[:limit]

    rows: List[Dict[str, object]] = []
    graph_samples: List[np.ndarray] = []
    row_graph_indices: List[int] = []
    row_node_indices: List[int] = []

    for horizon in horizons:
        graph_idx = len(graph_samples)
        graph_samples.append(_graph_snapshot_features(node_groups, node_keys, lookup, latest_time, horizon, seq_len))
        for node_idx in selected_node_indices:
            key = node_keys[node_idx]
            current = _current_row_for_node(node_groups, key, latest_time)
            budget = _pick_budget(current, lookup)
            rows.append(_feature_record(current, budget, horizon))
            row_graph_indices.append(graph_idx)
            row_node_indices.append(node_idx)

    if not graph_samples:
        return (
            pd.DataFrame(),
            np.empty((0, len(node_keys), seq_len, len(FEATURE_COLUMNS)), dtype=np.float32),
            np.empty((0,), dtype=np.int64),
            np.empty((0,), dtype=np.int64),
        )
    return (
        pd.DataFrame(rows),
        np.stack(graph_samples).astype(np.float32),
        np.asarray(row_graph_indices, dtype=np.int64),
        np.asarray(row_node_indices, dtype=np.int64),
    )


def _build_prediction_result(
    features: pd.DataFrame,
    p95_pred: np.ndarray,
    p99_pred: np.ndarray,
    violation_pred: np.ndarray,
    mode: str,
    model_name: str,
    model_version: str,
    write_predictions: bool,
) -> Dict[str, object]:
    now = datetime.now().replace(microsecond=0)
    records: List[Dict[str, object]] = []
    for idx, (_, row) in enumerate(features.iterrows()):
        horizon = _int(row.get("horizon_minutes"), 15)
        p95 = max(float(p95_pred[idx]), 0.0)
        p99 = max(float(p99_pred[idx]), p95 * 1.05)
        records.append({
            "objective_id": row.get("objective_id", DEFAULT_OBJECTIVE_ID),
            "cluster_uuid": row.get("cluster_uuid", ""),
            "business_flow": row.get("business_flow", DEFAULT_BUSINESS_FLOW),
            "service_id": row.get("service_id", ""),
            "service_name": row.get("service_name", ""),
            "api_id": row.get("api_id", ""),
            "forecast_time": (now + timedelta(minutes=horizon)).strftime("%Y-%m-%d %H:%M:%S"),
            "horizon_minutes": horizon,
            "qps_forecast": round(_num(row.get("qps_forecast")), 2),
            "request_mix": row.get("request_mix", "default"),
            "replica_count": _int(row.get("replica_count"), 1),
            "cpu_request": round(_num(row.get("cpu_request")), 2),
            "gpu_request": round(_num(row.get("gpu_request")), 2),
            "memory_request_gb": round(_num(row.get("memory_request_gb")), 2),
            "predicted_cpu_util": round(_num(row.get("predicted_cpu_util")), 2),
            "predicted_gpu_util": round(_num(row.get("predicted_gpu_util")), 2),
            "p99_latency_budget": round(_num(row.get("p99_latency_budget")), 2),
            "error_rate_budget": round(_num(row.get("error_rate_budget")), 4),
            "predicted_p95_latency": round(p95, 2),
            "predicted_p99_latency": round(p99, 2),
            "violation_probability": round(float(np.clip(violation_pred[idx], 0, 1)), 4),
            "model_name": model_name,
            "model_version": model_version,
        })

    if write_predictions:
        upsert_predictions(records)
    return {
        "mode": mode,
        "model_name": model_name,
        "model_version": model_version,
        "count": len(records),
        "wrote_predictions": write_predictions,
        "records": records,
    }


def predict(horizons: str, limit: int | None, write_predictions: bool, model_mode: str = "auto") -> Dict[str, object]:
    horizon_values = _parse_horizons(horizons)
    features = build_prediction_features(horizon_values, limit=limit)
    if features.empty:
        raise RuntimeError("No prediction rows could be built")

    X = _prepare_X(features)
    mode = "heuristic"
    model_name = "slo_proxy_heuristic"
    model_version = "v0.3_heuristic_baseline"
    if model_mode not in {"auto", "lgbm", "heuristic"}:
        raise RuntimeError(f"Unsupported baseline model mode: {model_mode}")
    if model_mode == "lgbm" and not MODEL_PATH.exists():
        raise RuntimeError(f"LightGBM checkpoint not found: {MODEL_PATH}")
    if model_mode in {"auto", "lgbm"} and MODEL_PATH.exists():
        bundle = joblib.load(MODEL_PATH)
        p95_pred = bundle["p95_model"].predict(X)
        p99_pred = bundle["p99_model"].predict(X)
        violation_model = bundle.get("violation_model")
        if violation_model is not None:
            violation_pred = violation_model.predict_proba(X)[:, 1]
        else:
            violation_pred = np.full(len(features), float(bundle.get("violation_constant") or 0.0))
        mode = "lightgbm"
        model_name = bundle.get("model_name", "slo_proxy_lgbm")
        model_version = bundle.get("model_version", "v0.3_lgbm_baseline")
    else:
        p95_vals, p99_vals, violation_vals = [], [], []
        for _, row in features.iterrows():
            p95, p99, violation = _heuristic_predict(row.to_dict())
            p95_vals.append(p95)
            p99_vals.append(p99)
            violation_vals.append(violation)
        p95_pred = np.asarray(p95_vals)
        p99_pred = np.asarray(p99_vals)
        violation_pred = np.asarray(violation_vals)

    return _build_prediction_result(
        features=features,
        p95_pred=p95_pred,
        p99_pred=p99_pred,
        violation_pred=violation_pred,
        mode=mode,
        model_name=model_name,
        model_version=model_version,
        write_predictions=write_predictions,
    )


def predict_sequence(
    model_type: str,
    horizons: str,
    limit: int | None,
    write_predictions: bool,
    device: str,
) -> Dict[str, object]:
    model_type = model_type.lower()
    if model_type not in SEQUENCE_MODEL_TYPES:
        raise RuntimeError(f"Unsupported sequence model type: {model_type}")

    model_path = _sequence_model_path(model_type)
    if not model_path.exists():
        raise RuntimeError(f"{model_type.upper()} sequence checkpoint not found: {model_path}")

    torch, _, _, _ = _import_torch()
    device_obj = _resolve_device(torch, device)
    checkpoint = _torch_load(model_path, device_obj)
    seq_len = int(checkpoint.get("seq_len", 12))
    horizon_values = _parse_horizons(horizons)
    features, X = build_sequence_prediction_data(horizon_values, seq_len=seq_len, limit=limit)
    if features.empty or len(X) == 0:
        raise RuntimeError("No sequence prediction rows could be built")

    model = _build_sequence_model(
        model_type=checkpoint.get("model_type", model_type),
        input_dim=int(checkpoint.get("input_dim", len(FEATURE_COLUMNS))),
        hidden_dim=int(checkpoint.get("hidden_dim", 64)),
        num_layers=int(checkpoint.get("num_layers", 2)),
        dropout=float(checkpoint.get("dropout", 0.1)),
        kernel_size=int(checkpoint.get("kernel_size", 3)),
    ).to(device_obj)
    model.load_state_dict(checkpoint["model_state"])

    X_scaled = _apply_sequence_scaler(X, checkpoint["feature_scaler"])
    out = _predict_sequence_array(model, X_scaled, batch_size=256, device=device_obj, torch=torch)
    pred_reg = _inverse_target_scaler(out[:, :2], checkpoint["target_scaler"])
    p95_pred = np.clip(pred_reg[:, 0], 0.0, None)
    p99_pred = np.maximum(np.clip(pred_reg[:, 1], 0.0, None), p95_pred * 1.05)
    violation_pred = _sigmoid_array(out[:, 2])
    return _build_prediction_result(
        features=features,
        p95_pred=p95_pred,
        p99_pred=p99_pred,
        violation_pred=violation_pred,
        mode=model_type,
        model_name=f"slo_proxy_sequence_{model_type}",
        model_version=f"v0.3_{model_type}_sequence",
        write_predictions=write_predictions,
    )


def predict_gnn(
    horizons: str,
    limit: int | None,
    write_predictions: bool,
    device: str,
) -> Dict[str, object]:
    model_path = _sequence_model_path(GNN_MODEL_TYPE)
    if not model_path.exists():
        raise RuntimeError(f"GNN checkpoint not found: {model_path}")

    torch, _, _, _ = _import_torch()
    device_obj = _resolve_device(torch, device)
    checkpoint = _torch_load(model_path, device_obj)
    node_keys = _decode_node_keys(checkpoint.get("node_keys", []))
    if not node_keys:
        raise RuntimeError("GNN checkpoint does not contain graph node metadata")

    seq_len = int(checkpoint.get("seq_len", 12))
    horizon_values = _parse_horizons(horizons)
    features, X, row_graph_idx, row_node_idx = build_gnn_prediction_data(
        horizon_values,
        seq_len=seq_len,
        node_keys=node_keys,
        limit=limit,
    )
    if features.empty or len(X) == 0:
        raise RuntimeError("No GNN prediction rows could be built")

    model = _build_gnn_model(
        input_dim=int(checkpoint.get("input_dim", len(FEATURE_COLUMNS))),
        hidden_dim=int(checkpoint.get("hidden_dim", 64)),
        num_layers=int(checkpoint.get("num_layers", 2)),
        dropout=float(checkpoint.get("dropout", 0.1)),
        graph_layers=int(checkpoint.get("graph_layers", 2)),
    ).to(device_obj)
    model.load_state_dict(checkpoint["model_state"])
    adjacency = torch.as_tensor(np.asarray(checkpoint["adjacency"], dtype=np.float32), device=device_obj)

    X_scaled = _apply_sequence_scaler(X, checkpoint["feature_scaler"])
    out = _predict_gnn_array(model, X_scaled, adjacency, batch_size=256, device=device_obj, torch=torch)
    selected = out[row_graph_idx, row_node_idx]
    pred_reg = _inverse_target_scaler(selected[:, :2], checkpoint["target_scaler"])
    p95_pred = np.clip(pred_reg[:, 0], 0.0, None)
    p99_pred = np.maximum(np.clip(pred_reg[:, 1], 0.0, None), p95_pred * 1.05)
    violation_pred = _sigmoid_array(selected[:, 2])
    return _build_prediction_result(
        features=features,
        p95_pred=p95_pred,
        p99_pred=p99_pred,
        violation_pred=violation_pred,
        mode=GNN_MODEL_TYPE,
        model_name="slo_proxy_gnn",
        model_version="v0.3_gnn_predictor",
        write_predictions=write_predictions,
    )


def upsert_predictions(records: List[Dict[str, object]]) -> None:
    ensure_schema()
    sql = """
INSERT INTO slo_proxy_prediction
  (objective_id, cluster_uuid, business_flow, service_id, service_name, api_id,
   forecast_time, horizon_minutes, qps_forecast, request_mix, replica_count,
   cpu_request, gpu_request, memory_request_gb, predicted_cpu_util, predicted_gpu_util,
   p99_latency_budget, error_rate_budget, predicted_p95_latency, predicted_p99_latency,
   violation_probability, model_name, model_version)
VALUES
  (%(objective_id)s, %(cluster_uuid)s, %(business_flow)s, %(service_id)s, %(service_name)s, %(api_id)s,
   %(forecast_time)s, %(horizon_minutes)s, %(qps_forecast)s, %(request_mix)s, %(replica_count)s,
   %(cpu_request)s, %(gpu_request)s, %(memory_request_gb)s, %(predicted_cpu_util)s, %(predicted_gpu_util)s,
   %(p99_latency_budget)s, %(error_rate_budget)s, %(predicted_p95_latency)s, %(predicted_p99_latency)s,
   %(violation_probability)s, %(model_name)s, %(model_version)s)
ON DUPLICATE KEY UPDATE
  cluster_uuid = VALUES(cluster_uuid),
  business_flow = VALUES(business_flow),
  service_name = VALUES(service_name),
  forecast_time = VALUES(forecast_time),
  qps_forecast = VALUES(qps_forecast),
  request_mix = VALUES(request_mix),
  replica_count = VALUES(replica_count),
  cpu_request = VALUES(cpu_request),
  gpu_request = VALUES(gpu_request),
  memory_request_gb = VALUES(memory_request_gb),
  predicted_cpu_util = VALUES(predicted_cpu_util),
  predicted_gpu_util = VALUES(predicted_gpu_util),
  p99_latency_budget = VALUES(p99_latency_budget),
  error_rate_budget = VALUES(error_rate_budget),
  predicted_p95_latency = VALUES(predicted_p95_latency),
  predicted_p99_latency = VALUES(predicted_p99_latency),
  violation_probability = VALUES(violation_probability),
  model_version = VALUES(model_version),
  created_at = CURRENT_TIMESTAMP
"""
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.executemany(sql, records)
        conn.commit()


def main():
    parser = argparse.ArgumentParser(description="Train or run the V0.3 SLO proxy model")
    sub = parser.add_subparsers(dest="cmd", required=True)

    train_parser = sub.add_parser("train")
    train_parser.add_argument("--model", choices=["lgbm", "gru", "tcn", "gnn"], default="lgbm")
    train_parser.add_argument("--start-time", default=None)
    train_parser.add_argument("--end-time", default=None)
    train_parser.add_argument("--horizons", default="15,30,60")
    train_parser.add_argument("--seq-len", type=int, default=24)
    train_parser.add_argument("--stride", type=int, default=1)
    train_parser.add_argument("--epochs", type=int, default=30)
    train_parser.add_argument("--batch-size", type=int, default=64)
    train_parser.add_argument("--learning-rate", type=float, default=1e-3)
    train_parser.add_argument("--hidden-dim", type=int, default=64)
    train_parser.add_argument("--num-layers", type=int, default=2)
    train_parser.add_argument("--graph-layers", type=int, default=2)
    train_parser.add_argument("--dropout", type=float, default=0.1)
    train_parser.add_argument("--kernel-size", type=int, default=3)
    train_parser.add_argument("--violation-loss-weight", type=float, default=0.5)
    train_parser.add_argument("--device", default="auto")

    predict_parser = sub.add_parser("predict")
    predict_parser.add_argument("--model", choices=["auto", "lgbm", "heuristic", "gru", "tcn", "gnn"], default="auto")
    predict_parser.add_argument("--horizons", default="15,30,60")
    predict_parser.add_argument("--limit", type=int, default=None)
    predict_parser.add_argument("--write-predictions", action="store_true")
    predict_parser.add_argument("--device", default="auto")

    args = parser.parse_args()
    if args.cmd == "train":
        if args.model == "lgbm":
            result = train(args.start_time, args.end_time, args.horizons)
        elif args.model == GNN_MODEL_TYPE:
            result = train_gnn(
                start_time=args.start_time,
                end_time=args.end_time,
                horizons=args.horizons,
                seq_len=args.seq_len,
                stride=args.stride,
                epochs=args.epochs,
                batch_size=args.batch_size,
                learning_rate=args.learning_rate,
                hidden_dim=args.hidden_dim,
                num_layers=args.num_layers,
                graph_layers=args.graph_layers,
                dropout=args.dropout,
                violation_loss_weight=args.violation_loss_weight,
                device=args.device,
            )
        else:
            result = train_sequence(
                model_type=args.model,
                start_time=args.start_time,
                end_time=args.end_time,
                horizons=args.horizons,
                seq_len=args.seq_len,
                stride=args.stride,
                epochs=args.epochs,
                batch_size=args.batch_size,
                learning_rate=args.learning_rate,
                hidden_dim=args.hidden_dim,
                num_layers=args.num_layers,
                dropout=args.dropout,
                kernel_size=args.kernel_size,
                violation_loss_weight=args.violation_loss_weight,
                device=args.device,
            )
    else:
        if args.model == GNN_MODEL_TYPE:
            result = predict_gnn(args.horizons, args.limit, args.write_predictions, args.device)
        elif args.model in SEQUENCE_MODEL_TYPES:
            result = predict_sequence(args.model, args.horizons, args.limit, args.write_predictions, args.device)
        else:
            result = predict(args.horizons, args.limit, args.write_predictions, model_mode=args.model)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
