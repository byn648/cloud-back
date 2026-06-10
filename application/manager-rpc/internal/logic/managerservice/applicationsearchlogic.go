package managerservicelogic

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/model"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/zeromicro/go-zero/core/logx"
)

type ApplicationSearchLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewApplicationSearchLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ApplicationSearchLogic {
	return &ApplicationSearchLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

// ApplicationSearch 查询应用列表
func (l *ApplicationSearchLogic) ApplicationSearch(in *pb.SearchOnecProjectApplicationReq) (*pb.SearchOnecProjectApplicationResp, error) {
	// 参数校验
	if in.WorkspaceId == 0 {
		l.Errorf("参数校验失败: workspaceId 不能为空")
		return nil, status.Error(codes.InvalidArgument, "workspaceId 不能为空")
	}

	workspace, err := l.svcCtx.OnecProjectWorkspaceModel.FindOne(l.ctx, in.WorkspaceId)
	if err != nil {
		l.Errorf("查询工作空间失败: %v, workspaceId=%d", err, in.WorkspaceId)
		return nil, status.Error(codes.Internal, "查询工作空间失败")
	}

	projectCluster, err := l.svcCtx.OnecProjectClusterModel.FindOne(l.ctx, workspace.ProjectClusterId)
	if err != nil {
		l.Errorf("查询项目集群失败: %v, projectClusterId=%d", err, workspace.ProjectClusterId)
		return nil, status.Error(codes.Internal, "查询项目集群失败")
	}

	logicalWorkspaces, err := l.svcCtx.OnecProjectWorkspaceModel.SearchNoPage(
		l.ctx,
		"`id`",
		true,
		"`project_cluster_id` IN (SELECT `id` FROM `onec_project_cluster` WHERE `project_id` = ? AND `is_deleted` = 0) AND `namespace` = ? AND `name` = ?",
		projectCluster.ProjectId,
		workspace.Namespace,
		workspace.Name,
	)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		l.Errorf("查询逻辑工作空间绑定失败: %v, projectId=%d, namespace=%s, name=%s",
			err, projectCluster.ProjectId, workspace.Namespace, workspace.Name)
		return nil, status.Error(codes.Internal, "查询逻辑工作空间绑定失败")
	}

	workspaceIds := make([]uint64, 0, len(logicalWorkspaces))
	for _, item := range logicalWorkspaces {
		workspaceIds = append(workspaceIds, item.Id)
	}
	if len(workspaceIds) == 0 {
		workspaceIds = append(workspaceIds, in.WorkspaceId)
	}

	// 构建查询条件
	placeholders := make([]string, 0, len(workspaceIds))
	var args []interface{}
	for _, workspaceId := range workspaceIds {
		placeholders = append(placeholders, "?")
		args = append(args, workspaceId)
	}

	queryStr := fmt.Sprintf("`workspace_id` IN (%s)", strings.Join(placeholders, ","))

	// 如果指定了服务中文名，添加模糊查询
	if in.NameCn != "" {
		queryStr += " AND `name_cn` LIKE ?"
		args = append(args, fmt.Sprintf("%%%s%%", in.NameCn))
	}

	// 查询数据
	applications, err := l.svcCtx.OnecProjectApplication.SearchNoPage(
		l.ctx, "", true, queryStr, args...)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			l.Infof("未查询到应用: workspaceId=%d", in.WorkspaceId)
			return &pb.SearchOnecProjectApplicationResp{Data: []*pb.OnecProjectApplication{}}, nil
		}
		l.Errorf("查询应用失败: %v, workspaceId=%d", err, in.WorkspaceId)
		return nil, status.Error(codes.Internal, "查询应用失败")
	}

	// 转换数据格式
	var pbApplications []*pb.OnecProjectApplication
	for _, app := range applications {
		pbApplications = append(pbApplications, &pb.OnecProjectApplication{
			Id:           app.Id,
			WorkspaceId:  app.WorkspaceId,
			NameCn:       app.NameCn,
			NameEn:       app.NameEn,
			ResourceType: app.ResourceType,
			Description:  app.Description,
			CreatedBy:    app.CreatedBy,
			UpdatedBy:    app.UpdatedBy,
			CreatedAt:    app.CreatedAt.Unix(),
			UpdatedAt:    app.UpdatedAt.Unix(),
		})
	}

	return &pb.SearchOnecProjectApplicationResp{
		Data: pbApplications,
	}, nil
}
