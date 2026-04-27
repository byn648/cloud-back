START TRANSACTION;

-- Minimal seed data for scope-based visibility testing.
-- Test password (plain): Cloud@20260424
-- bcrypt hash generated locally.

INSERT INTO sys_user (
  username, password, nickname, avatar, email, phone, work_number,
  dept_id, status, is_need_reset_pwd, created_by, updated_by, is_deleted
) VALUES
  (
    'scope_user_a_20260424',
    '$2a$10$Xk0QsErYFv/6S.RU2JGwfOmgqswXjA/68fIUFpHj/r6p4KWKLGsHS',
    'Scope User A',
    '/public/default-avatar.png',
    'scope_user_a_20260424@example.local',
    '13900002401',
    'U2026042401',
    0, 1, 0, 'super_admin', 'super_admin', 0
  ),
  (
    'scope_user_b_20260424',
    '$2a$10$Xk0QsErYFv/6S.RU2JGwfOmgqswXjA/68fIUFpHj/r6p4KWKLGsHS',
    'Scope User B',
    '/public/default-avatar.png',
    'scope_user_b_20260424@example.local',
    '13900002402',
    'U2026042402',
    0, 1, 0, 'super_admin', 'super_admin', 0
  )
ON DUPLICATE KEY UPDATE
  password = VALUES(password),
  nickname = VALUES(nickname),
  email = VALUES(email),
  phone = VALUES(phone),
  work_number = VALUES(work_number),
  status = 1,
  is_need_reset_pwd = 0,
  is_deleted = 0,
  updated_by = 'super_admin',
  updated_at = NOW();

-- Bind users to role code project_admin.
INSERT INTO sys_user_role (user_id, role_id, created_at, is_deleted)
SELECT u.id, r.id, NOW(), 0
FROM sys_user u
JOIN sys_role r ON r.code = 'project_admin' AND r.is_deleted = 0
WHERE u.username IN ('scope_user_a_20260424', 'scope_user_b_20260424')
  AND u.is_deleted = 0
ON DUPLICATE KEY UPDATE
  is_deleted = 0;

-- Bind users to container platform (platform_id=1).
INSERT INTO sys_user_platform (
  user_id, platform_id, is_enable, status, is_deleted, create_by, update_by
)
SELECT u.id, 1, 1, 1, 0, 'super_admin', 'super_admin'
FROM sys_user u
WHERE u.username IN ('scope_user_a_20260424', 'scope_user_b_20260424')
  AND u.is_deleted = 0
ON DUPLICATE KEY UPDATE
  is_enable = 1,
  status = 1,
  is_deleted = 0,
  update_by = 'super_admin',
  update_time = NOW();

-- Sample clusters.
INSERT INTO onec_cluster (
  name, avatar, uuid, description, cluster_type, environment,
  region, zone, datacenter, provider, is_managed,
  node_lb, master_lb, ingress_domain,
  status, health_status, last_sync_at, version, git_commit, platform,
  version_build_at, cluster_created_at,
  cost_center, business_unit, owner_team, owner_email,
  priority, created_by, updated_by, is_deleted
)
SELECT
  'scope-cluster-a',
  '/public/kube-nova.png',
  '11111111-1111-4111-8111-111111111111',
  'scope test cluster A',
  'standard',
  'development',
  '', '', '', 'self-hosted', 0,
  '', '', '',
  3, 1, NOW(), 'v1.29.0', '', 'linux/amd64',
  NOW(), NOW(),
  '', '', '', '',
  0, 'scope_user_a_20260424', 'scope_user_a_20260424', 0
WHERE NOT EXISTS (
  SELECT 1 FROM onec_cluster WHERE uuid = '11111111-1111-4111-8111-111111111111'
);

INSERT INTO onec_cluster (
  name, avatar, uuid, description, cluster_type, environment,
  region, zone, datacenter, provider, is_managed,
  node_lb, master_lb, ingress_domain,
  status, health_status, last_sync_at, version, git_commit, platform,
  version_build_at, cluster_created_at,
  cost_center, business_unit, owner_team, owner_email,
  priority, created_by, updated_by, is_deleted
)
SELECT
  'scope-cluster-b',
  '/public/kube-nova.png',
  '22222222-2222-4222-8222-222222222222',
  'scope test cluster B',
  'standard',
  'testing',
  '', '', '', 'self-hosted', 0,
  '', '', '',
  3, 1, NOW(), 'v1.29.0', '', 'linux/amd64',
  NOW(), NOW(),
  '', '', '', '',
  0, 'scope_user_b_20260424', 'scope_user_b_20260424', 0
WHERE NOT EXISTS (
  SELECT 1 FROM onec_cluster WHERE uuid = '22222222-2222-4222-8222-222222222222'
);

INSERT INTO onec_cluster (
  name, avatar, uuid, description, cluster_type, environment,
  region, zone, datacenter, provider, is_managed,
  node_lb, master_lb, ingress_domain,
  status, health_status, last_sync_at, version, git_commit, platform,
  version_build_at, cluster_created_at,
  cost_center, business_unit, owner_team, owner_email,
  priority, created_by, updated_by, is_deleted
)
SELECT
  'scope-cluster-super',
  '/public/kube-nova.png',
  '33333333-3333-4333-8333-333333333333',
  'scope test cluster super',
  'standard',
  'production',
  '', '', '', 'self-hosted', 0,
  '', '', '',
  3, 1, NOW(), 'v1.29.0', '', 'linux/amd64',
  NOW(), NOW(),
  '', '', '', '',
  0, 'super_admin', 'super_admin', 0
WHERE NOT EXISTS (
  SELECT 1 FROM onec_cluster WHERE uuid = '33333333-3333-4333-8333-333333333333'
);

-- Cluster member assignments.
-- 默认不做“跨用户分配”，避免测试时出现“集群不属于我但仍可见”的误解。
-- 需要测试“超级管理员分配可见集群”时，可按需执行下面示例：
--
-- INSERT INTO onec_cluster_member (cluster_uuid, user_id, created_by, is_deleted)
-- SELECT '11111111-1111-4111-8111-111111111111', u.id, 'super_admin', 0
-- FROM sys_user u
-- WHERE u.username = 'scope_user_b_20260424' AND u.is_deleted = 0
-- ON DUPLICATE KEY UPDATE
--   is_deleted = 0,
--   created_by = VALUES(created_by);
--
-- INSERT INTO onec_cluster_member (cluster_uuid, user_id, created_by, is_deleted)
-- SELECT '33333333-3333-4333-8333-333333333333', u.id, 'super_admin', 0
-- FROM sys_user u
-- WHERE u.username = 'scope_user_a_20260424' AND u.is_deleted = 0
-- ON DUPLICATE KEY UPDATE
--   is_deleted = 0,
--   created_by = VALUES(created_by);

COMMIT;
