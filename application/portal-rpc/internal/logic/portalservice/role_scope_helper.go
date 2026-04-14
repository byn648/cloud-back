package portalservicelogic

import (
	"context"
	"strings"

	"github.com/yanshicheng/kube-nova/application/portal-rpc/internal/model"
)

const superAdminRoleCode = "super_admin"

func currentUsername(ctx context.Context) string {
	username, _ := ctx.Value("username").(string)
	return strings.TrimSpace(username)
}

func hasSuperAdminRole(ctx context.Context) bool {
	roles, _ := ctx.Value("roles").([]string)
	for _, role := range roles {
		if strings.EqualFold(strings.TrimSpace(role), superAdminRoleCode) {
			return true
		}
	}
	return false
}

func canOperateRole(ctx context.Context, role *model.SysRole) bool {
	if role == nil {
		return false
	}
	if hasSuperAdminRole(ctx) {
		return true
	}
	username := currentUsername(ctx)
	if username == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(role.CreatedBy), username)
}
