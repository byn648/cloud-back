package managerservicelogic

import (
	"context"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"

	"github.com/zeromicro/go-zero/core/logx"
)

type ListOnecClusterNodeEnergyLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewListOnecClusterNodeEnergyLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListOnecClusterNodeEnergyLogic {
	return &ListOnecClusterNodeEnergyLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

// Deprecated: 节点相对能耗表没有真实采集依据，调度器已不再使用。
func (l *ListOnecClusterNodeEnergyLogic) ListOnecClusterNodeEnergy(in *pb.ListOnecClusterNodeEnergyReq) (*pb.ListOnecClusterNodeEnergyResp, error) {
	return &pb.ListOnecClusterNodeEnergyResp{Data: nil}, nil
}
