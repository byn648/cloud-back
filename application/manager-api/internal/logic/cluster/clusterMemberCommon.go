package cluster

import (
	"context"
	"strings"

	"github.com/yanshicheng/kube-nova/common/handler/errorx"
)

func getClusterMemberCurrentUser(ctx context.Context) (string, []string) {
	username := ""
	if rawUsername, ok := ctx.Value("username").(string); ok {
		username = strings.TrimSpace(rawUsername)
	}

	roles := []string{}
	if rawRoles, ok := ctx.Value("roles").([]string); ok {
		roles = rawRoles
	}
	return username, roles
}

func isClusterMemberSuperAdmin(username string, roles []string) bool {
	if strings.EqualFold(strings.TrimSpace(username), "super_admin") {
		return true
	}
	for _, role := range roles {
		normalized := strings.TrimSpace(role)
		if strings.EqualFold(normalized, "super_admin") || strings.EqualFold(normalized, "SUPER_ADMIN") {
			return true
		}
	}
	return false
}

func requireClusterMemberSuperAdmin(ctx context.Context) (string, error) {
	username, roles := getClusterMemberCurrentUser(ctx)
	if !isClusterMemberSuperAdmin(username, roles) {
		return "", errorx.New(100999, "无权限访问!")
	}
	if username == "" {
		username = "super_admin"
	}
	return username, nil
}

func uniquePositiveUserIDs(userIDs []uint64) []uint64 {
	if len(userIDs) == 0 {
		return []uint64{}
	}
	seen := make(map[uint64]struct{}, len(userIDs))
	result := make([]uint64, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		result = append(result, userID)
	}
	return result
}

func isClusterMemberTableMissingErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "onec_cluster_member") &&
		(strings.Contains(msg, "doesn't exist") || strings.Contains(msg, "1146"))
}
