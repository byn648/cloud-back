package handler

import (
	"github.com/yanshicheng/kube-nova/application/green-api/internal/svc"
	appcfg "github.com/yanshicheng/kube-nova/common/config"
)

type Handler struct {
	svcCtx *svc.ServiceContext
}

func New(cfg appcfg.AppConfig) *Handler {
	return &Handler{
		svcCtx: svc.NewServiceContext(cfg),
	}
}

func (h *Handler) SvcCtx() *svc.ServiceContext {
	return h.svcCtx
}
