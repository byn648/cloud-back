-- ============================================
-- 节点级负荷预测结果表
-- 插入时使用 upsert 避免重复
-- ============================================

CREATE TABLE IF NOT EXISTS green_model_prediction (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  cluster_uuid VARCHAR(64) NOT NULL DEFAULT '' COMMENT '集群UUID',
  node_uuid VARCHAR(64) NOT NULL DEFAULT '' COMMENT '节点UUID',
  target_metric VARCHAR(32) NOT NULL DEFAULT '' COMMENT '目标指标: cpu_util / gpu_util / gpu_mem_util / node_power',
  forecast_time DATETIME NOT NULL COMMENT '预测时间点',
  horizon INT NOT NULL DEFAULT 0 COMMENT '预测步长（分钟）',
  y_pred DOUBLE NOT NULL DEFAULT 0 COMMENT '预测值',
  risk_level VARCHAR(16) NOT NULL DEFAULT 'LOW' COMMENT '风险等级: LOW / MEDIUM / HIGH',
  model_name VARCHAR(32) NOT NULL DEFAULT 'lightgbm' COMMENT '模型名称',
  model_version VARCHAR(32) NOT NULL DEFAULT '' COMMENT '模型版本',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '记录创建时间',
  UNIQUE KEY uk_prediction (cluster_uuid, node_uuid, target_metric, forecast_time, horizon, model_version),
  INDEX idx_node_forecast (node_uuid, forecast_time),
  INDEX idx_risk_level (risk_level)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='节点级负荷预测结果表';

-- 执行方式: mysql -u root -p123456 cloud_back < sql/green_model_prediction.sql
