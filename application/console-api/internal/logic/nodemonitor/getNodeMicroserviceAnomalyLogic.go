package nodemonitor

import (
	"context"
	"fmt"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultAnomalyLookback               = 24 * time.Hour
	defaultAnomalyStep                   = "5m"
	defaultMinPowerWatts                 = 0.05
	dynamicBaselineFromAvgMultiplier     = 1.10
	dynamicBaselineFromCurrentMultiplier = 0.75
	dynamicBaselineMinWatts              = 5.0
	anomalyClusterConcurrency            = 4
	anomalyNodeConcurrency               = 6
	anomalyNodeNameCacheTTL              = 20 * time.Second
	anomalyPodWorkloadCacheTTL           = 20 * time.Second
)

type GetNodeMicroserviceAnomalyLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

type visibleCluster struct {
	UUID string
	Name string
}

type podWorkloadIdentity struct {
	Service    string
	Workload   string
	OwnerKind  string
	OwnerName  string
	PodNameRaw string
}

type cachedNodeNamesValue struct {
	Names     []string
	ExpiresAt time.Time
}

type cachedPodWorkloadByNodeValue struct {
	Data      map[string]map[string]podWorkloadIdentity
	ExpiresAt time.Time
}

var (
	anomalyNodeNamesCache   sync.Map // map[clusterUUID]cachedNodeNamesValue
	anomalyPodWorkloadCache sync.Map // map[clusterUUID]cachedPodWorkloadByNodeValue
)

func NewGetNodeMicroserviceAnomalyLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetNodeMicroserviceAnomalyLogic {
	return &GetNodeMicroserviceAnomalyLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetNodeMicroserviceAnomalyLogic) GetNodeMicroserviceAnomaly(req *types.GetNodeMicroserviceAnomalyRequest) (resp *types.GetNodeMicroserviceAnomalyResponse, err error) {
	timeRange := normalizeMicroserviceAnomalyTimeRange(utils.ParseTimeRange(req.Start, req.End, ""))

	clusters, err := l.listVisibleClusters()
	if err != nil {
		l.Errorf("查询当前用户可见集群失败: %v", err)
		return nil, err
	}

	targetClusters, err := selectTargetClusters(clusters, strings.TrimSpace(req.ClusterUuid))
	if err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}

	minPowerWatts := req.MinPowerWatts
	if minPowerWatts <= 0 {
		minPowerWatts = defaultMinPowerWatts
	}

	nodeName := strings.TrimSpace(req.NodeName)
	detectedAt := timeRange.End.Unix()
	items := make([]types.MicroserviceAnomalyItem, 0, 64)
	warnings := make([]string, 0, 8)
	if len(targetClusters) > 0 {
		var (
			aggMu sync.Mutex
			wg    sync.WaitGroup
			sem   = make(chan struct{}, anomalyClusterConcurrency)
		)

		for _, cluster := range targetClusters {
			cluster := cluster
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				clusterItems, clusterWarnings := l.collectClusterAnomaly(cluster, nodeName, timeRange, minPowerWatts, detectedAt)
				aggMu.Lock()
				if len(clusterItems) > 0 {
					items = append(items, clusterItems...)
				}
				if len(clusterWarnings) > 0 {
					warnings = append(warnings, clusterWarnings...)
				}
				aggMu.Unlock()
			}()
		}

		wg.Wait()
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ExtraPowerKwh == items[j].ExtraPowerKwh {
			return items[i].DeltaPercent > items[j].DeltaPercent
		}
		return items[i].ExtraPowerKwh > items[j].ExtraPowerKwh
	})

	for i := range items {
		// 在不改变响应结构的情况下，把排行信息体现在 reason 尾部，前端可直接展示。
		items[i].Reason = fmt.Sprintf("%s（排名 #%d）", items[i].Reason, i+1)
	}

	if len(items) > limit {
		items = items[:limit]
	}

	resp = &types.GetNodeMicroserviceAnomalyResponse{
		Data: types.NodeMicroserviceAnomaly{
			Summary:  summarizeMicroserviceAnomaly(items),
			Items:    items,
			Warnings: uniqueStrings(warnings),
		},
	}

	l.Infof("获取微服务算电异常成功: clusters=%d, items=%d", len(targetClusters), len(items))
	return resp, nil
}

func (l *GetNodeMicroserviceAnomalyLogic) collectClusterAnomaly(
	cluster visibleCluster,
	nodeName string,
	timeRange *pmtypes.TimeRange,
	minPowerWatts float64,
	detectedAt int64,
) ([]types.MicroserviceAnomalyItem, []string) {
	client, promErr := l.svcCtx.PrometheusManager.Get(cluster.UUID)
	if promErr != nil {
		return nil, []string{fmt.Sprintf("%s: 获取 Prometheus 客户端失败: %v", cluster.Name, promErr)}
	}

	nodeOperator := client.Node()
	nodeNames, nodeErr := l.resolveNodeNames(cluster.UUID, nodeName, client, timeRange)
	if nodeErr != nil {
		return nil, []string{fmt.Sprintf("%s: %v", cluster.Name, nodeErr)}
	}

	workloadByNode, workloadErr := l.loadClusterPodWorkloadByNode(cluster.UUID)
	clusterWarnings := make([]string, 0, 4)
	if workloadErr != nil {
		// 仅告警一次，避免按节点重复报错导致噪音和性能浪费。
		clusterWarnings = append(clusterWarnings, fmt.Sprintf("%s: 读取 Pod 归属失败，已回退名称推断: %v", cluster.Name, workloadErr))
	}

	var (
		nodeWg    sync.WaitGroup
		nodeSem   = make(chan struct{}, anomalyNodeConcurrency)
		clusterMu sync.Mutex
		items     = make([]types.MicroserviceAnomalyItem, 0, len(nodeNames)*4)
	)

	for _, n := range nodeNames {
		nodeNameLocal := n
		nodeWg.Add(1)
		go func() {
			defer nodeWg.Done()
			nodeSem <- struct{}{}
			defer func() { <-nodeSem }()

			pods, podErr := nodeOperator.GetNodePods(nodeNameLocal, timeRange)
			if podErr != nil {
				clusterMu.Lock()
				clusterWarnings = append(clusterWarnings, fmt.Sprintf("%s/%s: 查询 Pod 指标失败: %v", cluster.Name, nodeNameLocal, podErr))
				clusterMu.Unlock()
				return
			}
			if pods == nil || len(pods.PodList) == 0 {
				return
			}

			workloadMap := map[string]podWorkloadIdentity{}
			if nodeMap, ok := workloadByNode[nodeNameLocal]; ok {
				workloadMap = nodeMap
			}

			nodeItems := buildMicroserviceAnomalyItems(cluster, nodeNameLocal, pods.PodList, timeRange, minPowerWatts, detectedAt, workloadMap)
			if len(nodeItems) == 0 {
				return
			}

			clusterMu.Lock()
			items = append(items, nodeItems...)
			clusterMu.Unlock()
		}()
	}

	nodeWg.Wait()
	return items, clusterWarnings
}

func (l *GetNodeMicroserviceAnomalyLogic) listVisibleClusters() ([]visibleCluster, error) {
	const pageSize uint64 = 200
	result := make([]visibleCluster, 0, pageSize)
	page := uint64(1)

	for {
		resp, err := l.svcCtx.ManagerRpc.ClusterSearch(l.ctx, &managerservice.SearchClusterReq{
			Page:     page,
			PageSize: pageSize,
		})
		if err != nil {
			return nil, err
		}
		if resp == nil || len(resp.Data) == 0 {
			break
		}

		for _, cluster := range resp.Data {
			result = append(result, visibleCluster{
				UUID: cluster.GetUuid(),
				Name: cluster.GetName(),
			})
		}

		if uint64(len(result)) >= resp.GetTotal() || uint64(len(resp.Data)) < pageSize {
			break
		}
		page++
		if page > 100 {
			break
		}
	}

	return result, nil
}

func selectTargetClusters(clusters []visibleCluster, clusterUUID string) ([]visibleCluster, error) {
	if clusterUUID == "" {
		return clusters, nil
	}

	for _, cluster := range clusters {
		if cluster.UUID == clusterUUID {
			return []visibleCluster{cluster}, nil
		}
	}
	return nil, fmt.Errorf("当前账号无权限访问集群: %s", clusterUUID)
}

func (l *GetNodeMicroserviceAnomalyLogic) resolveNodeNames(clusterUUID, nodeName string, promClient pmtypes.PrometheusClient, timeRange *pmtypes.TimeRange) ([]string, error) {
	if nodeName != "" {
		return []string{nodeName}, nil
	}

	if cached := loadCachedAnomalyNodeNames(clusterUUID); len(cached) > 0 {
		return cached, nil
	}

	names := l.listNodeNamesFromPrometheus(promClient, timeRange)
	if len(names) > 0 {
		storeCachedAnomalyNodeNames(clusterUUID, names)
		return names, nil
	}
	nameSet := make(map[string]struct{}, 16)

	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUUID)
	if err != nil {
		return nil, fmt.Errorf("查询节点列表失败: %w", err)
	}

	nodeList, err := clusterClient.GetClientSet().CoreV1().Nodes().List(l.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("查询节点列表失败: %w", err)
	}

	for _, node := range nodeList.Items {
		if node.Name == "" {
			continue
		}
		if _, existed := nameSet[node.Name]; existed {
			continue
		}
		nameSet[node.Name] = struct{}{}
		names = append(names, node.Name)
	}

	if len(names) == 0 {
		return nil, fmt.Errorf("未发现可用节点")
	}

	sort.Strings(names)
	storeCachedAnomalyNodeNames(clusterUUID, names)
	return names, nil
}

func (l *GetNodeMicroserviceAnomalyLogic) listNodeNamesFromPrometheus(client pmtypes.PrometheusClient, timeRange *pmtypes.TimeRange) []string {
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

func (l *GetNodeMicroserviceAnomalyLogic) loadClusterPodWorkloadByNode(clusterUUID string) (map[string]map[string]podWorkloadIdentity, error) {
	if cached := loadCachedPodWorkloadByNode(clusterUUID); cached != nil {
		return cached, nil
	}

	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUUID)
	if err != nil {
		return nil, fmt.Errorf("获取集群客户端失败: %w", err)
	}

	podList, err := clusterClient.GetClientSet().CoreV1().Pods("").List(l.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("查询集群 Pod 列表失败: %w", err)
	}

	result := make(map[string]map[string]podWorkloadIdentity, 16)
	for _, pod := range podList.Items {
		nodeName := strings.TrimSpace(pod.Spec.NodeName)
		if nodeName == "" {
			continue
		}
		key := pod.Namespace + "/" + pod.Name
		nodeMap, exists := result[nodeName]
		if !exists {
			nodeMap = make(map[string]podWorkloadIdentity, 16)
			result[nodeName] = nodeMap
		}
		nodeMap[key] = buildPodWorkloadIdentity(pod)
	}
	storeCachedPodWorkloadByNode(clusterUUID, result)
	return result, nil
}

func loadCachedAnomalyNodeNames(clusterUUID string) []string {
	cachedRaw, ok := anomalyNodeNamesCache.Load(clusterUUID)
	if !ok {
		return nil
	}
	cached, ok := cachedRaw.(cachedNodeNamesValue)
	if !ok {
		anomalyNodeNamesCache.Delete(clusterUUID)
		return nil
	}
	if time.Now().After(cached.ExpiresAt) {
		anomalyNodeNamesCache.Delete(clusterUUID)
		return nil
	}
	return append([]string(nil), cached.Names...)
}

func storeCachedAnomalyNodeNames(clusterUUID string, names []string) {
	if clusterUUID == "" || len(names) == 0 {
		return
	}
	anomalyNodeNamesCache.Store(clusterUUID, cachedNodeNamesValue{
		Names:     append([]string(nil), names...),
		ExpiresAt: time.Now().Add(anomalyNodeNameCacheTTL),
	})
}

func loadCachedPodWorkloadByNode(clusterUUID string) map[string]map[string]podWorkloadIdentity {
	cachedRaw, ok := anomalyPodWorkloadCache.Load(clusterUUID)
	if !ok {
		return nil
	}
	cached, ok := cachedRaw.(cachedPodWorkloadByNodeValue)
	if !ok {
		anomalyPodWorkloadCache.Delete(clusterUUID)
		return nil
	}
	if time.Now().After(cached.ExpiresAt) {
		anomalyPodWorkloadCache.Delete(clusterUUID)
		return nil
	}
	return cached.Data
}

func storeCachedPodWorkloadByNode(clusterUUID string, data map[string]map[string]podWorkloadIdentity) {
	if clusterUUID == "" || len(data) == 0 {
		return
	}
	anomalyPodWorkloadCache.Store(clusterUUID, cachedPodWorkloadByNodeValue{
		Data:      data,
		ExpiresAt: time.Now().Add(anomalyPodWorkloadCacheTTL),
	})
}

func normalizeMicroserviceAnomalyTimeRange(timeRange *pmtypes.TimeRange) *pmtypes.TimeRange {
	now := time.Now()
	if timeRange == nil {
		return &pmtypes.TimeRange{
			Start: now.Add(-defaultAnomalyLookback),
			End:   now,
			Step:  defaultAnomalyStep,
		}
	}

	if timeRange.End.IsZero() {
		timeRange.End = now
	}
	if timeRange.Start.IsZero() {
		timeRange.Start = timeRange.End.Add(-defaultAnomalyLookback)
	}
	if !timeRange.End.After(timeRange.Start) {
		timeRange.End = timeRange.Start.Add(1 * time.Hour)
	}
	if timeRange.Step == "" {
		timeRange.Step = defaultAnomalyStep
	}
	return timeRange
}

func buildMicroserviceAnomalyItems(
	cluster visibleCluster,
	nodeName string,
	pods []pmtypes.NodePodBrief,
	timeRange *pmtypes.TimeRange,
	minPowerWatts float64,
	detectedAt int64,
	workloadMap map[string]podWorkloadIdentity,
) []types.MicroserviceAnomalyItem {
	durationHours := timeRange.End.Sub(timeRange.Start).Hours()
	if durationHours <= 0 {
		durationHours = defaultAnomalyLookback.Hours()
	}

	result := make([]types.MicroserviceAnomalyItem, 0, len(pods))
	for _, pod := range pods {
		currentPower := maxFloat64(pod.PodCurrentPowerWatts, 0)
		energyDeltaJoules := maxFloat64(pod.PodEnergyDeltaJoules, 0)
		if currentPower < minPowerWatts && energyDeltaJoules <= 0 {
			continue
		}

		baselinePower := estimateBaselinePowerWatts(currentPower, energyDeltaJoules, durationHours)
		deltaPercent := calculateDeltaPercent(currentPower, baselinePower)
		severity := classifyAnomalySeverity(deltaPercent)
		if severity == "" {
			continue
		}

		avgPower := 0.0
		if durationHours > 0 && energyDeltaJoules > 0 {
			avgPower = energyDeltaJoules / (durationHours * 3600)
		}

		service, workload := deriveServiceAndWorkload(pod.PodName)
		extraPowerKwh := estimateExtraPowerKwh(currentPower, baselinePower, durationHours)
		key := pod.Namespace + "/" + pod.PodName
		if identity, ok := workloadMap[key]; ok {
			if identity.Service != "" {
				service = identity.Service
			}
			if identity.Workload != "" {
				workload = identity.Workload
			}
		}

		anomalyType := anomalyTypeBySignal(severity, deltaPercent, currentPower, avgPower, extraPowerKwh)
		reason := buildAnomalyReason(currentPower, baselinePower, avgPower, deltaPercent, extraPowerKwh, energyDeltaJoules)

		result = append(result, types.MicroserviceAnomalyItem{
			Id:                 fmt.Sprintf("%s::%s::%s::%s", cluster.UUID, nodeName, pod.Namespace, pod.PodName),
			ClusterUuid:        cluster.UUID,
			ClusterName:        cluster.Name,
			NodeName:           nodeName,
			Namespace:          pod.Namespace,
			Service:            service,
			Workload:           workload,
			AnomalyType:        anomalyType,
			Reason:             reason,
			Severity:           severity,
			CurrentPowerWatts:  currentPower,
			BaselinePowerWatts: baselinePower,
			ExtraPowerKwh:      extraPowerKwh,
			DeltaPercent:       deltaPercent,
			DetectedAt:         detectedAt,
		})
	}

	return result
}

func estimateBaselinePowerWatts(currentPower, energyDeltaJoules, durationHours float64) float64 {
	if durationHours <= 0 {
		durationHours = defaultAnomalyLookback.Hours()
	}

	avgPower := 0.0
	if energyDeltaJoules > 0 {
		avgPower = energyDeltaJoules / (durationHours * 3600)
	}

	if avgPower > 0 {
		return maxFloat64(avgPower*dynamicBaselineFromAvgMultiplier, dynamicBaselineMinWatts)
	}
	if currentPower > 0 {
		return maxFloat64(currentPower*dynamicBaselineFromCurrentMultiplier, dynamicBaselineMinWatts)
	}

	return dynamicBaselineMinWatts
}

func calculateDeltaPercent(currentPower, baselinePower float64) float64 {
	if baselinePower <= 0 {
		return 0
	}
	return ((currentPower - baselinePower) / baselinePower) * 100
}

func classifyAnomalySeverity(deltaPercent float64) string {
	if deltaPercent >= 100 {
		return "critical"
	}
	if deltaPercent >= 50 {
		return "warning"
	}
	if deltaPercent >= 20 {
		return "info"
	}
	return ""
}

func anomalyTypeBySeverity(severity string) string {
	switch severity {
	case "critical":
		return "持续高功耗"
	case "warning":
		return "功耗高于基线"
	default:
		return "轻度偏离基线"
	}
}

func anomalyTypeBySignal(severity string, deltaPercent, currentPower, avgPower, extraPowerKwh float64) string {
	if severity == "critical" {
		if avgPower > 0 && currentPower >= avgPower*1.8 {
			return "瞬时功率突增"
		}
		if extraPowerKwh >= 0.8 {
			return "高额外能耗异常"
		}
		return "持续高功耗"
	}
	if severity == "warning" {
		if extraPowerKwh >= 0.5 {
			return "额外能耗偏高"
		}
		if deltaPercent >= 70 {
			return "功耗显著高于基线"
		}
		return "功耗高于基线"
	}
	if avgPower > 0 && currentPower >= avgPower*1.5 {
		return "轻度峰值抬升"
	}
	return anomalyTypeBySeverity(severity)
}

func estimateExtraPowerKwh(currentPower, baselinePower, durationHours float64) float64 {
	extraPower := currentPower - baselinePower
	if extraPower <= 0 {
		return 0
	}
	return (extraPower * durationHours) / 1000
}

func buildAnomalyReason(
	currentPower, baselinePower, avgPower, deltaPercent, extraPowerKwh, energyDeltaJoules float64,
) string {
	reasons := []string{
		fmt.Sprintf("高于基线 %.1f%%", deltaPercent),
	}

	if avgPower > 0 && currentPower >= avgPower*1.5 {
		reasons = append(reasons, fmt.Sprintf("当前 %.2fW 明显高于窗口均值 %.2fW", currentPower, avgPower))
	} else {
		reasons = append(reasons, fmt.Sprintf("当前 %.2fW，基线 %.2fW", currentPower, baselinePower))
	}

	if extraPowerKwh > 0 {
		reasons = append(reasons, fmt.Sprintf("额外能耗 %.3fkWh", extraPowerKwh))
	}
	if energyDeltaJoules > 0 {
		reasons = append(reasons, fmt.Sprintf("窗口总能耗 %.3fkWh", energyDeltaJoules/3_600_000))
	}

	return strings.Join(reasons, "；")
}

func deriveServiceAndWorkload(podName string) (service string, workload string) {
	if podName == "" {
		return "", ""
	}

	parts := strings.Split(podName, "-")
	if len(parts) >= 3 && isLikelyReplicaSetHash(parts[len(parts)-2]) && isLikelyPodSuffix(parts[len(parts)-1]) {
		base := strings.Join(parts[:len(parts)-2], "-")
		return base, base
	}

	if len(parts) >= 2 && isLikelyReplicaSetHash(parts[len(parts)-1]) {
		base := strings.Join(parts[:len(parts)-1], "-")
		return base, base
	}

	return podName, podName
}

func isLikelyReplicaSetHash(s string) bool {
	if len(s) < 6 || len(s) > 12 {
		return false
	}
	for _, ch := range s {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func isLikelyPodSuffix(s string) bool {
	if len(s) < 4 || len(s) > 8 {
		return false
	}
	for _, ch := range s {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func buildPodWorkloadIdentity(pod corev1.Pod) podWorkloadIdentity {
	ownerKind, ownerName := resolvePodOwner(pod.OwnerReferences)
	if ownerKind == "ReplicaSet" {
		if deploymentName := trimReplicaSetHash(ownerName); deploymentName != "" {
			ownerKind = "Deployment"
			ownerName = deploymentName
		}
	}

	service := firstNonEmpty(
		pod.Labels["app.kubernetes.io/name"],
		pod.Labels["k8s-app"],
		pod.Labels["app"],
		pod.Labels["app.kubernetes.io/component"],
	)
	if service == "" {
		service = ownerName
	}
	if service == "" {
		guessedService, _ := deriveServiceAndWorkload(pod.Name)
		service = guessedService
	}

	workloadName := ownerName
	if workloadName == "" {
		_, guessedWorkload := deriveServiceAndWorkload(pod.Name)
		workloadName = guessedWorkload
	}

	workload := workloadName
	if ownerKind != "" && workloadName != "" {
		workload = ownerKind + "/" + workloadName
	}
	if workload == "" {
		workload = service
	}

	return podWorkloadIdentity{
		Service:    service,
		Workload:   workload,
		OwnerKind:  ownerKind,
		OwnerName:  ownerName,
		PodNameRaw: pod.Name,
	}
}

func resolvePodOwner(ownerRefs []metav1.OwnerReference) (kind string, name string) {
	for _, ref := range ownerRefs {
		if ref.Controller != nil && *ref.Controller {
			return strings.TrimSpace(ref.Kind), strings.TrimSpace(ref.Name)
		}
	}
	if len(ownerRefs) > 0 {
		return strings.TrimSpace(ownerRefs[0].Kind), strings.TrimSpace(ownerRefs[0].Name)
	}
	return "", ""
}

func trimReplicaSetHash(replicaSetName string) string {
	parts := strings.Split(replicaSetName, "-")
	if len(parts) < 2 {
		return ""
	}
	last := parts[len(parts)-1]
	if !isLikelyReplicaSetHash(last) {
		return ""
	}
	base := strings.Join(parts[:len(parts)-1], "-")
	return strings.TrimSpace(base)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v != "" {
			return v
		}
	}
	return ""
}

func summarizeMicroserviceAnomaly(items []types.MicroserviceAnomalyItem) types.MicroserviceAnomalySummary {
	clusterSet := make(map[string]struct{}, len(items))
	highRiskSet := make(map[string]struct{}, len(items))
	totalExtraPower := 0.0

	for _, item := range items {
		clusterSet[item.ClusterUuid] = struct{}{}
		if item.Severity == "critical" {
			highRiskSet[item.Namespace+"/"+item.Workload] = struct{}{}
		}
		totalExtraPower += item.ExtraPowerKwh
	}

	return types.MicroserviceAnomalySummary{
		TotalEvents:      int64(len(items)),
		HighRiskServices: int64(len(highRiskSet)),
		RelatedClusters:  int64(len(clusterSet)),
		ExtraPowerKwh:    totalExtraPower,
	}
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}

	unique := make([]string, 0, len(values))
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, existed := set[value]; existed {
			continue
		}
		set[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func maxFloat64(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
