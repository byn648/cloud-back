-- 课题二 V0.2：端到端 SLO 分解为服务级 SLO budget

CREATE TABLE IF NOT EXISTS slo_e2e_objective (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  objective_id VARCHAR(64) NOT NULL,
  cluster_uuid VARCHAR(64) NOT NULL DEFAULT '',
  business_flow VARCHAR(128) NOT NULL DEFAULT '',
  target_p99_latency DOUBLE NOT NULL DEFAULT 1000,
  target_error_rate DOUBLE NOT NULL DEFAULT 0.1,
  allocation_method VARCHAR(32) NOT NULL DEFAULT 'risk_weighted',
  is_active TINYINT NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_slo_e2e_objective (objective_id),
  INDEX idx_slo_e2e_cluster_active (cluster_uuid, is_active)
);

CREATE TABLE IF NOT EXISTS slo_service_dependency (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  objective_id VARCHAR(64) NOT NULL,
  upstream_service_id VARCHAR(64) NOT NULL DEFAULT '',
  downstream_service_id VARCHAR(64) NOT NULL DEFAULT '',
  service_id VARCHAR(64) NOT NULL,
  service_name VARCHAR(64) NOT NULL DEFAULT '',
  api_id VARCHAR(64) NOT NULL DEFAULT '',
  is_critical TINYINT NOT NULL DEFAULT 0,
  path_order INT NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_slo_dependency_node (objective_id, service_id, api_id),
  INDEX idx_slo_dependency_objective (objective_id, path_order)
);

CREATE TABLE IF NOT EXISTS slo_budget_allocation (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  objective_id VARCHAR(64) NOT NULL,
  cluster_uuid VARCHAR(64) NOT NULL DEFAULT '',
  business_flow VARCHAR(128) NOT NULL DEFAULT '',
  service_id VARCHAR(64) NOT NULL,
  service_name VARCHAR(64) NOT NULL DEFAULT '',
  api_id VARCHAR(64) NOT NULL DEFAULT '',
  allocation_method VARCHAR(32) NOT NULL DEFAULT 'risk_weighted',
  latency_contribution DOUBLE NOT NULL DEFAULT 0,
  qps DOUBLE NOT NULL DEFAULT 0,
  qps_cv DOUBLE NOT NULL DEFAULT 0,
  error_rate DOUBLE NOT NULL DEFAULT 0,
  critical_path TINYINT NOT NULL DEFAULT 0,
  risk_weight DOUBLE NOT NULL DEFAULT 1,
  budget_ratio DOUBLE NOT NULL DEFAULT 0,
  p99_latency_budget DOUBLE NOT NULL DEFAULT 0,
  error_rate_budget DOUBLE NOT NULL DEFAULT 0,
  allocated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_slo_budget_allocation (objective_id, service_id, api_id, allocation_method),
  INDEX idx_slo_budget_objective (objective_id, allocation_method),
  INDEX idx_slo_budget_cluster_time (cluster_uuid, allocated_at)
);

INSERT INTO slo_e2e_objective
  (objective_id, cluster_uuid, business_flow, target_p99_latency, target_error_rate, allocation_method, is_active)
VALUES
  ('checkout-flow', '11111111-1111-1111-1111-111111111111', '下单流程', 1000, 0.1, 'risk_weighted', 1)
ON DUPLICATE KEY UPDATE
  target_p99_latency = VALUES(target_p99_latency),
  target_error_rate = VALUES(target_error_rate),
  allocation_method = VALUES(allocation_method),
  is_active = VALUES(is_active);

INSERT INTO slo_service_dependency
  (objective_id, upstream_service_id, downstream_service_id, service_id, service_name, api_id, is_critical, path_order)
VALUES
  ('checkout-flow', '', 'model-inference', 'api-gw', 'API 网关', '/api/v1/orders', 1, 1),
  ('checkout-flow', 'api-gw', 'data-process', 'model-inference', '模型推理', '/api/v1/infer', 1, 2),
  ('checkout-flow', 'model-inference', '', 'data-process', '数据处理', '/api/v1/features', 0, 3)
ON DUPLICATE KEY UPDATE
  upstream_service_id = VALUES(upstream_service_id),
  downstream_service_id = VALUES(downstream_service_id),
  service_name = VALUES(service_name),
  is_critical = VALUES(is_critical),
  path_order = VALUES(path_order);
