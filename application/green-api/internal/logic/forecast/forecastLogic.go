package forecast

import (
	"context"

	"github.com/yanshicheng/kube-nova/application/green-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/green-api/internal/types"
)

type ForecastLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewForecastLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ForecastLogic {
	return &ForecastLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ForecastLogic) GetForecast(req *types.ForecastRequest) *types.ForecastResponse {
	return l.svcCtx.Green.GetForecast(req, &l.svcCtx.GreenRepo)
}
