-- 课题二 V0.1：SLO 监控与违约识别初始化表

CREATE TABLE IF NOT EXISTS slo_metric_ts (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  cluster_uuid VARCHAR(64) NOT NULL DEFAULT '',
  service_id VARCHAR(64) NOT NULL,
  service_name VARCHAR(64) NOT NULL DEFAULT '',
  api_id VARCHAR(64) NOT NULL DEFAULT '',
  metric_time DATETIME NOT NULL,
  qps DOUBLE NOT NULL DEFAULT 0,
  p95_latency DOUBLE NOT NULL DEFAULT 0,
  p99_latency DOUBLE NOT NULL DEFAULT 0,
  error_rate DOUBLE NOT NULL DEFAULT 0,
  replica_count INT NOT NULL DEFAULT 0,
  cpu_util DOUBLE NOT NULL DEFAULT 0,
  gpu_util DOUBLE NOT NULL DEFAULT 0,
  node_power DOUBLE NOT NULL DEFAULT 0,
  p99_latency_target DOUBLE NOT NULL DEFAULT 500,
  error_rate_target DOUBLE NOT NULL DEFAULT 0.1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_slo_metric_point (cluster_uuid, service_id, api_id, metric_time),
  INDEX idx_slo_metric_service_time (service_id, api_id, metric_time),
  INDEX idx_slo_metric_cluster_time (cluster_uuid, metric_time)
);

CREATE TABLE IF NOT EXISTS slo_status (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  cluster_uuid VARCHAR(64) NOT NULL DEFAULT '',
  service_id VARCHAR(64) NOT NULL,
  service_name VARCHAR(64) NOT NULL DEFAULT '',
  api_id VARCHAR(64) NOT NULL DEFAULT '',
  status_time DATETIME NOT NULL,
  slo_status VARCHAR(16) NOT NULL DEFAULT 'NORMAL',
  violation_risk DOUBLE NOT NULL DEFAULT 0,
  reason VARCHAR(512) NOT NULL DEFAULT '',
  qps DOUBLE NOT NULL DEFAULT 0,
  p95_latency DOUBLE NOT NULL DEFAULT 0,
  p99_latency DOUBLE NOT NULL DEFAULT 0,
  error_rate DOUBLE NOT NULL DEFAULT 0,
  replica_count INT NOT NULL DEFAULT 0,
  cpu_util DOUBLE NOT NULL DEFAULT 0,
  gpu_util DOUBLE NOT NULL DEFAULT 0,
  node_power DOUBLE NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_slo_status_point (cluster_uuid, service_id, api_id, status_time),
  INDEX idx_slo_status_service_time (service_id, api_id, status_time),
  INDEX idx_slo_status_cluster_time (cluster_uuid, status_time),
  INDEX idx_slo_status_state (slo_status)
);
