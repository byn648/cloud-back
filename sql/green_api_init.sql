-- ============================================================
-- Green模块 API、菜单、角色关联初始化脚本
-- 执行方式: mysql -u root -p123456 kube_nova < sql/green_api_init.sql
-- ============================================================

START TRANSACTION;

-- ============================================================
-- 1. 插入 green 模块 API 记录
-- ============================================================

INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted)
VALUES (0, 'Green模块', '/green/v1', '*', 0, 'super_admin', 'super_admin', NOW(), NOW(), 0);

SET @green_parent_id = (SELECT LAST_INSERT_ID());

INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '能耗预测', '/green/v1/forecast', 'GET', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);
INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '能源预测', '/green/v1/forecast/energy', 'GET', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);
INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '数据采集', '/green/v1/forecast/collect', 'POST', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);
INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '性能SLO', '/green/v1/performance/slo', 'GET', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);
INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '性能Error-Budget', '/green/v1/performance/error-budget', 'GET', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);
INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '性能Resource', '/green/v1/performance/resource', 'GET', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);
INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '性能Power-Storage', '/green/v1/performance/power-storage', 'GET', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);
INSERT INTO sys_api (parent_id, name, path, method, is_permission, created_by, updated_by, created_at, updated_at, is_deleted) VALUES (@green_parent_id, '性能Service-Chain', '/green/v1/performance/service-chain', 'GET', 1, 'super_admin', 'super_admin', NOW(), NOW(), 0);

-- ============================================================
-- 2. 绑定 API 给超级管理员
-- ============================================================

INSERT INTO sys_role_api (role_id, api_id, created_at, is_deleted)
SELECT 1, id, NOW(), 0 FROM sys_api
WHERE parent_id = @green_parent_id AND is_permission = 1;

-- ============================================================
-- 3. 插入 Green 模块目录菜单 (集群概览页面)
-- ============================================================

INSERT INTO sys_menu (
    parent_id, platform_id, menu_type, name, path, component, redirect, label,
    title, icon, sort, link, is_enable, is_menu, keep_alive, is_hide, is_iframe,
    is_hide_tab, show_badge, show_text_badge, is_first_level, fixed_tab, is_full_page,
    active_path, roles, auth_name, auth_label, auth_icon, auth_sort, status, is_deleted,
    create_time, update_time, create_by, update_by
) VALUES (
    1, 1, 1, 'Green', '/green', '/dashboard/green/index', '', 'green',
    'Green模块', 'ri-leaf-line', 50, '', 1, 1, 0, 0, 0,
    0, 0, '', 0, 0, 0,
    '', NULL, '', '', '', 0, 1, 0,
    NOW(), NOW(), 'super_admin', 'super_admin'
);

SET @green_menu_id = (SELECT LAST_INSERT_ID());

-- ============================================================
-- 4. 插入 Green 模块子菜单 (预测分析)
-- ============================================================

INSERT INTO sys_menu (
    parent_id, platform_id, menu_type, name, path, component, redirect, label,
    title, icon, sort, link, is_enable, is_menu, keep_alive, is_hide, is_iframe,
    is_hide_tab, show_badge, show_text_badge, is_first_level, fixed_tab, is_full_page,
    active_path, roles, auth_name, auth_label, auth_icon, auth_sort, status, is_deleted,
    create_time, update_time, create_by, update_by
) VALUES (
    @green_menu_id, 1, 2, 'Forecast', '/green/forecast', '/dashboard/green/forecast/index', '', 'forecast',
    '预测分析', 'ri-line-chart-line', 1, '', 1, 1, 0, 0, 0,
    0, 0, '', 0, 0, 0,
    '', NULL, '', '', '', 1, 1, 0,
    NOW(), NOW(), 'super_admin', 'super_admin'
);

SET @forecast_menu_id = (SELECT LAST_INSERT_ID());

-- ============================================================
-- 5. 绑定菜单给超级管理员
-- ============================================================

INSERT INTO sys_role_menu (role_id, menu_id, created_at, is_deleted)
SELECT 1, id, NOW(), 0 FROM sys_menu
WHERE id IN (@green_menu_id, @forecast_menu_id);

-- ============================================================
-- 提交事务
-- ============================================================
COMMIT;

-- ============================================================
-- 验证结果
-- ============================================================
SELECT '=== Green 模块 API 记录 ===' AS '';
SELECT id, parent_id, name, path, method, is_permission FROM sys_api WHERE path LIKE '/green/v1%' OR path = '/green/v1';

SELECT '=== Green 模块菜单记录 ===' AS '';
SELECT id, parent_id, name, path, title, menu_type FROM sys_menu WHERE path LIKE '/green%' OR name = 'Green';
