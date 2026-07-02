"""Stage V0.1 SLO status classifier.

This script provides the machine-learning half of the V0.1 hybrid design:
rule labels are generated from SLO thresholds, then a LightGBM classifier learns
nonlinear combinations of traffic, latency, error rate, replicas, and node load.
"""
import argparse
import json
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Dict, List, Tuple

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
    "qps",
    "p95_latency",
    "p99_latency",
    "error_rate",
    "replica_count",
    "cpu_util",
    "gpu_util",
    "node_power",
    "qps_per_replica",
    "p99_ratio",
    "error_ratio",
    "resource_pressure",
]
LABELS = ["NORMAL", "WARNING", "VIOLATED"]
MODEL_PATH = config.CHECKPOINTS_DIR / "slo_v01_lgbm.pkl"
METADATA_PATH = config.CHECKPOINTS_DIR / "slo_v01_metadata.json"
SCHEMA_PATHS = [
    config.BASE_DIR.parent / "sql" / "slo_v01.sql",
    config.BASE_DIR.parent / "sql" / "slo_v02.sql",
    config.BASE_DIR.parent / "sql" / "slo_v03.sql",
]


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


def _risk_score(row: pd.Series) -> float:
    p99_target = float(row.get("p99_latency_target") or 500.0)
    err_target = float(row.get("error_rate_target") or 0.1)
    p99_ratio = float(row.get("p99_latency", 0.0)) / max(p99_target, 1e-9)
    err_ratio = float(row.get("error_rate", 0.0)) / max(err_target, 1e-9)
    resource_pressure = max(float(row.get("cpu_util", 0.0)), float(row.get("gpu_util", 0.0))) / 100.0
    qps_per_replica = float(row.get("qps", 0.0)) / max(float(row.get("replica_count", 1.0)), 1.0)
    qps_pressure = qps_per_replica / 1200.0
    power_pressure = float(row.get("node_power", 0.0)) / 15.0
    return float(np.clip(
        0.42 * p99_ratio
        + 0.28 * err_ratio
        + 0.12 * qps_pressure
        + 0.10 * resource_pressure
        + 0.08 * power_pressure,
        0,
        1,
    ))


def _rule_label(row: pd.Series) -> str:
    p99_target = float(row.get("p99_latency_target") or 500.0)
    err_target = float(row.get("error_rate_target") or 0.1)
    p99 = float(row.get("p99_latency", 0.0))
    err = float(row.get("error_rate", 0.0))
    risk = _risk_score(row)
    if p99 >= p99_target or err >= err_target:
        return "VIOLATED"
    if p99 >= p99_target * 0.8 or err >= err_target * 0.8 or risk >= 0.7:
        return "WARNING"
    return "NORMAL"


def _prepare_features(df: pd.DataFrame) -> pd.DataFrame:
    frame = df.copy()
    for col in [
        "qps",
        "p95_latency",
        "p99_latency",
        "error_rate",
        "replica_count",
        "cpu_util",
        "gpu_util",
        "node_power",
        "p99_latency_target",
        "error_rate_target",
    ]:
        if col not in frame.columns:
            frame[col] = 0.0
        frame[col] = pd.to_numeric(frame[col], errors="coerce").fillna(0.0)
    frame["replica_count"] = frame["replica_count"].clip(lower=1)
    frame["qps_per_replica"] = frame["qps"] / frame["replica_count"]
    frame["p99_ratio"] = frame["p99_latency"] / frame["p99_latency_target"].replace(0, 500)
    frame["error_ratio"] = frame["error_rate"] / frame["error_rate_target"].replace(0, 0.1)
    frame["resource_pressure"] = frame[["cpu_util", "gpu_util"]].max(axis=1) / 100.0
    return frame[FEATURE_COLUMNS].replace([np.inf, -np.inf], 0.0).fillna(0.0)


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
    sql += " ORDER BY metric_time ASC"
    if limit:
        sql += " LIMIT %s"
        params.append(limit)
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.execute(sql, params)
            rows = cur.fetchall()
    return pd.DataFrame(rows)


def train(start_time: str | None, end_time: str | None) -> Dict[str, object]:
    try:
        from lightgbm import LGBMClassifier
    except ModuleNotFoundError as exc:
        raise RuntimeError("lightgbm is required: python -m pip install lightgbm") from exc

    df = load_metrics(start_time, end_time)
    if df.empty:
        raise RuntimeError("No rows found in slo_metric_ts")

    X = _prepare_features(df)
    y_text = df.apply(_rule_label, axis=1)
    y = y_text.map({label: idx for idx, label in enumerate(LABELS)}).astype(int)
    model = LGBMClassifier(
        objective="multiclass",
        num_class=len(LABELS),
        n_estimators=120,
        learning_rate=0.05,
        max_depth=-1,
        min_child_samples=1,
        min_data_in_bin=1,
        verbose=-1,
        random_state=42,
    )
    model.fit(X, y)
    joblib.dump(model, MODEL_PATH)

    metadata = {
        "model_name": "slo_v01_lgbm",
        "model_version": "slo_v01_lgbm_baseline",
        "created_at": datetime.now().isoformat(),
        "feature_columns": FEATURE_COLUMNS,
        "labels": LABELS,
        "train_rows": int(len(df)),
        "label_distribution": y_text.value_counts().to_dict(),
        "model_file": str(MODEL_PATH),
    }
    METADATA_PATH.write_text(json.dumps(metadata, indent=2, ensure_ascii=False))
    return metadata


def seed_demo(hours: int, interval_minutes: int, cluster_uuid: str, write_status: bool) -> Dict[str, object]:
    ensure_schema()
    now = datetime.now().replace(second=0, microsecond=0)
    start = now - timedelta(hours=max(hours, 1))
    interval = max(interval_minutes, 1)
    profiles = [
        ("api-gw", "API 网关", "/api/v1/orders", 0.46, 48.0, 0.68, 0.04),
        ("model-inference", "模型推理", "/api/v1/infer", 0.22, 180.0, 0.42, 1.28),
        ("data-process", "数据处理", "/api/v1/features", 0.32, 78.0, 0.62, 0.22),
    ]
    rows: List[Dict[str, object]] = []
    step_count = int(hours * 60 / interval)
    for step in range(max(step_count, 1)):
        ts = start + timedelta(minutes=step * interval)
        h = ts.hour + ts.minute / 60
        daily = np.sin(2 * np.pi * h / 24)
        work_boost = 1.0 if 9 <= ts.hour <= 20 else 0.35
        base_qps = 4200 + daily * 900 + work_boost * 1300 + np.random.default_rng(step).normal(0, 180)
        cpu = float(np.clip(52 + daily * 8 + work_boost * 14 + np.random.default_rng(step + 7).normal(0, 4), 15, 96))
        gpu = float(np.clip(45 + daily * 7 + work_boost * 18 + np.random.default_rng(step + 17).normal(0, 5), 8, 98))
        node_power = float(np.clip(5.5 + cpu / 100 * 3.0 + gpu / 100 * 4.8, 2, 15))

        for idx, (service_id, service_name, api_id, share, base_latency, cpu_w, gpu_w) in enumerate(profiles):
            replicas = 2 + (step + idx) % 4
            qps = max(base_qps * share, 1)
            qps_per_replica = qps / replicas
            pressure = qps_per_replica / 1200
            p95 = base_latency + cpu * cpu_w + gpu * gpu_w + pressure * 75
            p99 = p95 * 1.28 + pressure * 70 + idx * 8
            error_rate = 0.012 + max(0, pressure - 0.55) * 0.08 + max(0, cpu - 75) * 0.0035 + max(0, gpu - 82) * 0.004
            rows.append({
                "cluster_uuid": cluster_uuid,
                "service_id": service_id,
                "service_name": service_name,
                "api_id": api_id,
                "metric_time": ts.strftime("%Y-%m-%d %H:%M:%S"),
                "qps": round(float(qps), 2),
                "p95_latency": round(float(np.clip(p95, 1, 2000)), 2),
                "p99_latency": round(float(np.clip(p99, 1, 3000)), 2),
                "error_rate": round(float(np.clip(error_rate, 0, 10)), 4),
                "replica_count": replicas,
                "cpu_util": round(cpu, 2),
                "gpu_util": round(gpu, 2),
                "node_power": round(node_power, 2),
                "p99_latency_target": 500.0,
                "error_rate_target": 0.1,
            })

    sql = """
INSERT INTO slo_metric_ts
  (cluster_uuid, service_id, service_name, api_id, metric_time,
   qps, p95_latency, p99_latency, error_rate, replica_count,
   cpu_util, gpu_util, node_power, p99_latency_target, error_rate_target)
VALUES
  (%(cluster_uuid)s, %(service_id)s, %(service_name)s, %(api_id)s, %(metric_time)s,
   %(qps)s, %(p95_latency)s, %(p99_latency)s, %(error_rate)s, %(replica_count)s,
   %(cpu_util)s, %(gpu_util)s, %(node_power)s, %(p99_latency_target)s, %(error_rate_target)s)
ON DUPLICATE KEY UPDATE
  qps = VALUES(qps),
  p95_latency = VALUES(p95_latency),
  p99_latency = VALUES(p99_latency),
  error_rate = VALUES(error_rate),
  replica_count = VALUES(replica_count),
  cpu_util = VALUES(cpu_util),
  gpu_util = VALUES(gpu_util),
  node_power = VALUES(node_power)
"""
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.executemany(sql, rows)
        conn.commit()

    if write_status:
        score_latest(limit=len(rows), write_status=True)
    return {
        "inserted_or_updated": len(rows),
        "hours": hours,
        "interval_minutes": interval,
        "cluster_uuid": cluster_uuid,
        "wrote_status": write_status,
    }


def _status_reason(row: pd.Series, status: str, risk: float) -> str:
    reasons = []
    if row["p99_ratio"] >= 1:
        reasons.append("P99 latency exceeds target")
    elif row["p99_ratio"] >= 0.8:
        reasons.append("P99 latency is close to target")
    if row["error_ratio"] >= 1:
        reasons.append("error rate exceeds target")
    elif row["error_ratio"] >= 0.8:
        reasons.append("error rate is close to target")
    if risk >= 0.7 and status == "WARNING":
        reasons.append("combined traffic/resource pressure is high")
    return "; ".join(reasons) or "all core indicators are within SLO thresholds"


def score_latest(limit: int, write_status: bool) -> Dict[str, object]:
    df = load_metrics(None, None, limit=limit)
    if df.empty:
        raise RuntimeError("No rows found in slo_metric_ts")
    X = _prepare_features(df)

    if MODEL_PATH.exists():
        model = joblib.load(MODEL_PATH)
        proba = model.predict_proba(X)
        label_idx = np.argmax(proba, axis=1)
        labels = [LABELS[int(idx)] for idx in label_idx]
        risks = np.clip(proba[:, 1] + proba[:, 2], 0, 1)
        mode = "lightgbm"
    else:
        labels = df.apply(_rule_label, axis=1).tolist()
        risks = df.apply(_risk_score, axis=1).to_numpy()
        mode = "rules"

    records = []
    for i, (_, row) in enumerate(df.iterrows()):
        feature_row = X.iloc[i]
        record = {
            "cluster_uuid": row.get("cluster_uuid", ""),
            "service_id": row.get("service_id", ""),
            "service_name": row.get("service_name", ""),
            "api_id": row.get("api_id", ""),
            "status_time": str(row.get("metric_time")),
            "slo_status": labels[i],
            "violation_risk": round(float(risks[i]), 4),
            "reason": _status_reason(feature_row, labels[i], float(risks[i])),
            "qps": float(row.get("qps", 0.0)),
            "p95_latency": float(row.get("p95_latency", 0.0)),
            "p99_latency": float(row.get("p99_latency", 0.0)),
            "error_rate": float(row.get("error_rate", 0.0)),
            "replica_count": int(row.get("replica_count", 0)),
            "cpu_util": float(row.get("cpu_util", 0.0)),
            "gpu_util": float(row.get("gpu_util", 0.0)),
            "node_power": float(row.get("node_power", 0.0)),
        }
        records.append(record)

    if write_status:
        upsert_status(records)
    return {"mode": mode, "count": len(records), "records": records}


def upsert_status(records: List[Dict[str, object]]) -> None:
    ensure_schema()
    sql = """
INSERT INTO slo_status
  (cluster_uuid, service_id, service_name, api_id, status_time,
   slo_status, violation_risk, reason,
   qps, p95_latency, p99_latency, error_rate, replica_count,
   cpu_util, gpu_util, node_power)
VALUES
  (%(cluster_uuid)s, %(service_id)s, %(service_name)s, %(api_id)s, %(status_time)s,
   %(slo_status)s, %(violation_risk)s, %(reason)s,
   %(qps)s, %(p95_latency)s, %(p99_latency)s, %(error_rate)s, %(replica_count)s,
   %(cpu_util)s, %(gpu_util)s, %(node_power)s)
ON DUPLICATE KEY UPDATE
  slo_status = VALUES(slo_status),
  violation_risk = VALUES(violation_risk),
  reason = VALUES(reason),
  qps = VALUES(qps),
  p95_latency = VALUES(p95_latency),
  p99_latency = VALUES(p99_latency),
  error_rate = VALUES(error_rate),
  replica_count = VALUES(replica_count),
  cpu_util = VALUES(cpu_util),
  gpu_util = VALUES(gpu_util),
  node_power = VALUES(node_power)
"""
    with _connect() as conn:
        with conn.cursor() as cur:
            cur.executemany(sql, records)
        conn.commit()


def main():
    parser = argparse.ArgumentParser(description="Train or score the V0.1 SLO classifier")
    sub = parser.add_subparsers(dest="cmd", required=True)

    train_parser = sub.add_parser("train")
    train_parser.add_argument("--start-time", default=None)
    train_parser.add_argument("--end-time", default=None)

    seed_parser = sub.add_parser("seed-demo")
    seed_parser.add_argument("--hours", type=int, default=48)
    seed_parser.add_argument("--interval-minutes", type=int, default=5)
    seed_parser.add_argument("--cluster-uuid", default="11111111-1111-1111-1111-111111111111")
    seed_parser.add_argument("--write-status", action="store_true")

    score_parser = sub.add_parser("score")
    score_parser.add_argument("--limit", type=int, default=100)
    score_parser.add_argument("--write-status", action="store_true")

    args = parser.parse_args()
    if args.cmd == "train":
        result = train(args.start_time, args.end_time)
    elif args.cmd == "seed-demo":
        result = seed_demo(args.hours, args.interval_minutes, args.cluster_uuid, args.write_status)
    else:
        result = score_latest(args.limit, args.write_status)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
