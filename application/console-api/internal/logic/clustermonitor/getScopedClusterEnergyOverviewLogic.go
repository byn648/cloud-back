package clustermonitor

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yanshicheng/kube-nova/application/console-api/internal/logic/utils"
	"github.com/yanshicheng/kube-nova/application/console-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/console-api/internal/types"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/client/managerservice"
	pmtypes "github.com/yanshicheng/kube-nova/common/prometheusmanager/types"
	"github.com/zeromicro/go-zero/core/logx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type GetScopedClusterEnergyOverviewLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

type clusterEnergyMetric struct {
	PodCurrentPowerWatts  float64
	PodEnergyDeltaJoules  float64
	NodeCurrentPowerWatts float64
	NodeEnergyDeltaJoules float64
	NodeMetric            string
}

const (
	scopedEnergyClusterConcurrency = 4
	scopedEnergyNodeNameCacheTTL   = 20 * time.Second
)

type cachedScopedNodeNamesValue struct {
	Names     []string
	ExpiresAt time.Time
}

var scopedEnergyNodeNamesCache sync.Map // map[clusterUUID]cachedScopedNodeNamesValue

func NewGetScopedClusterEnergyOverviewLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetScopedClusterEnergyOverviewLogic {
	return &GetScopedClusterEnergyOverviewLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetScopedClusterEnergyOverviewLogic) GetScopedClusterEnergyOverview(req *types.GetScopedClusterEnergyOverviewRequest) (resp *types.GetScopedClusterEnergyOverviewResponse, err error) {
	timeRange := normalizeEnergyTimeRange(utils.ParseTimeRange(req.Start, req.End, req.Step))

	clusters, err := l.listVisibleClusters()
	if err != nil {
		l.Errorf("查询当前用户可见集群失败: %v", err)
		return nil, err
	}

	durationSeconds := int64(timeRange.End.Sub(timeRange.Start).Seconds())
	if durationSeconds < 0 {
		durationSeconds = 0
	}

	items := make([]types.ClusterEnergyScopeItem, len(clusters))
	summary := types.ClusterEnergyScopeSummary{
		ClusterCount:    int64(len(clusters)),
		DurationSeconds: durationSeconds,
	}

	if len(clusters) > 0 {
		var (
			wg      sync.WaitGroup
			sem     = make(chan struct{}, scopedEnergyClusterConcurrency)
			sumLock sync.Mutex
		)

		for i, cluster := range clusters {
			i := i
			cluster := cluster
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				item := types.ClusterEnergyScopeItem{
					ClusterUuid:     cluster.GetUuid(),
					ClusterName:     cluster.GetName(),
					Environment:     cluster.GetEnvironment(),
					DurationSeconds: durationSeconds,
				}

				metric, queryErr := l.queryClusterEnergy(cluster.GetUuid(), timeRange)
				if queryErr != nil {
					item.Error = queryErr.Error()
					l.Errorf("查询集群能耗失败: cluster=%s(%s), error=%v", item.ClusterName, item.ClusterUuid, queryErr)
					items[i] = item
					return
				}

				item.PodCurrentPowerWatts = sanitizeFloat64(metric.PodCurrentPowerWatts)
				item.PodEnergyDeltaJoules = sanitizeFloat64(metric.PodEnergyDeltaJoules)
				item.NodeCurrentPowerWatts = sanitizeFloat64(metric.NodeCurrentPowerWatts)
				item.NodeEnergyDeltaJoules = sanitizeFloat64(metric.NodeEnergyDeltaJoules)
				item.NodeMetric = metric.NodeMetric

				sumLock.Lock()
				summary.PodCurrentPowerWatts += item.PodCurrentPowerWatts
				summary.PodEnergyDeltaJoules += item.PodEnergyDeltaJoules
				summary.NodeCurrentPowerWatts += item.NodeCurrentPowerWatts
				summary.NodeEnergyDeltaJoules += item.NodeEnergyDeltaJoules
				sumLock.Unlock()

				items[i] = item
			}()
		}

		wg.Wait()
	}

	resp = &types.GetScopedClusterEnergyOverviewResponse{
		Data: types.ClusterEnergyScopeOverview{
			Summary: summary,
			Items:   items,
		},
	}

	l.Infof("获取可见集群能耗概览成功: clusters=%d, duration=%ds", len(clusters), durationSeconds)
	return resp, nil
}

func normalizeEnergyTimeRange(timeRange *pmtypes.TimeRange) *pmtypes.TimeRange {
	now := time.Now()
	if timeRange == nil {
		return &pmtypes.TimeRange{
			Start: now.Add(-24 * time.Hour),
			End:   now,
			Step:  "5m",
		}
	}

	if timeRange.End.IsZero() {
		timeRange.End = now
	}

	if timeRange.Start.IsZero() {
		timeRange.Start = timeRange.End.Add(-24 * time.Hour)
	}

	if !timeRange.End.After(timeRange.Start) {
		timeRange.End = timeRange.Start.Add(1 * time.Hour)
	}

	if timeRange.Step == "" {
		timeRange.Step = "5m"
	}

	return timeRange
}

func (l *GetScopedClusterEnergyOverviewLogic) listVisibleClusters() ([]*managerservice.Cluster, error) {
	const pageSize uint64 = 200
	all := make([]*managerservice.Cluster, 0, pageSize)
	page := uint64(1)

	for {
		result, err := l.svcCtx.ManagerRpc.ClusterSearch(l.ctx, &managerservice.SearchClusterReq{
			Page:     page,
			PageSize: pageSize,
		})
		if err != nil {
			return nil, err
		}

		if result == nil || len(result.Data) == 0 {
			break
		}

		all = append(all, result.Data...)
		if uint64(len(all)) >= result.GetTotal() || uint64(len(result.Data)) < pageSize {
			break
		}

		page++
		if page > 100 {
			break
		}
	}

	return all, nil
}

func (l *GetScopedClusterEnergyOverviewLogic) queryClusterEnergy(clusterUUID string, timeRange *pmtypes.TimeRange) (*clusterEnergyMetric, error) {
	client, err := l.svcCtx.PrometheusManager.Get(clusterUUID)
	if err != nil {
		return nil, fmt.Errorf("获取 Prometheus 客户端失败: %w", err)
	}

	nodeNames, err := l.getClusterNodeNames(clusterUUID, client, timeRange)
	if err != nil {
		return nil, err
	}
	if len(nodeNames) == 0 {
		return nil, fmt.Errorf("当前集群没有可用节点")
	}
	nodeMatcher := buildNodeRegexMatcher(nodeNames)

	rateWindow := "5m"
	durationWindow := energyDurationWindow(timeRange)

	podPowerQuery := fmt.Sprintf(`sum by () (
			label_replace(
				label_replace(
					rate(kepler_container_joules_total{container_name!="",container_name!="POD"}[%s]),
					"namespace", "$1", "container_namespace", "(.*)"
				),
				"pod", "$1", "pod_name", "(.*)"
			)
			* on(namespace, pod) group_left() kube_pod_info{node=~"%s"}
		)`, rateWindow, nodeMatcher)
	podEnergyDeltaQuery := fmt.Sprintf(`sum by () (
			label_replace(
				label_replace(
					increase(kepler_container_joules_total{container_name!="",container_name!="POD"}[%s]),
					"namespace", "$1", "container_namespace", "(.*)"
				),
				"pod", "$1", "pod_name", "(.*)"
			)
			* on(namespace, pod) group_left() kube_pod_info{node=~"%s"}
		)`, durationWindow, nodeMatcher)

	var (
		podPower      float64
		podPowerFound bool
		podPowerErr   error
		podDelta      float64
		podDeltaFound bool
		podDeltaErr   error
		nodePower     float64
		nodeDelta     float64
		nodeMetric    string
		nodeErr       error
	)

	var metricWg sync.WaitGroup
	metricWg.Add(3)
	go func() {
		defer metricWg.Done()
		podPower, podPowerFound, podPowerErr = querySingleValue(client, podPowerQuery)
	}()
	go func() {
		defer metricWg.Done()
		podDelta, podDeltaFound, podDeltaErr = querySingleValue(client, podEnergyDeltaQuery)
	}()
	go func() {
		defer metricWg.Done()
		nodePower, nodeDelta, nodeMetric, nodeErr = queryNodeEnergyValue(client, rateWindow, durationWindow, nodeMatcher)
	}()
	metricWg.Wait()

	if !podPowerFound && !podDeltaFound && podPowerErr != nil && podDeltaErr != nil {
		return nil, fmt.Errorf("查询 Pod 能耗失败: %v; %v", podPowerErr, podDeltaErr)
	}
	if nodeErr != nil || (nodeMetric == "" && nodePower <= 0 && nodeDelta <= 0) {
		// Node 能耗按节点标签查询失败或未命中时，不中断整体返回；使用 Pod 聚合作为保底估算。
		nodePower = podPower
		nodeDelta = podDelta
		nodeMetric = "kepler_container_joules_total(fallback)"
		if nodeErr != nil {
			l.Errorf("查询 Node 能耗失败，已回退 Pod 聚合估算: cluster=%s, error=%v", clusterUUID, nodeErr)
		} else {
			l.Infof("未命中 Node 能耗指标，已回退 Pod 聚合估算: cluster=%s", clusterUUID)
		}
	}

	return &clusterEnergyMetric{
		PodCurrentPowerWatts:  sanitizeFloat64(podPower),
		PodEnergyDeltaJoules:  sanitizeFloat64(podDelta),
		NodeCurrentPowerWatts: sanitizeFloat64(nodePower),
		NodeEnergyDeltaJoules: sanitizeFloat64(nodeDelta),
		NodeMetric:            nodeMetric,
	}, nil
}

func (l *GetScopedClusterEnergyOverviewLogic) getClusterNodeNames(clusterUUID string, promClient pmtypes.PrometheusClient, timeRange *pmtypes.TimeRange) ([]string, error) {
	if cached := loadCachedScopedNodeNames(clusterUUID); len(cached) > 0 {
		return cached, nil
	}

	nameSet := make(map[string]struct{}, 16)
	result := make([]string, 0, 16)

	// 优先走轻量 Prometheus 查询，仅提取节点名，避免 ListNodesMetrics 触发大量指标聚合。
	for _, nodeName := range listNodeNamesFromPrometheus(promClient, timeRange) {
		if _, existed := nameSet[nodeName]; existed {
			continue
		}
		nameSet[nodeName] = struct{}{}
		result = append(result, nodeName)
	}
	if len(result) > 0 {
		sort.Strings(result)
		storeCachedScopedNodeNames(clusterUUID, result)
		return result, nil
	}

	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUUID)
	if err != nil {
		return nil, fmt.Errorf("查询节点列表失败: %w", err)
	}

	nodeList, err := clusterClient.GetClientSet().CoreV1().Nodes().List(l.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("查询节点列表失败: %w", err)
	}

	for _, node := range nodeList.Items {
		nodeName := strings.TrimSpace(node.Name)
		if nodeName == "" {
			continue
		}
		if _, existed := nameSet[nodeName]; existed {
			continue
		}
		nameSet[nodeName] = struct{}{}
		result = append(result, nodeName)
	}

	sort.Strings(result)
	storeCachedScopedNodeNames(clusterUUID, result)
	return result, nil
}

func listNodeNamesFromPrometheus(client pmtypes.PrometheusClient, timeRange *pmtypes.TimeRange) []string {
	if client == nil {
		return nil
	}

	var ts *time.Time
	if timeRange != nil && !timeRange.End.IsZero() {
		end := timeRange.End
		ts = &end
	}

	queries := []string{
		`kube_node_info`,
		`kube_node_status_condition{condition="Ready"}`,
		`node_uname_info`,
	}

	nameSet := make(map[string]struct{}, 16)
	names := make([]string, 0, 16)
	appendName := func(name string) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return
		}
		if _, exists := nameSet[trimmed]; exists {
			return
		}
		nameSet[trimmed] = struct{}{}
		names = append(names, trimmed)
	}

	for _, query := range queries {
		results, err := client.Query(query, ts)
		if err != nil || len(results) == 0 {
			continue
		}

		for _, item := range results {
			appendName(item.Metric["node"])
			appendName(item.Metric["nodename"])
		}
		if len(names) > 0 {
			sort.Strings(names)
			return names
		}
	}

	return nil
}

func loadCachedScopedNodeNames(clusterUUID string) []string {
	cachedRaw, ok := scopedEnergyNodeNamesCache.Load(clusterUUID)
	if !ok {
		return nil
	}
	cached, ok := cachedRaw.(cachedScopedNodeNamesValue)
	if !ok {
		scopedEnergyNodeNamesCache.Delete(clusterUUID)
		return nil
	}
	if time.Now().After(cached.ExpiresAt) {
		scopedEnergyNodeNamesCache.Delete(clusterUUID)
		return nil
	}
	return append([]string(nil), cached.Names...)
}

func storeCachedScopedNodeNames(clusterUUID string, names []string) {
	if clusterUUID == "" || len(names) == 0 {
		return
	}
	scopedEnergyNodeNamesCache.Store(clusterUUID, cachedScopedNodeNamesValue{
		Names:     append([]string(nil), names...),
		ExpiresAt: time.Now().Add(scopedEnergyNodeNameCacheTTL),
	})
}

func buildNodeRegexMatcher(nodeNames []string) string {
	escaped := make([]string, 0, len(nodeNames))
	for _, nodeName := range nodeNames {
		trimmed := strings.TrimSpace(nodeName)
		if trimmed == "" {
			continue
		}
		escaped = append(escaped, regexp.QuoteMeta(trimmed))
	}
	if len(escaped) == 0 {
		return "^$"
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func energyDurationWindow(timeRange *pmtypes.TimeRange) string {
	if timeRange == nil {
		return "24h"
	}

	duration := timeRange.End.Sub(timeRange.Start)
	if duration <= 0 {
		return "24h"
	}

	seconds := int64(duration.Seconds())
	if seconds < 60 {
		seconds = 60
	}
	return fmt.Sprintf("%ds", seconds)
}

func querySingleValue(client pmtypes.PrometheusClient, promql string) (value float64, found bool, err error) {
	results, err := client.Query(promql, nil)
	if err != nil {
		return 0, false, err
	}

	if len(results) == 0 {
		return 0, false, nil
	}

	return sanitizeFloat64(results[0].Value), true, nil
}

func queryNodeEnergyValue(client pmtypes.PrometheusClient, rateWindow, durationWindow, nodeMatcher string) (power float64, delta float64, metric string, err error) {
	candidates := []string{
		"kepler_node_package_joules_total",
		"kepler_node_joules_total",
		"kepler_node_core_joules_total",
	}

	var lastErr error
	for _, candidate := range candidates {
		powerQuery := fmt.Sprintf(`sum(rate(%s{node=~"%s"}[%s]))`, candidate, nodeMatcher, rateWindow)
		deltaQuery := fmt.Sprintf(`sum(increase(%s{node=~"%s"}[%s]))`, candidate, nodeMatcher, durationWindow)

		currentPower, powerFound, powerErr := querySingleValue(client, powerQuery)
		energyDelta, deltaFound, deltaErr := querySingleValue(client, deltaQuery)
		if powerErr != nil || deltaErr != nil {
			if powerErr != nil {
				lastErr = powerErr
			}
			if deltaErr != nil {
				lastErr = deltaErr
			}
			continue
		}

		if powerFound || deltaFound {
			return currentPower, energyDelta, candidate, nil
		}
	}

	if lastErr != nil {
		return 0, 0, "", fmt.Errorf("查询 Node 能耗失败: %w", lastErr)
	}

	// 未发现 Node 能耗指标时返回 0，不中断整页展示。
	return 0, 0, "", nil
}
