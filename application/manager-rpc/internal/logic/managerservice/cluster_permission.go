package managerservicelogic

import (
	"context"
	"errors"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/model"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
)

func getClusterCurrentUserContext(ctx context.Context) (string, []string, uint64) {
	username := ""
	if ctxUsername, ok := ctx.Value("username").(string); ok {
		username = strings.TrimSpace(ctxUsername)
	}

	roles := []string{}
	if ctxRoles, ok := ctx.Value("roles").([]string); ok {
		roles = ctxRoles
	}

	userID := uint64(0)
	if ctxUserID, ok := ctx.Value("userId").(uint64); ok {
		userID = ctxUserID
	}

	return username, roles, userID
}

func buildClusterSyncContext(baseCtx context.Context, operator string) context.Context {
	username, roles, userID := getClusterCurrentUserContext(baseCtx)
	if trimmedOperator := strings.TrimSpace(operator); trimmedOperator != "" {
		username = trimmedOperator
	}

	syncCtx := context.Background()
	if trimmedUsername := strings.TrimSpace(username); trimmedUsername != "" {
		syncCtx = context.WithValue(syncCtx, "username", trimmedUsername)
	}
	if userID > 0 {
		syncCtx = context.WithValue(syncCtx, "userId", userID)
	}
	if len(roles) > 0 {
		syncCtx = context.WithValue(syncCtx, "roles", roles)
	}
	return syncCtx
}

func isClusterSuperAdmin(username string, roles []string) bool {
	if strings.EqualFold(strings.TrimSpace(username), "super_admin") {
		return true
	}
	for _, role := range roles {
		if strings.EqualFold(strings.TrimSpace(role), "super_admin") || strings.EqualFold(strings.TrimSpace(role), "SUPER_ADMIN") {
			return true
		}
	}
	return false
}

func buildClusterVisibilityClause(username string, userID uint64, enableAssignment bool) (string, []interface{}) {
	if !enableAssignment {
		return "(`created_by` = ?)", []interface{}{username}
	}

	if userID > 0 {
		clause := `(
			created_by = ?
			OR EXISTS (
				SELECT 1
				FROM onec_cluster_member ocm
				WHERE ocm.cluster_uuid = onec_cluster.uuid
					AND ocm.is_deleted = 0
					AND ocm.user_id = ?
			)
		)`
		return clause, []interface{}{username, userID}
	}

	clause := `(
		created_by = ?
		OR EXISTS (
			SELECT 1
			FROM onec_cluster_member ocm
			JOIN sys_user su ON su.id = ocm.user_id
			WHERE ocm.cluster_uuid = onec_cluster.uuid
				AND ocm.is_deleted = 0
				AND su.is_deleted = 0
				AND su.username = ?
		)
	)`
	return clause, []interface{}{username, username}
}

func buildClusterVisibleQuery(baseQuery string, baseArgs []interface{}, username string, userID uint64, enableAssignment bool) (string, []interface{}) {
	query := strings.TrimSpace(baseQuery)
	args := make([]interface{}, 0, len(baseArgs)+2)
	args = append(args, baseArgs...)

	visibilityClause, visibilityArgs := buildClusterVisibilityClause(username, userID, enableAssignment)
	if query != "" {
		query += " AND "
	}
	query += visibilityClause
	args = append(args, visibilityArgs...)

	return query, args
}

func clusterMemberTableMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "onec_cluster_member") &&
		(strings.Contains(msg, "doesn't exist") || strings.Contains(msg, "1146"))
}

func canAccessClusterByID(ctx context.Context, svcCtx *svc.ServiceContext, clusterID uint64, username string, roles []string, userID uint64) (bool, error) {
	if isClusterSuperAdmin(username, roles) {
		return true, nil
	}
	if strings.TrimSpace(username) == "" {
		return false, nil
	}

	query, args := buildClusterVisibleQuery("`id` = ?", []interface{}{clusterID}, username, userID, true)
	clusters, err := svcCtx.OnecClusterModel.SearchNoPage(ctx, "id", true, query, args...)
	if err == nil {
		return len(clusters) > 0, nil
	}
	if errors.Is(err, model.ErrNotFound) {
		return false, nil
	}
	if !clusterMemberTableMissing(err) {
		return false, err
	}

	// 兼容数据库未升级（缺少 onec_cluster_member 表）时，退化到“仅创建者可见”。
	fallbackQuery, fallbackArgs := buildClusterVisibleQuery("`id` = ?", []interface{}{clusterID}, username, userID, false)
	fallbackClusters, fallbackErr := svcCtx.OnecClusterModel.SearchNoPage(ctx, "id", true, fallbackQuery, fallbackArgs...)
	if fallbackErr == nil {
		return len(fallbackClusters) > 0, nil
	}
	if errors.Is(fallbackErr, model.ErrNotFound) {
		return false, nil
	}
	return false, fallbackErr
}

func canAccessClusterByUUID(ctx context.Context, svcCtx *svc.ServiceContext, clusterUUID string, username string, roles []string, userID uint64) (bool, error) {
	if isClusterSuperAdmin(username, roles) {
		return true, nil
	}
	if strings.TrimSpace(username) == "" {
		return false, nil
	}

	query, args := buildClusterVisibleQuery("`uuid` = ?", []interface{}{clusterUUID}, username, userID, true)
	clusters, err := svcCtx.OnecClusterModel.SearchNoPage(ctx, "id", true, query, args...)
	if err == nil {
		return len(clusters) > 0, nil
	}
	if errors.Is(err, model.ErrNotFound) {
		return false, nil
	}
	if !clusterMemberTableMissing(err) {
		return false, err
	}

	// 兼容数据库未升级（缺少 onec_cluster_member 表）时，退化到“仅创建者可见”。
	fallbackQuery, fallbackArgs := buildClusterVisibleQuery("`uuid` = ?", []interface{}{clusterUUID}, username, userID, false)
	fallbackClusters, fallbackErr := svcCtx.OnecClusterModel.SearchNoPage(ctx, "id", true, fallbackQuery, fallbackArgs...)
	if fallbackErr == nil {
		return len(fallbackClusters) > 0, nil
	}
	if errors.Is(fallbackErr, model.ErrNotFound) {
		return false, nil
	}
	return false, fallbackErr
}
