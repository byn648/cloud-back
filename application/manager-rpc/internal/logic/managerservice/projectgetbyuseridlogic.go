package managerservicelogic

import (
	"context"
	"errors"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/model"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"

	"github.com/zeromicro/go-zero/core/logx"
)

type ProjectGetByUserIdLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewProjectGetByUserIdLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ProjectGetByUserIdLogic {
	return &ProjectGetByUserIdLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *ProjectGetByUserIdLogic) ProjectGetByUserId(in *pb.GetOnecProjectsByUserIdReq) (*pb.GetOnecProjectsByUserIdResp, error) {
	username, roles := l.resolveCurrentUserContext(in)

	// 标准化搜索名称
	searchName := ""
	if in.Name != "" {
		searchName = strings.ToLower(strings.TrimSpace(in.Name))
		l.Infof("开始查询用户 [username:%s] 名称包含 [%s] 的项目列表", username, searchName)
	} else {
		l.Infof("开始查询用户 [username:%s] 的所有项目列表", username)
	}

	// 检查是否为超级管理员
	if l.isSuperAdmin(username, roles) {
		l.Infof("用户 [username:%s] 拥有 SUPER_ADMIN 角色，返回所有项目", username)
		return l.getAllProjects(searchName)
	}

	if strings.TrimSpace(username) == "" {
		l.Error("用户名为空或无效")
		return nil, errorx.Msg("用户名无效")
	}

	// 查询当前用户创建的项目
	queryStr := `(
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
	args := []any{username, username}
	if searchName != "" {
		queryStr += " AND LOWER(name) LIKE ?"
		args = append(args, "%"+searchName+"%")
	}

	userProjects, err := l.svcCtx.OnecProjectModel.SearchNoPage(
		l.ctx,
		"created_at",
		false,
		queryStr,
		args...,
	)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			l.Infof("用户 [username:%s] 未创建任何项目", username)
			return &pb.GetOnecProjectsByUserIdResp{Data: []*pb.ProjectInfo{}}, nil
		}
		l.Errorf("查询用户 [username:%s] 创建的项目失败: %v", username, err)
		return nil, errorx.Msg("查询用户项目权限失败")
	}

	if len(userProjects) == 0 {
		l.Infof("用户 [username:%s] 未创建任何项目", username)
		return &pb.GetOnecProjectsByUserIdResp{Data: []*pb.ProjectInfo{}}, nil
	}

	projects := make([]*pb.ProjectInfo, 0, len(userProjects))
	for _, project := range userProjects {
		pbProject, buildErr := l.buildProjectInfo(project)
		if buildErr != nil {
			l.Errorf("构建项目 [ID:%d] 信息失败: %v", project.Id, buildErr)
			continue
		}
		projects = append(projects, pbProject)
	}

	l.Infof("成功查询到用户 [username:%s] 的 %d 个匹配项目", username, len(projects))

	return &pb.GetOnecProjectsByUserIdResp{Data: projects}, nil
}

func (l *ProjectGetByUserIdLogic) resolveCurrentUserContext(in *pb.GetOnecProjectsByUserIdReq) (string, []string) {
	username := ""
	if ctxUsername, ok := l.ctx.Value("username").(string); ok {
		username = ctxUsername
	}

	roles := in.Roles
	if ctxRoles, ok := l.ctx.Value("roles").([]string); ok && len(ctxRoles) > 0 {
		roles = ctxRoles
	}

	return username, roles
}

// isSuperAdmin 检查是否为超级管理员（用户名兜底 + 角色判断）
func (l *ProjectGetByUserIdLogic) isSuperAdmin(username string, roles []string) bool {
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

// getAllProjects 获取所有项目（超级管理员使用）
func (l *ProjectGetByUserIdLogic) getAllProjects(searchName string) (*pb.GetOnecProjectsByUserIdResp, error) {
	// 构建查询条件
	queryStr := ""
	var args []any

	if searchName != "" {
		queryStr = "LOWER(name) LIKE ?"
		args = append(args, "%"+searchName+"%")
	}

	// 查询所有项目
	allProjects, err := l.svcCtx.OnecProjectModel.SearchNoPage(
		l.ctx,
		"created_at",
		false,
		queryStr,
		args...,
	)
	if err != nil {
		l.Errorf("查询所有项目失败: %v", err)
		return nil, errorx.Msg("查询项目列表失败")
	}

	// 构建返回结果
	var projects []*pb.ProjectInfo
	for _, project := range allProjects {
		pbProject, err := l.buildProjectInfo(project)
		if err != nil {
			l.Errorf("构建项目 [ID:%d] 信息失败: %v", project.Id, err)
			continue
		}
		projects = append(projects, pbProject)
	}

	l.Infof("超级管理员查询成功，共 %d 个匹配项目", len(projects))
	return &pb.GetOnecProjectsByUserIdResp{Data: projects}, nil
}

// buildProjectInfo 构建单个项目信息
func (l *ProjectGetByUserIdLogic) buildProjectInfo(project *model.OnecProject) (*pb.ProjectInfo, error) {
	// 构建项目信息响应
	return &pb.ProjectInfo{
		Id:       project.Id,
		Name:     project.Name,
		Uuid:     project.Uuid,
		IsSystem: project.IsSystem,
	}, nil
}
