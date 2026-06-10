SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

CREATE TABLE IF NOT EXISTS `onec_schedule_plan` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '自增主键',
  `plan_id` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '调度计划ID',
  `project_id` bigint unsigned NOT NULL DEFAULT '0' COMMENT '项目ID',
  `workspace_id` bigint unsigned NOT NULL DEFAULT '0' COMMENT '用户提交的逻辑工作空间绑定ID',
  `target_workspace_id` bigint unsigned NOT NULL DEFAULT '0' COMMENT '算法选择的目标工作空间绑定ID',
  `target_project_cluster_id` bigint unsigned NOT NULL DEFAULT '0' COMMENT '算法选择的项目-集群绑定ID',
  `target_cluster_uuid` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '算法选择的目标集群UUID',
  `namespace` varchar(63) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '目标Kubernetes命名空间',
  `service_name` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '服务名称',
  `resource_type` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '资源类型',
  `resource_name` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'Kubernetes资源名称',
  `model_version` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '调度算法版本',
  `cluster_snapshot_json` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci COMMENT '跨集群调度输入状态快照JSON',
  `node_snapshot_json` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci COMMENT '集群内调度输入状态快照JSON',
  `placements_json` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci COMMENT '副本到节点映射JSON',
  `plan_json` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci COMMENT '完整调度计划JSON',
  `executable` tinyint(1) NOT NULL DEFAULT '0' COMMENT '计划是否可执行',
  `status` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'PLANNED' COMMENT 'PLANNED/EXECUTING/SUCCEEDED/FAILED',
  `reason` varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '调度原因或不可调度原因',
  `execute_message` text CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci COMMENT '执行结果说明',
  `created_by` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '创建人',
  `updated_by` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '更新人',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  `is_deleted` tinyint(1) NOT NULL DEFAULT '0' COMMENT '软删除',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_plan_id` (`plan_id`),
  KEY `idx_workspace_id` (`workspace_id`),
  KEY `idx_target_workspace_id` (`target_workspace_id`),
  KEY `idx_target_cluster_uuid` (`target_cluster_uuid`),
  KEY `idx_status` (`status`),
  KEY `idx_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='电-储-算微服务调度计划表';

SET FOREIGN_KEY_CHECKS = 1;
