package managerservicelogic

import (
	"context"
	"errors"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/model"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"

	"github.com/zeromicro/go-zero/core/logx"
)

type AppDefaultConfigLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewAppDefaultConfigLogic(ctx context.Context, svcCtx *svc.ServiceContext) *AppDefaultConfigLogic {
	return &AppDefaultConfigLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

// AppDefaultConfig 根据集群UUID和应用类型获取默认应用配置
func (l *AppDefaultConfigLogic) AppDefaultConfig(in *pb.AppDefaultConfigReq) (*pb.AppDefaultConfigResp, error) {
	l.Infof("步骤1: 验证请求参数")
	if in.ClusterUuid == "" {
		l.Errorf("集群UUID不能为空")
		return nil, errorx.Msg("集群UUID不能为空")
	}

	if in.AppType <= 0 {
		l.Errorf("应用类型无效 [appType=%d]", in.AppType)
		return nil, errorx.Msg("应用类型必须大于0")
	}

	// 2. 验证集群是否存在
	l.Infof("步骤2: 验证集群存在性 [clusterUuid=%s]", in.ClusterUuid)
	_, err := l.svcCtx.OnecClusterModel.FindOneByUuid(l.ctx, in.ClusterUuid)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			l.Errorf("集群不存在 [clusterUuid=%s]", in.ClusterUuid)
			return nil, errorx.Msg("指定的集群不存在")
		}
		l.Errorf("查询集群失败: %v", err)
		return nil, errorx.Msg("查询集群信息失败")
	}

	// 3. 查找集群中指定类型的默认应用配置
	l.Infof("步骤3: 查找默认应用配置")
	defaultQuery := "`cluster_uuid` = ? AND `app_type` = ? AND `is_default` = 1"
	defaultApps, err := l.svcCtx.OnecClusterAppModel.SearchNoPage(
		l.ctx,
		"created_at", // 按创建时间排序
		false,        // 降序，最新的在前面
		defaultQuery,
		in.ClusterUuid,
		in.AppType,
	)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		l.Errorf("查询默认应用配置失败: %v", err)
		return nil, errorx.Msg("查询默认应用配置失败")
	}

	var defaultApp *model.OnecClusterApp
	if len(defaultApps) > 0 {
		// 保持兼容：历史数据可能错误地存在多条默认配置，这里取最新一条继续服务，避免全量报错。
		if len(defaultApps) > 1 {
			l.Errorf("集群: %s, 存在多个类型为%d的默认应用配置，已回退取最新一条", in.ClusterUuid, in.AppType)
		}
		defaultApp = defaultApps[0]
	} else {
		// 兜底：若未设置默认配置，则回退到该类型最新一条可用配置（兼容老数据）。
		l.Infof("集群: %s 未设置默认应用配置，回退到同类型最新配置 [appType=%d]", in.ClusterUuid, in.AppType)
		fallbackQuery := "`cluster_uuid` = ? AND `app_type` = ?"
		fallbackApps, fallbackErr := l.svcCtx.OnecClusterAppModel.SearchNoPage(
			l.ctx,
			"created_at",
			false,
			fallbackQuery,
			in.ClusterUuid,
			in.AppType,
		)
		if fallbackErr != nil {
			if errors.Is(fallbackErr, model.ErrNotFound) {
				l.Errorf("集群: %s, 未找到指定类型的应用配置 [appType=%d]", in.ClusterUuid, in.AppType)
				return nil, errorx.Msg("未找到指定类型的默认应用配置")
			}
			l.Errorf("查询应用配置失败: %v", fallbackErr)
			return nil, errorx.Msg("查询默认应用配置失败")
		}
		if len(fallbackApps) == 0 {
			l.Errorf("集群: %s, 未找到指定类型的应用配置 [appType=%d]", in.ClusterUuid, in.AppType)
			return nil, errorx.Msg("未找到指定类型的默认应用配置")
		}
		defaultApp = fallbackApps[0]
	}

	l.Infof("步骤4: 构建默认配置响应")

	// 使用数据库中的默认配置
	l.Infof("使用数据库中的默认配置")
	resp := &pb.AppDefaultConfigResp{
		Uuid:               defaultApp.ClusterUuid,
		AppName:            defaultApp.AppName,
		AppCode:            defaultApp.AppCode,
		AppType:            defaultApp.AppType,
		IsDefault:          defaultApp.IsDefault,
		AppUrl:             defaultApp.AppUrl,
		Port:               defaultApp.Port,
		Protocol:           defaultApp.Protocol,
		AuthEnabled:        defaultApp.AuthEnabled,
		AuthType:           defaultApp.AuthType,
		Username:           defaultApp.Username,
		Password:           defaultApp.Password,
		AccessKey:          defaultApp.AccessKey,
		AccessSecret:       defaultApp.AccessSecret,
		TlsEnabled:         defaultApp.TlsEnabled,
		InsecureSkipVerify: defaultApp.InsecureSkipVerify,
	}

	// 处理可选的SQL NULL字段
	if defaultApp.Token.Valid {
		resp.Token = defaultApp.Token.String
	}

	if defaultApp.CaFile.Valid {
		resp.CaFile = defaultApp.CaFile.String
	}

	if defaultApp.CaKey.Valid {
		resp.CaKey = defaultApp.CaKey.String
	}

	if defaultApp.CaCert.Valid {
		resp.CaCert = defaultApp.CaCert.String
	}

	if defaultApp.ClientCert.Valid {
		resp.ClientCert = defaultApp.ClientCert.String
	}

	if defaultApp.ClientKey.Valid {
		resp.ClientKey = defaultApp.ClientKey.String
	}

	l.Infof("成功获取默认应用配置: appName=%s, appUrl=%s", resp.AppName, resp.AppUrl)
	return resp, nil
}
