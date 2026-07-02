"""Import one raw collection run into green_node_metrics/green_node_info.

The collector writes CSV files under pod_raw_runs/<run_id>. Topic 1 training
currently reads MySQL tables, so this script normalizes the raw files into the
training schema. By default it only prints a conversion summary; pass --write to
insert rows into MySQL.
"""
import argparse
import json
import math
import sys
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional

import numpy as np
import pandas as pd
import pymysql

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from model_service import config
else:
    from . import config


NA_VALUES = ["NA", "N/A", "nan", "NaN", ""]

GREEN_NODE_METRICS_COLUMNS = [
    "node_uuid",
    "cluster_uuid",
    "metric_time",
    "cpu_usage",
    "gpu_usage",
    "gpu_memory_usage",
    "memory_usage",
    "io_read",
    "io_write",
    "network_rx",
    "network_tx",
    "node_power",
    "ups_power",
    "service_energy",
    "task_type",
    "batch_size",
    "concurrency",
    "request_rate",
    "replica_count",
    "schedule_policy",
    "requests",
    "limits",
]

GREEN_NODE_INFO_COLUMNS = [
    "cluster_uuid",
    "node_uuid",
    "cpu_model",
    "cpu_cores",
    "memory_gb",
    "gpu_model",
    "gpu_count",
    "gpu_mem_gb",
    "rated_power",
    "node_type",
    "requests",
    "limits",
    "os",
    "kernel",
]


def _read_json(path: Path) -> Dict[str, Any]:
    if not path.exists():
        return {}
    with path.open("r", encoding="utf-8") as fh:
        return json.load(fh)


def _read_csv(run_dir: Path, name: str) -> pd.DataFrame:
    path = run_dir / name
    if not path.exists():
        return pd.DataFrame()
    return pd.read_csv(path, na_values=NA_VALUES)


def _to_numeric(series: pd.Series, default: Optional[float] = None) -> pd.Series:
    values = pd.to_numeric(series, errors="coerce")
    if default is not None:
        values = values.fillna(default)
    return values


def _to_datetime(series: pd.Series) -> pd.Series:
    return pd.to_datetime(series, errors="coerce")


def _clean_str(value: Any, default: str = "") -> str:
    if value is None:
        return default
    if isinstance(value, float) and math.isnan(value):
        return default
    text = str(value).strip()
    if not text or text.upper() == "NA":
        return default
    return text


def _first_nonempty(frame: pd.DataFrame, column: str, default: str = "") -> str:
    if column not in frame.columns:
        return default
    values = frame[column].dropna()
    if values.empty:
        return default
    return _clean_str(values.iloc[0], default)


def _first_number(frame: pd.DataFrame, column: str, default: float = 0.0) -> float:
    if column not in frame.columns:
        return default
    values = pd.to_numeric(frame[column], errors="coerce").dropna()
    if values.empty:
        return default
    return float(values.iloc[0])


def _mode_non_unknown(values: pd.Series) -> str:
    cleaned = values.dropna().map(lambda item: _clean_str(item, "unknown"))
    useful = cleaned[cleaned != "unknown"]
    if useful.empty:
        useful = cleaned
    if useful.empty:
        return "unknown"
    return str(useful.mode().iloc[0])


def _counter_rate(frame: pd.DataFrame, group_col: str, value_col: str, time_col: str) -> pd.Series:
    values = pd.to_numeric(frame[value_col], errors="coerce")
    elapsed = frame.groupby(group_col)[time_col].diff().dt.total_seconds()
    delta = values.groupby(frame[group_col]).diff()
    rate = delta / elapsed
    rate = rate.where(rate >= 0, 0.0)
    return rate.replace([np.inf, -np.inf], np.nan).fillna(0.0)


def _static_total_gpu_mem_mb(node_info: pd.DataFrame) -> Dict[str, float]:
    totals: Dict[str, float] = {}
    if node_info.empty:
        return totals
    for _, row in node_info.iterrows():
        node = _clean_str(row.get("node_name") or row.get("node_uuid"))
        if not node:
            continue
        gpu_count = pd.to_numeric(pd.Series([row.get("gpu_count")]), errors="coerce").iloc[0]
        gpu_mem_gb = pd.to_numeric(pd.Series([row.get("gpu_mem_gb")]), errors="coerce").iloc[0]
        if pd.notna(gpu_count) and pd.notna(gpu_mem_gb) and gpu_count > 0 and gpu_mem_gb > 0:
            totals[node] = float(gpu_count) * float(gpu_mem_gb) * 1024.0
    return totals


def _build_gpu_aggregate(gpu_raw: pd.DataFrame, node_info: pd.DataFrame, warnings: List[str]) -> pd.DataFrame:
    if gpu_raw.empty:
        warnings.append("gpu_metrics_raw.csv is missing or empty; gpu fields will be 0")
        return pd.DataFrame(columns=["metric_time", "node_name", "gpu_usage", "gpu_memory_usage", "gpu_power_sum"])

    gpu = gpu_raw.copy()
    gpu["metric_time"] = _to_datetime(gpu["timestamp"])
    gpu = gpu.dropna(subset=["metric_time", "node_name"])
    if gpu.empty:
        warnings.append("gpu_metrics_raw.csv has no valid timestamp/node rows")
        return pd.DataFrame(columns=["metric_time", "node_name", "gpu_usage", "gpu_memory_usage", "gpu_power_sum"])

    for col in ["gpu_util_percent", "gpu_memory_util_percent", "gpu_memory_used_mb", "gpu_memory_total_mb", "gpu_power_w"]:
        if col not in gpu.columns:
            gpu[col] = np.nan
        gpu[col] = pd.to_numeric(gpu[col], errors="coerce")

    grouped = gpu.groupby(["metric_time", "node_name"], as_index=False).agg(
        gpu_usage=("gpu_util_percent", "mean"),
        gpu_memory_pct=("gpu_memory_util_percent", "mean"),
        gpu_memory_used_mb=("gpu_memory_used_mb", "sum"),
        gpu_memory_total_mb=("gpu_memory_total_mb", "sum"),
        gpu_power_sum=("gpu_power_w", "sum"),
    )

    static_total = _static_total_gpu_mem_mb(node_info)
    computed_pct = []
    for _, row in grouped.iterrows():
        total = row["gpu_memory_total_mb"]
        if pd.isna(total) or total <= 0:
            total = static_total.get(_clean_str(row["node_name"]), np.nan)
        used = row["gpu_memory_used_mb"]
        if pd.notna(used) and pd.notna(total) and total > 0:
            computed_pct.append(float(used) / float(total) * 100.0)
        else:
            computed_pct.append(np.nan)

    grouped["gpu_memory_usage"] = grouped["gpu_memory_pct"].combine_first(pd.Series(computed_pct, index=grouped.index))
    if grouped["gpu_memory_pct"].isna().all() and grouped["gpu_memory_usage"].notna().any():
        warnings.append("gpu_memory_util_percent is empty; computed gpu_memory_usage from used memory and static GPU memory")
    grouped["gpu_usage"] = grouped["gpu_usage"].fillna(0.0)
    grouped["gpu_memory_usage"] = grouped["gpu_memory_usage"].fillna(0.0)
    grouped["gpu_power_sum"] = grouped["gpu_power_sum"].fillna(0.0)
    return grouped[["metric_time", "node_name", "gpu_usage", "gpu_memory_usage", "gpu_power_sum"]]


def _build_workload_context(workload_raw: pd.DataFrame, slo_raw: pd.DataFrame) -> pd.DataFrame:
    if workload_raw.empty:
        return pd.DataFrame(
            columns=[
                "metric_time",
                "node_name",
                "task_type",
                "replica_count",
                "requests",
                "limits",
                "batch_size",
                "concurrency",
                "request_rate",
            ]
        )

    workload = workload_raw.copy()
    workload["metric_time"] = _to_datetime(workload["timestamp"])
    workload = workload.dropna(subset=["metric_time", "node_name"])
    for col in [
        "replicas",
        "cpu_request_cores",
        "cpu_limit_cores",
        "batch_size",
        "concurrency",
    ]:
        if col not in workload.columns:
            workload[col] = np.nan
        workload[col] = pd.to_numeric(workload[col], errors="coerce")

    workload_level = workload.groupby(["metric_time", "node_name", "workload_name"], as_index=False).agg(
        task_type=("task_type", _mode_non_unknown),
        replicas=("replicas", "max"),
        cpu_request_cores=("cpu_request_cores", "sum"),
        cpu_limit_cores=("cpu_limit_cores", "sum"),
        batch_size=("batch_size", "mean"),
        concurrency=("concurrency", "mean"),
    )

    node_context = workload_level.groupby(["metric_time", "node_name"], as_index=False).agg(
        task_type=("task_type", _mode_non_unknown),
        replica_count=("replicas", "sum"),
        requests=("cpu_request_cores", "sum"),
        limits=("cpu_limit_cores", "sum"),
        batch_size=("batch_size", "mean"),
        concurrency=("concurrency", "sum"),
    )

    node_context["request_rate"] = 0.0
    if not slo_raw.empty and {"timestamp", "workload_name", "qps"}.issubset(slo_raw.columns):
        slo = slo_raw[["timestamp", "workload_name", "qps"]].copy()
        slo["metric_time"] = _to_datetime(slo["timestamp"])
        slo["qps"] = pd.to_numeric(slo["qps"], errors="coerce")
        workload_nodes = workload[["metric_time", "workload_name", "node_name"]].drop_duplicates()
        slo_with_node = slo.merge(workload_nodes, on=["metric_time", "workload_name"], how="left")
        qps_by_node = slo_with_node.dropna(subset=["node_name"]).groupby(["metric_time", "node_name"], as_index=False)["qps"].sum()
        node_context = node_context.drop(columns=["request_rate"]).merge(qps_by_node.rename(columns={"qps": "request_rate"}), on=["metric_time", "node_name"], how="left")

    for col in ["replica_count", "requests", "limits", "batch_size", "concurrency", "request_rate"]:
        node_context[col] = pd.to_numeric(node_context[col], errors="coerce").fillna(0.0)
    node_context["task_type"] = node_context["task_type"].fillna("unknown")
    return node_context


def build_green_node_info(run_dir: Path, cluster_uuid: str, metrics_nodes: Iterable[str]) -> pd.DataFrame:
    static_raw = _read_csv(run_dir, "node_static_info.csv")
    workload_raw = _read_csv(run_dir, "workload_config_raw.csv")
    rows: List[Dict[str, Any]] = []
    metric_node_set = {_clean_str(node) for node in metrics_nodes if _clean_str(node)}

    if not static_raw.empty:
        for _, row in static_raw.iterrows():
            node = _clean_str(row.get("node_name"))
            if not node:
                continue
            rows.append(
                {
                    "cluster_uuid": cluster_uuid,
                    "node_uuid": node,
                    "cpu_model": _clean_str(row.get("cpu_model"), "unknown"),
                    "cpu_cores": int(_first_number(pd.DataFrame([row]), "cpu_cores", 0)),
                    "memory_gb": _first_number(pd.DataFrame([row]), "memory_gb", 0.0),
                    "gpu_model": _clean_str(row.get("gpu_model"), "none"),
                    "gpu_count": int(_first_number(pd.DataFrame([row]), "gpu_count", 0)),
                    "gpu_mem_gb": _first_number(pd.DataFrame([row]), "gpu_mem_gb", 0.0),
                    "rated_power": _first_number(pd.DataFrame([row]), "rated_power_w", 0.0),
                    "node_type": _clean_str(row.get("node_type"), "unknown"),
                    "requests": 0.0,
                    "limits": 0.0,
                    "os": _clean_str(row.get("os"), ""),
                    "kernel": _clean_str(row.get("kernel"), ""),
                }
            )
            metric_node_set.discard(node)

    for node in sorted(metric_node_set):
        rows.append(
            {
                "cluster_uuid": cluster_uuid,
                "node_uuid": node,
                "cpu_model": "unknown",
                "cpu_cores": 0,
                "memory_gb": 0.0,
                "gpu_model": "unknown",
                "gpu_count": 0,
                "gpu_mem_gb": 0.0,
                "rated_power": 0.0,
                "node_type": "unknown",
                "requests": 0.0,
                "limits": 0.0,
                "os": "",
                "kernel": "",
            }
        )

    info = pd.DataFrame(rows, columns=GREEN_NODE_INFO_COLUMNS)
    if not info.empty and not workload_raw.empty:
        workload = workload_raw.copy()
        for col in ["cpu_request_cores", "cpu_limit_cores"]:
            if col not in workload.columns:
                workload[col] = np.nan
            workload[col] = pd.to_numeric(workload[col], errors="coerce")
        by_node = workload.groupby("node_name", as_index=False).agg(
            requests=("cpu_request_cores", "sum"),
            limits=("cpu_limit_cores", "sum"),
        )
        info = info.drop(columns=["requests", "limits"]).merge(
            by_node.rename(columns={"node_name": "node_uuid"}), on="node_uuid", how="left"
        )
        info["requests"] = pd.to_numeric(info["requests"], errors="coerce").fillna(0.0)
        info["limits"] = pd.to_numeric(info["limits"], errors="coerce").fillna(0.0)
    return info


def build_green_node_metrics(
    run_dir: Path,
    cluster_uuid: str,
    counter_mode: str,
    node_power_fallback: str,
) -> tuple[pd.DataFrame, pd.DataFrame, List[str]]:
    warnings: List[str] = []
    node_raw = _read_csv(run_dir, "node_metrics_raw.csv")
    if node_raw.empty:
        raise ValueError("node_metrics_raw.csv is missing or empty")
    node = node_raw.copy()
    node["metric_time"] = _to_datetime(node["timestamp"])
    node = node.dropna(subset=["metric_time", "node_name"]).sort_values(["node_name", "metric_time"])

    metrics_nodes = node["node_name"].dropna().astype(str).unique().tolist()
    node_info = build_green_node_info(run_dir, cluster_uuid, metrics_nodes)
    gpu_agg = _build_gpu_aggregate(_read_csv(run_dir, "gpu_metrics_raw.csv"), _read_csv(run_dir, "node_static_info.csv"), warnings)
    workload_context = _build_workload_context(_read_csv(run_dir, "workload_config_raw.csv"), _read_csv(run_dir, "slo_metrics_raw.csv"))

    numeric_map = {
        "cpu_util_percent": "cpu_usage",
        "memory_util_percent": "memory_usage",
        "node_power_w": "node_power",
    }
    for source_col in list(numeric_map) + [
        "disk_read_bytes_total",
        "disk_write_bytes_total",
        "network_rx_bytes_total",
        "network_tx_bytes_total",
    ]:
        if source_col not in node.columns:
            node[source_col] = np.nan
        node[source_col] = pd.to_numeric(node[source_col], errors="coerce")

    if counter_mode == "rate":
        node["io_read"] = _counter_rate(node, "node_name", "disk_read_bytes_total", "metric_time")
        node["io_write"] = _counter_rate(node, "node_name", "disk_write_bytes_total", "metric_time")
        node["network_rx"] = _counter_rate(node, "node_name", "network_rx_bytes_total", "metric_time")
        node["network_tx"] = _counter_rate(node, "node_name", "network_tx_bytes_total", "metric_time")
    else:
        node["io_read"] = node["disk_read_bytes_total"]
        node["io_write"] = node["disk_write_bytes_total"]
        node["network_rx"] = node["network_rx_bytes_total"]
        node["network_tx"] = node["network_tx_bytes_total"]

    out = pd.DataFrame(
        {
            "node_uuid": node["node_name"].map(lambda item: _clean_str(item, "unknown")),
            "cluster_uuid": cluster_uuid,
            "metric_time": node["metric_time"],
            "cpu_usage": node["cpu_util_percent"],
            "memory_usage": node["memory_util_percent"],
            "io_read": node["io_read"],
            "io_write": node["io_write"],
            "network_rx": node["network_rx"],
            "network_tx": node["network_tx"],
            "node_power": node["node_power_w"],
        }
    )
    out = out.merge(gpu_agg, left_on=["metric_time", "node_uuid"], right_on=["metric_time", "node_name"], how="left")
    out = out.drop(columns=[col for col in ["node_name"] if col in out.columns])

    if out["node_power"].isna().all():
        warnings.append("node_power_w is empty in node_metrics_raw.csv")
    if node_power_fallback == "gpu_power_sum":
        out["node_power"] = out["node_power"].combine_first(out["gpu_power_sum"])
        warnings.append("missing node_power_w values were filled with summed GPU power; this is not total server power")
    else:
        out["node_power"] = out["node_power"].fillna(0.0)
        warnings.append("missing node_power_w values were filled with 0; exclude node_power from formal targets")

    out = out.merge(workload_context, left_on=["metric_time", "node_uuid"], right_on=["metric_time", "node_name"], how="left")
    out = out.drop(columns=[col for col in ["node_name"] if col in out.columns])
    out["gpu_usage"] = pd.to_numeric(out.get("gpu_usage"), errors="coerce").fillna(0.0)
    out["gpu_memory_usage"] = pd.to_numeric(out.get("gpu_memory_usage"), errors="coerce").fillna(0.0)
    for col in ["cpu_usage", "memory_usage", "io_read", "io_write", "network_rx", "network_tx", "node_power"]:
        out[col] = pd.to_numeric(out[col], errors="coerce").fillna(0.0)
    for col in ["replica_count", "requests", "limits", "batch_size", "concurrency", "request_rate"]:
        if col not in out.columns:
            out[col] = 0.0
        out[col] = pd.to_numeric(out[col], errors="coerce").fillna(0.0)
    if "task_type" not in out.columns:
        out["task_type"] = "unknown"
    out["task_type"] = out["task_type"].fillna("unknown")
    out["schedule_policy"] = "raw_import"
    out["ups_power"] = 0.0
    out["service_energy"] = 0.0

    pod_raw = _read_csv(run_dir, "pod_metrics_raw.csv")
    if not pod_raw.empty and "node_name" in pod_raw.columns:
        pod_nodes = set(pod_raw["node_name"].dropna().astype(str))
        metric_nodes = set(out["node_uuid"].dropna().astype(str))
        unmatched = sorted(pod_nodes - metric_nodes)
        if unmatched:
            warnings.append(f"pod/workload nodes not present in node_metrics_raw.csv: {', '.join(unmatched[:8])}")

    out = out[GREEN_NODE_METRICS_COLUMNS].sort_values(["node_uuid", "metric_time"]).reset_index(drop=True)
    return out, node_info, warnings


def _connect():
    return pymysql.connect(**config.DB_CONFIG)


def _table_columns(conn, table_name: str) -> List[str]:
    with conn.cursor() as cur:
        cur.execute(f"SHOW COLUMNS FROM `{table_name}`")
        return [row["Field"] for row in cur.fetchall()]


def ensure_schema(conn) -> None:
    with conn.cursor() as cur:
        cur.execute(
            """
            CREATE TABLE IF NOT EXISTS green_node_metrics (
              id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
              node_uuid VARCHAR(64) NOT NULL,
              cluster_uuid VARCHAR(64) NOT NULL,
              metric_time DATETIME NOT NULL,
              cpu_usage DOUBLE NOT NULL DEFAULT 0,
              gpu_usage DOUBLE NOT NULL DEFAULT 0,
              gpu_memory_usage DOUBLE NOT NULL DEFAULT 0,
              memory_usage DOUBLE NOT NULL DEFAULT 0,
              io_read DOUBLE NOT NULL DEFAULT 0,
              io_write DOUBLE NOT NULL DEFAULT 0,
              network_rx DOUBLE NOT NULL DEFAULT 0,
              network_tx DOUBLE NOT NULL DEFAULT 0,
              node_power DOUBLE NOT NULL DEFAULT 0,
              ups_power DOUBLE NOT NULL DEFAULT 0,
              service_energy DOUBLE NOT NULL DEFAULT 0,
              task_type VARCHAR(32) NOT NULL DEFAULT 'unknown',
              batch_size INT NOT NULL DEFAULT 0,
              concurrency INT NOT NULL DEFAULT 0,
              request_rate DOUBLE NOT NULL DEFAULT 0,
              replica_count INT NOT NULL DEFAULT 0,
              schedule_policy VARCHAR(32) NOT NULL DEFAULT 'raw_import',
              requests DOUBLE NOT NULL DEFAULT 0,
              limits DOUBLE NOT NULL DEFAULT 0,
              INDEX idx_node_time (node_uuid, metric_time),
              INDEX idx_cluster_time (cluster_uuid, metric_time),
              INDEX idx_task_type (task_type)
            )
            """
        )
        cur.execute(
            """
            CREATE TABLE IF NOT EXISTS green_node_info (
              id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
              cluster_uuid VARCHAR(64) NOT NULL DEFAULT '',
              node_uuid VARCHAR(64) NOT NULL,
              cpu_model VARCHAR(128) NOT NULL DEFAULT 'unknown',
              cpu_cores INT NOT NULL DEFAULT 0,
              memory_gb DOUBLE NOT NULL DEFAULT 0,
              gpu_model VARCHAR(128) NOT NULL DEFAULT 'unknown',
              gpu_count INT NOT NULL DEFAULT 0,
              gpu_mem_gb DOUBLE NOT NULL DEFAULT 0,
              rated_power DOUBLE NOT NULL DEFAULT 0,
              node_type VARCHAR(32) NOT NULL DEFAULT 'unknown',
              requests DOUBLE NOT NULL DEFAULT 0,
              limits DOUBLE NOT NULL DEFAULT 0,
              os VARCHAR(128) NOT NULL DEFAULT '',
              kernel VARCHAR(128) NOT NULL DEFAULT '',
              created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
              updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
              UNIQUE KEY uk_cluster_node (cluster_uuid, node_uuid)
            )
            """
        )
    metric_cols = set(_table_columns(conn, "green_node_metrics"))
    metric_additions = {
        "requests": "ALTER TABLE green_node_metrics ADD COLUMN requests DOUBLE NOT NULL DEFAULT 0",
        "limits": "ALTER TABLE green_node_metrics ADD COLUMN limits DOUBLE NOT NULL DEFAULT 0",
        "request_rate": "ALTER TABLE green_node_metrics ADD COLUMN request_rate DOUBLE NOT NULL DEFAULT 0",
        "replica_count": "ALTER TABLE green_node_metrics ADD COLUMN replica_count INT NOT NULL DEFAULT 0",
        "schedule_policy": "ALTER TABLE green_node_metrics ADD COLUMN schedule_policy VARCHAR(32) NOT NULL DEFAULT 'raw_import'",
    }
    with conn.cursor() as cur:
        for col, sql in metric_additions.items():
            if col not in metric_cols:
                cur.execute(sql)
    conn.commit()


def _records(frame: pd.DataFrame, columns: List[str]) -> List[Dict[str, Any]]:
    clean = frame[columns].copy()
    clean = clean.replace([np.inf, -np.inf], np.nan)
    clean = clean.where(pd.notna(clean), None)
    if "metric_time" in clean.columns:
        clean["metric_time"] = np.array(pd.to_datetime(clean["metric_time"]).dt.to_pydatetime(), dtype=object)
    return clean.to_dict("records")


def _insert_rows(conn, table_name: str, frame: pd.DataFrame, columns: List[str], batch_size: int = 1000) -> int:
    if frame.empty:
        return 0
    placeholders = ", ".join([f"%({col})s" for col in columns])
    column_sql = ", ".join([f"`{col}`" for col in columns])
    sql = f"INSERT INTO `{table_name}` ({column_sql}) VALUES ({placeholders})"
    records = _records(frame, columns)
    with conn.cursor() as cur:
        for idx in range(0, len(records), batch_size):
            cur.executemany(sql, records[idx : idx + batch_size])
    return len(records)


def _upsert_node_info(conn, frame: pd.DataFrame, batch_size: int = 1000) -> int:
    if frame.empty:
        return 0
    columns = GREEN_NODE_INFO_COLUMNS
    placeholders = ", ".join([f"%({col})s" for col in columns])
    column_sql = ", ".join([f"`{col}`" for col in columns])
    update_sql = ", ".join([f"`{col}` = VALUES(`{col}`)" for col in columns if col not in {"cluster_uuid", "node_uuid"}])
    sql = f"INSERT INTO green_node_info ({column_sql}) VALUES ({placeholders}) ON DUPLICATE KEY UPDATE {update_sql}"
    records = _records(frame, columns)
    with conn.cursor() as cur:
        for idx in range(0, len(records), batch_size):
            cur.executemany(sql, records[idx : idx + batch_size])
    return len(records)


def _summarize(metrics: pd.DataFrame, node_info: pd.DataFrame, warnings: List[str], cluster_uuid: str) -> Dict[str, Any]:
    summary: Dict[str, Any] = {
        "cluster_uuid": cluster_uuid,
        "green_node_metrics_rows": int(len(metrics)),
        "green_node_info_rows": int(len(node_info)),
        "nodes": sorted(metrics["node_uuid"].dropna().unique().tolist()) if not metrics.empty else [],
        "warnings": warnings,
    }
    if not metrics.empty:
        summary["time_start"] = str(metrics["metric_time"].min())
        summary["time_end"] = str(metrics["metric_time"].max())
        summary["duration_hours"] = round((metrics["metric_time"].max() - metrics["metric_time"].min()).total_seconds() / 3600.0, 3)
        summary["one_minute_points_per_node"] = {
            node: int(group.set_index("metric_time").resample("1min").size().shape[0])
            for node, group in metrics.groupby("node_uuid")
        }
        for col in ["cpu_usage", "gpu_usage", "gpu_memory_usage", "memory_usage", "node_power", "io_read", "io_write", "network_rx", "network_tx", "requests", "limits"]:
            values = pd.to_numeric(metrics[col], errors="coerce")
            summary[col] = {
                "min": None if values.dropna().empty else round(float(values.min()), 6),
                "max": None if values.dropna().empty else round(float(values.max()), 6),
                "nonzero_rows": int((values.fillna(0.0) != 0.0).sum()),
            }
    return summary


def main() -> None:
    parser = argparse.ArgumentParser(description="Import raw collector CSV files into Topic 1 training tables")
    parser.add_argument("--run-dir", required=True)
    parser.add_argument("--cluster-uuid", default=None, help="defaults to manifest run_id")
    parser.add_argument("--counter-mode", choices=["rate", "total"], default="rate")
    parser.add_argument("--node-power-fallback", choices=["zero", "gpu_power_sum"], default="zero")
    parser.add_argument("--output-dir", default=None, help="optional directory for normalized preview CSV files")
    parser.add_argument("--write", action="store_true", help="write normalized rows into MySQL")
    parser.add_argument("--replace", action="store_true", help="delete existing rows for cluster_uuid before writing")
    parser.add_argument("--batch-size", type=int, default=1000)
    args = parser.parse_args()

    run_dir = Path(args.run_dir).resolve()
    if not run_dir.exists():
        print(f"[ERROR] run_dir does not exist: {run_dir}", file=sys.stderr)
        sys.exit(1)

    manifest = _read_json(run_dir / "manifest.json")
    cluster_uuid = args.cluster_uuid or manifest.get("run_id") or run_dir.name
    metrics, node_info, warnings = build_green_node_metrics(run_dir, cluster_uuid, args.counter_mode, args.node_power_fallback)
    summary = _summarize(metrics, node_info, warnings, cluster_uuid)

    if args.output_dir:
        output_dir = Path(args.output_dir).resolve()
        output_dir.mkdir(parents=True, exist_ok=True)
        metrics.to_csv(output_dir / "green_node_metrics_preview.csv", index=False)
        node_info.to_csv(output_dir / "green_node_info_preview.csv", index=False)
        summary["preview_dir"] = str(output_dir)

    if args.write:
        conn = _connect()
        try:
            ensure_schema(conn)
            if args.replace:
                with conn.cursor() as cur:
                    cur.execute("DELETE FROM green_node_metrics WHERE cluster_uuid = %s", (cluster_uuid,))
                    cur.execute("DELETE FROM green_node_info WHERE cluster_uuid = %s", (cluster_uuid,))
            elif metrics["cluster_uuid"].nunique() == 1:
                warnings.append("write without --replace may create duplicate green_node_metrics rows")
            info_rows = _upsert_node_info(conn, node_info, args.batch_size)
            metric_rows = _insert_rows(conn, "green_node_metrics", metrics, GREEN_NODE_METRICS_COLUMNS, args.batch_size)
            conn.commit()
            summary["written"] = True
            summary["written_green_node_info_rows"] = info_rows
            summary["written_green_node_metrics_rows"] = metric_rows
        except Exception:
            conn.rollback()
            raise
        finally:
            conn.close()
    else:
        summary["written"] = False

    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
