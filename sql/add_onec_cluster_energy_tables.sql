-- Creates the cluster energy profile table used by scheduling.
SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

DROP TABLE IF EXISTS `onec_cluster_node_energy`;

CREATE TABLE IF NOT EXISTS `onec_cluster_energy_profile` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT 'primary key',
  `cluster_uuid` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'onec_cluster.uuid, one row per cluster',
  `grid_price_per_kwh` decimal(12,6) NOT NULL DEFAULT '0.000000' COMMENT 'electricity price, CNY/kWh',
  `storage_soc` decimal(5,2) DEFAULT NULL COMMENT 'storage SOC, 0-100, NULL means not collected',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT 'updated time',
  `is_deleted` tinyint(1) NOT NULL DEFAULT '0' COMMENT 'soft delete flag',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_cluster_uuid` (`cluster_uuid`),
  KEY `idx_is_deleted` (`is_deleted`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='cluster electricity and storage SOC profile';

SET FOREIGN_KEY_CHECKS = 1;
