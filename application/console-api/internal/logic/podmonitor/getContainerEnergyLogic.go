package podmonitor

import (
	"context"

	"github.com/yanshicheng/kube-nova/application/console-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/console-api/internal/types"
	"github.com/zeromicro/go-zero/core/logx"
)

type GetContainerEnergyLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// 获取容器能耗指标
func NewGetContainerEnergyLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetContainerEnergyLogic {
	return &GetContainerEnergyLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetContainerEnergyLogic) GetContainerEnergy(req *types.GetContainerEnergyRequest) (resp *types.GetContainerEnergyResponse, err error) {
	client, err := l.svcCtx.PrometheusManager.Get(req.ClusterUuid)
	if err != nil {
		l.Errorf("获取 Prometheus 客户端失败: %v", err)
		return nil, err
	}

	pod := client.Pod()
	timeRange := parseRangeTime(req.Start, req.End, req.Step)

	metrics, err := pod.GetContainerEnergy(req.Namespace, req.PodName, req.ContainerName, timeRange)
	if err != nil {
		l.Errorf("获取容器能耗失败: Namespace=%s, Pod=%s, Container=%s, Error=%v",
			req.Namespace, req.PodName, req.ContainerName, err)
		return nil, err
	}

	resp = &types.GetContainerEnergyResponse{
		Data: types.ContainerEnergyMetrics{
			Namespace:     metrics.Namespace,
			PodName:       metrics.PodName,
			ContainerName: metrics.ContainerName,
			Current: types.EnergySnapshot{
				Timestamp:   metrics.Current.Timestamp.Unix(),
				JoulesTotal: metrics.Current.JoulesTotal,
				PowerWatts:  metrics.Current.PowerWatts,
			},
			Summary: types.EnergySummary{
				DurationSeconds:   metrics.Summary.DurationSeconds,
				EnergyDeltaJoules: metrics.Summary.EnergyDeltaJoules,
				AvgPowerWatts:     metrics.Summary.AvgPowerWatts,
				MaxPowerWatts:     metrics.Summary.MaxPowerWatts,
			},
		},
	}

	for _, point := range metrics.Trend {
		resp.Data.Trend = append(resp.Data.Trend, types.EnergyDataPoint{
			Timestamp:   point.Timestamp.Unix(),
			JoulesTotal: point.JoulesTotal,
			PowerWatts:  point.PowerWatts,
		})
	}

	l.Infof("获取容器能耗成功: Namespace=%s, Pod=%s, Container=%s, CurrentPower=%.4fW",
		req.Namespace, req.PodName, req.ContainerName, metrics.Current.PowerWatts)

	return resp, nil
}
