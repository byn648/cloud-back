package managerservicelogic

import (
	"context"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetClusterIngressDomainListLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewGetClusterIngressDomainListLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetClusterIngressDomainListLogic {
	return &GetClusterIngressDomainListLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

// 获取 某一个集群 ingressDomain 列表
func (l *GetClusterIngressDomainListLogic) GetClusterIngressDomainList(in *pb.GetClusterDomainListReq) (*pb.GetClusterDomainListResp, error) {
	username, roles, userID := getClusterCurrentUserContext(l.ctx)
	canAccess, err := canAccessClusterByUUID(l.ctx, l.svcCtx, in.Uuid, username, roles, userID)
	if err != nil {
		l.Errorf("校验集群域名查看权限失败 [clusterUuid=%s]: %v", in.Uuid, err)
		return nil, errorx.Msg("校验集群域名查看权限失败")
	}
	if !canAccess {
		return nil, errorx.Msg("无权限查看该集群域名配置")
	}

	cluster, err := l.svcCtx.OnecClusterModel.FindOneByUuid(l.ctx, in.Uuid)
	if err != nil {
		l.Errorf("查询集群失败: %v", err)
		return nil, err
	}
	// 如果为空直接返回[] 如果存在 , 号分隔返回 []strings
	domains := strings.Split(cluster.IngressDomain, ",")
	for i := range domains {
		domains[i] = strings.TrimSpace(domains[i])
	}
	return &pb.GetClusterDomainListResp{
		Data: domains,
	}, nil
}
