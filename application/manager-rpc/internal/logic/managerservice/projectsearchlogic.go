package managerservicelogic

import (
	"context"
	"errors"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/model"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"
	"github.com/yanshicheng/kube-nova/common/vars"

	"github.com/zeromicro/go-zero/core/logx"
)

type ProjectSearchLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewProjectSearchLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ProjectSearchLogic {
	return &ProjectSearchLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

// ProjectSearch 搜索项目列表
func (l *ProjectSearchLogic) ProjectSearch(in *pb.SearchOnecProjectReq) (*pb.SearchOnecProjectResp, error) {
	if in.Page == 0 {
		in.Page = vars.Page
	}
	if in.PageSize == 0 {
		in.PageSize = vars.PageSize
	}

	// 构建查询条件
	var queryStr string
	var args []interface{}

	if in.Name != "" {
		queryStr = "name LIKE ?"
		args = append(args, "%"+in.Name+"%")
	}

	if in.Uuid != "" {
		if queryStr != "" {
			queryStr += " AND "
		}
		queryStr += "uuid = ?"
		args = append(args, in.Uuid)
	}

	username, roles, userId := l.getCurrentUserContext()
	if !l.isSuperAdmin(username, roles) {
		if strings.TrimSpace(username) == "" {
			// 普通用户但上下文缺失用户名时，返回空列表避免越权
			return &pb.SearchOnecProjectResp{
				Data:  []*pb.ProjectInfo{},
				Total: 0,
			}, nil
		}
		if queryStr != "" {
			queryStr += " AND "
		}
		visibilityClause, visibilityArgs := l.buildProjectVisibilityClause(username, userId)
		queryStr += visibilityClause
		args = append(args, visibilityArgs...)
	}

	// 执行搜索
	projects, total, err := l.svcCtx.OnecProjectModel.Search(l.ctx, in.OrderStr, in.IsAsc, in.Page, in.PageSize, queryStr, args...)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return &pb.SearchOnecProjectResp{
				Data:  []*pb.ProjectInfo{},
				Total: 0,
			}, nil
		}

		l.Errorf("搜索项目失败，错误: %v", err)
		return nil, errorx.Msg("搜索项目失败")
	}

	// 转换结果
	var data []*pb.ProjectInfo
	for _, p := range projects {
		statistics, err := l.svcCtx.OnecProjectModel.GetProjectStatistics(l.ctx, p.Id)
		if err != nil {
			l.Errorf("获取项目统计信息失败，错误: %v", err)
		}

		data = append(data, &pb.ProjectInfo{
			Id:            p.Id,
			Name:          p.Name,
			Uuid:          p.Uuid,
			Description:   p.Description,
			CreatedBy:     p.CreatedBy,
			UpdatedBy:     p.UpdatedBy,
			AdminCount:    statistics.AdminCount,
			ResourceCount: statistics.ProjectClusterCount,
			CreatedAt:     p.CreatedAt.Unix(),
			UpdatedAt:     p.UpdatedAt.Unix(),
			IsSystem:      p.IsSystem,
		})
	}

	return &pb.SearchOnecProjectResp{
		Data:  data,
		Total: total,
	}, nil
}

func (l *ProjectSearchLogic) getCurrentUserContext() (string, []string, uint64) {
	username := ""
	if ctxUsername, ok := l.ctx.Value("username").(string); ok {
		username = ctxUsername
	}

	roles := []string{}
	if ctxRoles, ok := l.ctx.Value("roles").([]string); ok {
		roles = ctxRoles
	}

	userId := uint64(0)
	if ctxUserId, ok := l.ctx.Value("userId").(uint64); ok {
		userId = ctxUserId
	}

	return username, roles, userId
}

func (l *ProjectSearchLogic) isSuperAdmin(username string, roles []string) bool {
	if strings.EqualFold(strings.TrimSpace(username), "super_admin") {
		return true
	}
	for _, role := range roles {
		if strings.EqualFold(role, "SUPER_ADMIN") {
			return true
		}
	}
	return false
}

func (l *ProjectSearchLogic) buildProjectVisibilityClause(username string, userId uint64) (string, []interface{}) {
	if userId > 0 {
		clause := `(
		created_by = ?
		OR EXISTS (
			SELECT 1
			FROM onec_project_member opm
			WHERE opm.project_id = onec_project.id
				AND opm.is_deleted = 0
				AND opm.user_id = ?
		)
	)`
		return clause, []interface{}{username, userId}
	}
	clause := `(
		created_by = ?
		OR EXISTS (
			SELECT 1
			FROM onec_project_member opm
			JOIN sys_user su ON su.id = opm.user_id
			WHERE opm.project_id = onec_project.id
				AND opm.is_deleted = 0
				AND su.is_deleted = 0
				AND su.username = ?
		)
	)`
	return clause, []interface{}{username, username}
}
