package nodemonitor

import (
	"context"
	"fmt"
	"sort"

	"github.com/yanshicheng/kube-nova/application/console-api/internal/logic/utils"
	"github.com/yanshicheng/kube-nova/application/console-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/console-api/internal/types"
	pmtypes "github.com/yanshicheng/kube-nova/common/prometheusmanager/types"
	"github.com/zeromicro/go-zero/core/logx"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type GetNodePodsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// 获取节点上的 Pod 信息
func NewGetNodePodsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetNodePodsLogic {
	return &GetNodePodsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetNodePodsLogic) GetNodePods(req *types.GetNodePodsRequest) (resp *types.GetNodePodsResponse, err error) {
	var (
		pods    *pmtypes.NodePodMetrics
		promErr error
	)

	client, err := l.svcCtx.PrometheusManager.Get(req.ClusterUuid)
	if err != nil {
		promErr = err
		l.Errorf("获取 Prometheus 客户端失败，准备回退 K8s 直连: %v", err)
	} else {
		node := client.Node()
		timeRange := utils.ParseTimeRange(req.Start, req.End, "")

		pods, err = node.GetNodePods(req.NodeName, timeRange)
		if err != nil {
			promErr = err
			l.Errorf("获取节点 Pod 信息失败，准备回退 K8s 直连: Node=%s, Error=%v", req.NodeName, err)
		}
	}

	// 优先使用监控侧 Pod 明细；当监控侧无数据/不可用时，回退到 K8s API 直接查询节点上的 Pod。
	if promErr != nil || pods == nil || (pods.TotalPods == 0 && len(pods.PodList) == 0) {
		k8sPods, k8sErr := l.getNodePodsFromK8s(req.ClusterUuid, req.NodeName)
		if k8sErr != nil {
			if promErr != nil {
				return nil, promErr
			}
			return nil, k8sErr
		}
		pods = k8sPods
	}

	resp = &types.GetNodePodsResponse{
		Data: convertNodePodMetrics(pods),
	}

	l.Infof("获取节点 Pod 信息成功: Node=%s, Total=%d", req.NodeName, pods.TotalPods)
	return resp, nil
}

func (l *GetNodePodsLogic) getNodePodsFromK8s(clusterUUID, nodeName string) (*pmtypes.NodePodMetrics, error) {
	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUUID)
	if err != nil {
		return nil, fmt.Errorf("获取集群客户端失败: %w", err)
	}

	podList, err := clusterClient.GetClientSet().CoreV1().Pods("").List(l.ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, fmt.Errorf("查询节点 Pod 失败: %w", err)
	}

	metrics := &pmtypes.NodePodMetrics{
		NodeName:     nodeName,
		PodList:      make([]pmtypes.NodePodBrief, 0, len(podList.Items)),
		TopPodsByCPU: make([]pmtypes.PodResourceUsage, 0),
		TopPodsByMem: make([]pmtypes.PodResourceUsage, 0),
	}

	for _, pod := range podList.Items {
		restartCount := int64(0)
		for _, c := range pod.Status.ContainerStatuses {
			restartCount += int64(c.RestartCount)
		}

		phase := string(pod.Status.Phase)
		switch pod.Status.Phase {
		case corev1.PodRunning:
			metrics.RunningPods++
		case corev1.PodPending:
			metrics.PendingPods++
		case corev1.PodFailed:
			metrics.FailedPods++
		}

		metrics.PodList = append(metrics.PodList, pmtypes.NodePodBrief{
			Namespace:            pod.Namespace,
			PodName:              pod.Name,
			Phase:                phase,
			CPUUsage:             0,
			MemoryUsage:          0,
			RestartCount:         restartCount,
			PodCurrentPowerWatts: 0,
			PodEnergyDeltaJoules: 0,
		})
	}

	sort.Slice(metrics.PodList, func(i, j int) bool {
		left := metrics.PodList[i]
		right := metrics.PodList[j]
		if left.Namespace == right.Namespace {
			return left.PodName < right.PodName
		}
		return left.Namespace < right.Namespace
	})

	metrics.TotalPods = int64(len(metrics.PodList))
	return metrics, nil
}
