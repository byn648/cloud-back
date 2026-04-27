package managerservicelogic

import (
	"context"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"

	"github.com/zeromicro/go-zero/core/logx"
)

type UpdateClusterStorageCapacityLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewUpdateClusterStorageCapacityLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdateClusterStorageCapacityLogic {
	return &UpdateClusterStorageCapacityLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *UpdateClusterStorageCapacityLogic) UpdateClusterStorageCapacity(in *pb.UpdateClusterStorageCapacityReq) (*pb.UpdateClusterStorageCapacityResp, error) {
	// 查询集群的资源表
	clusterResource, err := l.svcCtx.OnecClusterResourceModel.FindOne(l.ctx, in.Id)
	if err != nil {
		l.Errorf("查询集群资源失败，集群ID: %s, 错误: %v", in.Id, err)
		return nil, errorx.Msg("查询集群资源失败")
	}
	username, roles, userID := getClusterCurrentUserContext(l.ctx)
	canAccess, err := canAccessClusterByUUID(l.ctx, l.svcCtx, clusterResource.ClusterUuid, username, roles, userID)
	if err != nil {
		l.Errorf("校验集群资源修改权限失败 [clusterUuid=%s]: %v", clusterResource.ClusterUuid, err)
		return nil, errorx.Msg("校验集群资源修改权限失败")
	}
	if !canAccess {
		return nil, errorx.Msg("无权限修改该集群资源")
	}

	clusterResource.StoragePhysicalCapacity = in.StoragePhysicalCapacity

	// 更新数据
	err = l.svcCtx.OnecClusterResourceModel.Update(l.ctx, clusterResource)
	if err != nil {
		l.Errorf("更新集群资源失败，集群ID: %s, 错误: %v", in.Id, err)
		return nil, errorx.Msg("更新集群资源失败")
	}
	return &pb.UpdateClusterStorageCapacityResp{}, nil
}
