"""Data loader for green_node_metrics.

Dynamically inspects table columns via SHOW COLUMNS, maps DB fields to canonical names,
and loads data for specified cluster/nodes within a time range.
"""
import sys
import argparse
from typing import Optional, List
from datetime import datetime

import pandas as pd
import pymysql

from . import config


FIELD_MAP = {
    "cpu_usage": "cpu_util",
    "gpu_usage": "gpu_util",
    "gpu_memory_usage": "gpu_mem_util",
    "memory_usage": "mem_util",
    "node_power": "node_power",
    "io_read": "disk_io_read",
    "io_write": "disk_io_write",
    "network_rx": "net_io_rx",
    "network_tx": "net_io_tx",
}

TARGET_COLS = ["cpu_util", "gpu_util", "gpu_mem_util", "node_power"]

METRIC_CONTEXT_FIELD_MAP = {
    "task_type": "task_type",
    "batch_size": "batch_size",
    "concurrency": "concurrency",
    "request_rate": "request_rate",
    "replica_count": "replicas",
    "schedule_policy": "schedule_policy",
    "requests": "requests",
    "limits": "limits",
}

NODE_INFO_COLUMNS = [
    "node_uuid",
    "rated_power",
    "cpu_model",
    "gpu_model",
    "gpu_count",
    "gpu_mem_gb",
    "node_type",
    "requests",
    "limits",
]


def get_available_columns(conn: pymysql.Connection) -> List[str]:
    with conn.cursor() as cur:
        cur.execute("SHOW COLUMNS FROM green_node_metrics")
        cols = [row["Field"] for row in cur.fetchall()]
    return cols


def get_available_columns_for_table(conn: pymysql.Connection, table_name: str) -> List[str]:
    with conn.cursor() as cur:
        cur.execute(f"SHOW COLUMNS FROM `{table_name}`")
        cols = [row["Field"] for row in cur.fetchall()]
    return cols


def load_node_info(conn: pymysql.Connection, cluster_uuid: str) -> pd.DataFrame:
    try:
        available = get_available_columns_for_table(conn, "green_node_info")
        select_cols = [col for col in NODE_INFO_COLUMNS if col in available]
        if "node_uuid" not in select_cols:
            return pd.DataFrame()
        if "cluster_uuid" in available:
            where_sql = " WHERE cluster_uuid = %s"
            params = (cluster_uuid,)
        else:
            where_sql = ""
            params = ()

        select_sql = ", ".join(f"`{col}`" for col in select_cols)
        with conn.cursor() as cur:
            cur.execute(f"SELECT {select_sql} FROM green_node_info{where_sql}", params)
            rows = cur.fetchall()
        if not rows:
            return pd.DataFrame()
        df = pd.DataFrame(rows)
        return df
    except pymysql.Error:
        print("[WARN] green_node_info table not found or query failed, skipping static info", file=sys.stderr)
        return pd.DataFrame()


def load_metrics(
    cluster_uuid: str,
    start_time: Optional[str] = None,
    end_time: Optional[str] = None,
    node_uuids: Optional[List[str]] = None,
) -> pd.DataFrame:
    conn = pymysql.connect(**config.DB_CONFIG)

    try:
        available = get_available_columns(conn)
    except pymysql.Error as e:
        conn.close()
        print(f"[WARN] Failed to get table columns: {e}", file=sys.stderr)
        return pd.DataFrame()

    select_parts = ["node_uuid", "cluster_uuid", "metric_time"]
    for db_col, canonical in FIELD_MAP.items():
        if db_col in available:
            select_parts.append(f"`{db_col}` AS `{canonical}`")
        else:
            print(f"[WARN] Column '{db_col}' not found in green_node_metrics, will use NaN", file=sys.stderr)

    for db_col, canonical in METRIC_CONTEXT_FIELD_MAP.items():
        if db_col in available:
            select_parts.append(f"`{db_col}` AS `{canonical}`")

    extra_cols = ["io_read", "io_write", "network_rx", "network_tx"]
    present_extra = [c for c in extra_cols if c in available]

    if "io_read" in present_extra and "io_write" in present_extra:
        select_parts.append("(io_read + io_write) AS `disk_io`")
    else:
        select_parts.append("NULL AS `disk_io`")

    if "network_rx" in present_extra and "network_tx" in present_extra:
        select_parts.append("(network_rx + network_tx) AS `net_io`")
    else:
        select_parts.append("NULL AS `net_io`")

    sql = f"SELECT {', '.join(select_parts)} FROM green_node_metrics WHERE cluster_uuid = %s"
    params: list = [cluster_uuid]

    if start_time:
        sql += " AND metric_time >= %s"
        params.append(start_time)
    if end_time:
        sql += " AND metric_time <= %s"
        params.append(end_time)
    if node_uuids:
        placeholders = ",".join(["%s"] * len(node_uuids))
        sql += f" AND node_uuid IN ({placeholders})"
        params.extend(node_uuids)

    sql += " ORDER BY node_uuid, metric_time"

    try:
        with conn.cursor() as cur:
            cur.execute(sql, params)
            rows = cur.fetchall()
            columns = [desc[0] for desc in cur.description] if cur.description else []
        df = pd.DataFrame(rows, columns=columns)
    except pymysql.Error as e:
        print(f"[ERROR] Failed to load metrics: {e}", file=sys.stderr)
        return pd.DataFrame()
    finally:
        conn.close()

    if df.empty:
        return df

    for col in TARGET_COLS + ["disk_io", "net_io"]:
        if col in df.columns:
            df[col] = pd.to_numeric(df[col], errors="coerce")

    node_conn = pymysql.connect(**config.DB_CONFIG)
    try:
        node_info = load_node_info(node_conn, cluster_uuid)
    finally:
        node_conn.close()
    if not node_info.empty:
        merge_cols = [col for col in NODE_INFO_COLUMNS if col in node_info.columns]
        static_info = node_info[merge_cols].drop_duplicates("node_uuid")
        overlapping = [col for col in static_info.columns if col != "node_uuid" and col in df.columns]
        if overlapping:
            static_info = static_info.rename(columns={col: f"{col}_node_info" for col in overlapping})
        df = df.merge(static_info, on="node_uuid", how="left")
        for col in overlapping:
            info_col = f"{col}_node_info"
            df[col] = df[col].combine_first(df[info_col])
            df = df.drop(columns=[info_col])

    return df


def main():
    parser = argparse.ArgumentParser(description="Load metrics from green_node_metrics")
    parser.add_argument("--cluster-uuid", required=True)
    parser.add_argument("--start-time", default=None)
    parser.add_argument("--end-time", default=None)
    parser.add_argument("--node-uuids", default=None, help="comma-separated node UUIDs")
    args = parser.parse_args()

    node_uuids = None
    if args.node_uuids:
        node_uuids = [n.strip() for n in args.node_uuids.split(",") if n.strip()]

    df = load_metrics(
        cluster_uuid=args.cluster_uuid,
        start_time=args.start_time,
        end_time=args.end_time,
        node_uuids=node_uuids,
    )

    if df.empty:
        print("[WARN] No data returned from green_node_metrics", file=sys.stderr)
        return

    print(f"[INFO] Loaded {len(df)} rows for {df['node_uuid'].nunique()} nodes", file=sys.stderr)
    print(df.to_json(orient="records", date_format="iso"))


if __name__ == "__main__":
    main()
