package svc

import (
	"github.com/yanshicheng/kube-nova/application/green-api/internal/repository/greenrepo"
	"github.com/yanshicheng/kube-nova/application/green-api/internal/service"
	appcfg "github.com/yanshicheng/kube-nova/common/config"
)

type ServiceContext struct {
	Config    appcfg.AppConfig
	Green     *service.Service
	GreenRepo *greenrepo.Service
}

func NewServiceContext(cfg appcfg.AppConfig) *ServiceContext {
	return &ServiceContext{
		Config:    cfg,
		Green:     service.NewService(),
		GreenRepo: greenrepo.NewService(cfg.Mysql),
	}
}
