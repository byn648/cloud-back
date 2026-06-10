-- 为能源相关表补初始行（可多次执行，已存在的不会重复插入）
-- 在 Navicat 里选中本库后执行整段

SET NAMES utf8mb4;
-- 源表(如 onec_cluster_node) 可能为 utf8mb4_0900_ai_ci，本表为 utf8mb4_unicode_ci，
-- 比较时须统一规则，避免 1267 Illegal mix of collations

-- 1) 集群级：对 onec_cluster 中未删且 uuid 非空的记录，在 onec_cluster_energy_profile 中补一行
INSERT INTO onec_cluster_energy_profile (
  cluster_uuid,
  grid_price_per_kwh,
  storage_soc,
  is_deleted
)
SELECT
  c.uuid,
  0.000000,
  -- SOC is only a real-time storage/BMS metric. Keep NULL when no storage collector exists.
  NULL,
  0
FROM onec_cluster c
WHERE c.is_deleted = 0
  AND c.uuid IS NOT NULL
  AND c.uuid <> ''
  AND NOT EXISTS (
    SELECT 1
    FROM onec_cluster_energy_profile p
    WHERE p.cluster_uuid COLLATE utf8mb4_unicode_ci = c.uuid COLLATE utf8mb4_unicode_ci
  );

