package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yanshicheng/kube-nova/application/manager-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-api/internal/types"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"
	"github.com/zeromicro/go-zero/core/logx"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	rlScheduleModelVersion = "maskable-ppo-two-level-v0.1"
	rlStorageSocMin        = 20.0
	bytesPerGiB            = 1024.0 * 1024.0 * 1024.0
)

var nodeCurrentPowerAnnotationKeys = []string{
	"green-scheduler.kube-nova.io/current-power-w",
	"kube-nova.io/current-power-w",
	"energy.kube-nova.io/current-power-w",
}

const prometheusNodeGroupLabels = "node_name,node,instance,host,hostname,Hostname,kubernetes_node"

var prometheusNodePowerQueries = []string{
	"sum by (" + prometheusNodeGroupLabels + ") (kepler_node_power_total)",
	"sum by (" + prometheusNodeGroupLabels + ") (node_current_power_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (green_scheduler_node_power_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (node_power_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (node_hwmon_power_average_watt)",
	"sum by (" + prometheusNodeGroupLabels + ") (node_hwmon_power_input_watt)",
	"sum by (" + prometheusNodeGroupLabels + ") (ipmi_dcmi_power_consumption_current_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (ipmi_dcmi_power_consumption_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (ipmi_power_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (kepler_node_cpu_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (kepler_node_cpu_active_watts + kepler_node_cpu_idle_watts)",
	"sum by (" + prometheusNodeGroupLabels + ") (DCGM_FI_DEV_POWER_USAGE)",
}

var prometheusStorageSocQueries = []string{
	"min(green_scheduler_storage_soc_percent)",
	"min(kube_nova_storage_soc_percent)",
	"min(energy_storage_soc_percent)",
	"min(storage_soc_percent)",
	"min(battery_soc_percent)",
	"min(nut_ups_battery_charge)",
}

var gpuResourceNames = []corev1.ResourceName{
	corev1.ResourceName("nvidia.com/gpu"),
	corev1.ResourceName("requests.nvidia.com/gpu"),
	corev1.ResourceName("k8s.amazonaws.com/accelerator"),
}

var prometheusDiscoveryNamespaces = []string{
	"monitoring",
	"prometheus",
	"kube-prometheus",
	"observability",
	"default",
}

type BuildRlSchedulePlanLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

type projectClusterScheduleScope struct {
	cluster        *pb.OnecProjectCluster
	workspace      *pb.OnecProjectWorkspace
	workspaceBound bool
}

// 生成电-储-算感知分层强化学习调度计划
func NewBuildRlSchedulePlanLogic(ctx context.Context, svcCtx *svc.ServiceContext) *BuildRlSchedulePlanLogic {
	return &BuildRlSchedulePlanLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *BuildRlSchedulePlanLogic) BuildRlSchedulePlan(req *types.BuildRlSchedulePlanRequest) (*types.BuildRlSchedulePlanResponse, error) {
	clusters, err := l.loadProjectClusters(req)
	if err != nil {
		return nil, err
	}

	clusterCandidates := make([]types.RlScheduleClusterCandidate, 0, len(clusters))
	for _, scope := range clusters {
		if scope.cluster == nil {
			continue
		}
		candidate := l.buildClusterCandidate(scope.cluster, scope.workspace, scope.workspaceBound, req.Service)
		l.applyWorkspaceQuotaMask(&candidate, scope.workspace, req.Service)
		clusterCandidates = append(clusterCandidates, candidate)
	}

	targetIndex := l.selectClusterIndex(clusterCandidates, req.Service)
	if targetIndex < 0 {
		sortClusterCandidates(clusterCandidates)
		reason := ""
		if l.rlPolicyRequired() && hasSelectableCluster(clusterCandidates) {
			reason = "RL policy service is not ready or returned an invalid cluster action; strict mode stops heuristic fallback"
		}
		resp := &types.BuildRlSchedulePlanResponse{
			PlanId:            makeRlPlanId(),
			ModelVersion:      rlScheduleModelVersion,
			ClusterCandidates: clusterCandidates,
			NodeCandidates:    []types.RlScheduleNodeCandidate{},
			Placements:        []types.RlReplicaPlacement{},
			Reason:            "没有满足工作空间可调度范围、配额、集群资源和储能 SOC 约束的候选集群",
			Executable:        false,
		}
		if reason != "" {
			resp.Reason = reason
		}
		if err := l.persistSchedulePlan(req, resp); err != nil {
			return nil, err
		}
		l.recordSchedulePlanAudit(req, resp)
		return resp, nil
	}

	clusterCandidates[targetIndex].Selected = true
	targetCluster := clusterCandidates[targetIndex]
	sortClusterCandidates(clusterCandidates)

	nodes, err := l.loadNodeCandidates(targetCluster.ClusterUuid, req.Service)
	if err != nil {
		return nil, err
	}
	placementNodes, placements, executable, reason := l.placeReplicas(targetCluster, nodes, req.Service)
	syncSelectedClusterPowerFromNodes(&targetCluster, clusterCandidates, placementNodes)

	resp := &types.BuildRlSchedulePlanResponse{
		PlanId:            makeRlPlanId(),
		ModelVersion:      rlScheduleModelVersion,
		TargetCluster:     targetCluster,
		ClusterCandidates: clusterCandidates,
		NodeCandidates:    placementNodes,
		Placements:        placements,
		Reason:            reason,
		Executable:        executable,
	}
	if err := l.persistSchedulePlan(req, resp); err != nil {
		return nil, err
	}
	l.recordSchedulePlanAudit(req, resp)
	return resp, nil
}

func (l *BuildRlSchedulePlanLogic) loadProjectClusters(req *types.BuildRlSchedulePlanRequest) ([]projectClusterScheduleScope, error) {
	workspaceResp, err := l.svcCtx.ManagerRpc.ProjectWorkspaceGetById(l.ctx, &pb.GetOnecProjectWorkspaceByIdReq{
		Id: req.WorkspaceId,
	})
	if err != nil {
		l.Errorf("ProjectWorkspaceGetById failed: %v", err)
		return nil, errorx.Msg("查询工作空间失败")
	}
	workspace := workspaceResp.GetData()
	if workspace == nil || workspace.ProjectClusterId == 0 {
		return nil, errorx.Msg("工作空间未绑定可调度集群")
	}

	currentClusterResp, err := l.svcCtx.ManagerRpc.ProjectClusterGetById(l.ctx, &pb.GetOnecProjectClusterByIdReq{
		Id: workspace.ProjectClusterId,
	})
	if err != nil {
		l.Errorf("ProjectClusterGetById failed: %v", err)
		return nil, errorx.Msg("查询工作空间绑定集群失败")
	}
	currentCluster := currentClusterResp.GetData()
	if currentCluster == nil {
		return []projectClusterScheduleScope{}, nil
	}

	projectId := req.ProjectId
	if projectId == 0 {
		projectId = currentCluster.ProjectId
	}
	if projectId == 0 {
		return []projectClusterScheduleScope{{cluster: currentCluster, workspace: workspace, workspaceBound: true}}, nil
	}

	resp, err := l.svcCtx.ManagerRpc.ProjectClusterSearch(l.ctx, &pb.SearchOnecProjectClusterReq{
		ProjectId: projectId,
	})
	if err != nil {
		l.Errorf("ProjectClusterSearch failed: %v", err)
		return nil, errorx.Msg("查询项目授权集群失败")
	}

	candidates := make([]projectClusterScheduleScope, 0, len(resp.GetData()))
	seen := map[uint64]bool{}
	for _, cluster := range resp.GetData() {
		if cluster == nil || cluster.Id == 0 {
			continue
		}
		seen[cluster.Id] = true
		candidateWorkspace := l.workspaceOnProjectCluster(cluster.Id, workspace.Namespace, workspace.Name)
		candidates = append(candidates, projectClusterScheduleScope{
			cluster:        cluster,
			workspace:      candidateWorkspace,
			workspaceBound: candidateWorkspace != nil,
		})
	}
	if !seen[currentCluster.Id] {
		candidates = append(candidates, projectClusterScheduleScope{
			cluster:        currentCluster,
			workspace:      workspace,
			workspaceBound: true,
		})
	} else {
		for i := range candidates {
			if candidates[i].cluster != nil && candidates[i].cluster.Id == currentCluster.Id {
				candidates[i].workspace = workspace
				candidates[i].workspaceBound = true
				break
			}
		}
	}
	if len(candidates) > 0 {
		return candidates, nil
	}

	// 兼容历史数据：如果项目内还没有同 namespace 的多集群工作空间，只保留当前工作空间绑定集群。
	return []projectClusterScheduleScope{{cluster: currentCluster, workspace: workspace, workspaceBound: true}}, nil
}

func (l *BuildRlSchedulePlanLogic) workspaceOnProjectCluster(projectClusterId uint64, namespace string, workspaceName string) *pb.OnecProjectWorkspace {
	if projectClusterId == 0 || strings.TrimSpace(namespace) == "" || strings.TrimSpace(workspaceName) == "" {
		return nil
	}
	resp, err := l.svcCtx.ManagerRpc.ProjectWorkspaceSearch(l.ctx, &pb.SearchOnecProjectWorkspaceReq{
		ProjectClusterId: projectClusterId,
		Name:             workspaceName,
		Namespace:        namespace,
	})
	if err != nil {
		l.Errorf("ProjectWorkspaceSearch failed when filtering schedule candidates: projectClusterId=%d namespace=%s err=%v", projectClusterId, namespace, err)
		return nil
	}
	for _, workspace := range resp.GetData() {
		if workspace != nil && workspace.GetNamespace() == namespace && workspace.GetName() == workspaceName {
			return workspace
		}
	}
	return nil
}

func (l *BuildRlSchedulePlanLogic) loadClusterRegion(clusterUuid string) string {
	if strings.TrimSpace(clusterUuid) == "" || l == nil || l.svcCtx == nil {
		return ""
	}
	resp, err := l.svcCtx.ManagerRpc.ClusterSearch(l.ctx, &pb.SearchClusterReq{
		Page:     1,
		PageSize: 1,
		Uuid:     clusterUuid,
	})
	if err != nil {
		l.Errorf("ClusterSearch failed when loading schedule cluster region: clusterUuid=%s err=%v", clusterUuid, err)
		return ""
	}
	for _, cluster := range resp.GetData() {
		if cluster == nil || cluster.GetUuid() != clusterUuid || cluster.GetId() == 0 {
			continue
		}
		detail, err := l.svcCtx.ManagerRpc.ClusterDetail(l.ctx, &pb.ClusterDetailReq{Id: cluster.GetId()})
		if err != nil {
			l.Errorf("ClusterDetail failed when loading schedule cluster region: clusterUuid=%s id=%d err=%v", clusterUuid, cluster.GetId(), err)
			return ""
		}
		return strings.TrimSpace(detail.GetRegion())
	}
	return ""
}

func (l *BuildRlSchedulePlanLogic) loadNodeCandidates(clusterUuid string, demand types.RlScheduleServiceDemand) ([]types.RlScheduleNodeCandidate, error) {
	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUuid)
	if err != nil {
		l.Errorf("GetCluster failed when loading schedule nodes: clusterUuid=%s err=%v", clusterUuid, err)
		return nil, errorx.Msg("query target cluster client failed")
	}
	kubeClient := clusterClient.GetKubeClient()

	nodeList, err := kubeClient.CoreV1().Nodes().List(l.ctx, metav1.ListOptions{})
	if err != nil {
		l.Errorf("list nodes failed when loading schedule nodes: clusterUuid=%s err=%v", clusterUuid, err)
		return nil, errorx.Msg("query target cluster nodes failed")
	}
	podList, err := kubeClient.CoreV1().Pods(metav1.NamespaceAll).List(l.ctx, metav1.ListOptions{})
	if err != nil {
		l.Errorf("list pods failed when loading schedule nodes: clusterUuid=%s err=%v", clusterUuid, err)
		return nil, errorx.Msg("query target cluster pods failed")
	}
	powerByNode := l.loadNodePowerReadings(clusterUuid, nodeList.Items)

	usedByNode := map[string]nodeRequestedResources{}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !podConsumesNodeResources(pod) {
			continue
		}
		used := usedByNode[pod.Spec.NodeName]
		requested := podRequestedResources(pod)
		used.cpuCores += requested.cpuCores
		used.memoryGiB += requested.memoryGiB
		used.gpu += requested.gpu
		used.pods++
		usedByNode[pod.Spec.NodeName] = used
	}

	nodes := make([]types.RlScheduleNodeCandidate, 0, len(nodeList.Items))
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		allocatable := nodeAllocatableResources(node)
		used := usedByNode[node.Name]
		cpuFree := math.Max(0, allocatable.cpuCores-used.cpuCores)
		memFree := math.Max(0, allocatable.memoryGiB-used.memoryGiB)
		gpuFree := math.Max(0, allocatable.gpu-used.gpu)
		podsFree := allocatable.pods - used.pods
		if podsFree < 0 {
			podsFree = 0
		}
		currentPowerW, hasCurrentPower := nodePowerReadingFor(node, powerByNode)

		candidate := types.RlScheduleNodeCandidate{
			ClusterUuid:   clusterUuid,
			NodeName:      node.Name,
			NodeStatus:    nodeReadyStatus(node),
			Unschedulable: nodeUnschedulableValue(node),
			CpuFreeCores:  roundFloat(cpuFree, 3),
			MemoryFreeGiB: roundFloat(memFree, 3),
			GpuFree:       roundFloat(gpuFree, 3),
			PodsFree:      podsFree,
			MaskReason:    "available node action",
		}
		if hasCurrentPower {
			candidate.HasCurrentPower = true
			candidate.CurrentPowerW = roundFloat(currentPowerW, 3)
		}
		if !isReadyNode(candidate.NodeStatus) {
			candidate.ActionMasked = true
			candidate.MaskReason = "node is not Ready"
		} else if isUnschedulableNode(candidate.Unschedulable) {
			candidate.ActionMasked = true
			candidate.MaskReason = "node is unschedulable"
		} else if cpuFree+1e-9 < demand.CpuRequestCores {
			candidate.ActionMasked = true
			candidate.MaskReason = "node CPU free is insufficient"
		} else if memFree+1e-9 < demand.MemoryRequestGiB {
			candidate.ActionMasked = true
			candidate.MaskReason = "node memory free is insufficient"
		} else if gpuFree+1e-9 < demand.GpuRequest {
			candidate.ActionMasked = true
			candidate.MaskReason = "node GPU free is insufficient"
		} else if podsFree < 1 {
			candidate.ActionMasked = true
			candidate.MaskReason = "node pod capacity is insufficient"
		}
		nodes = append(nodes, candidate)
	}
	return nodes, nil
}

func (l *BuildRlSchedulePlanLogic) applyWorkspaceQuotaMask(candidate *types.RlScheduleClusterCandidate, workspace *pb.OnecProjectWorkspace, demand types.RlScheduleServiceDemand) {
	if candidate == nil || candidate.ActionMasked || workspace == nil {
		return
	}
	quota, err := l.loadWorkspaceResourceQuota(candidate.ClusterUuid, workspace.GetNamespace(), workspace.GetName())
	if err != nil {
		l.Errorf("load workspace ResourceQuota failed: clusterUuid=%s namespace=%s workspace=%s err=%v", candidate.ClusterUuid, workspace.GetNamespace(), workspace.GetName(), err)
		candidate.ActionMasked = true
		candidate.MaskReason = "工作空间 ResourceQuota 不可用"
		return
	}

	requiredCpu := demand.CpuRequestCores * float64(demand.Replicas)
	requiredMem := demand.MemoryRequestGiB * float64(demand.Replicas)
	requiredGpu := demand.GpuRequest * float64(demand.Replicas)
	requiredPods := demand.Replicas

	if ok, reason := quotaHasFloatFree(quota, corev1.ResourceRequestsCPU, requiredCpu, quantityCores); !ok {
		candidate.ActionMasked = true
		candidate.MaskReason = "工作空间 CPU 配额余量不足: " + reason
		return
	}
	if ok, reason := quotaHasFloatFree(quota, corev1.ResourceRequestsMemory, requiredMem, quantityGiB); !ok {
		candidate.ActionMasked = true
		candidate.MaskReason = "工作空间内存配额余量不足: " + reason
		return
	}
	if requiredGpu > 0 {
		if ok, reason := quotaHasGpuFree(quota, requiredGpu); !ok {
			candidate.ActionMasked = true
			candidate.MaskReason = "工作空间 GPU 配额余量不足: " + reason
			return
		}
	}
	if ok, reason := quotaHasIntFree(quota, corev1.ResourcePods, requiredPods); !ok {
		candidate.ActionMasked = true
		candidate.MaskReason = "工作空间 Pod 配额余量不足: " + reason
		return
	}
}

func (l *BuildRlSchedulePlanLogic) loadWorkspaceResourceQuota(clusterUuid, namespace, workspaceName string) (*corev1.ResourceQuota, error) {
	if strings.TrimSpace(clusterUuid) == "" || strings.TrimSpace(namespace) == "" || strings.TrimSpace(workspaceName) == "" {
		return nil, fmt.Errorf("empty cluster uuid, namespace, or workspace name")
	}
	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUuid)
	if err != nil {
		return nil, err
	}
	kubeClient := clusterClient.GetKubeClient()
	quotaName := workspacePolicyName(namespace)
	if quota, err := kubeClient.CoreV1().ResourceQuotas(namespace).Get(l.ctx, quotaName, metav1.GetOptions{}); err == nil {
		return quota, nil
	}
	if legacyName := legacyWorkspacePolicyName(workspaceName); legacyName != quotaName {
		if quota, err := kubeClient.CoreV1().ResourceQuotas(namespace).Get(l.ctx, legacyName, metav1.GetOptions{}); err == nil {
			return quota, nil
		}
	}
	quotaList, err := kubeClient.CoreV1().ResourceQuotas(namespace).List(l.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range quotaList.Items {
		if strings.HasPrefix(quotaList.Items[i].Name, "ikubeops") {
			return &quotaList.Items[i], nil
		}
	}
	if len(quotaList.Items) == 1 {
		return &quotaList.Items[0], nil
	}
	return nil, fmt.Errorf("workspace ResourceQuota %s not found in namespace %s", quotaName, namespace)
}

func (l *BuildRlSchedulePlanLogic) loadClusterRealtimeFreeResources(clusterUuid string) (nodeRequestedResources, error) {
	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUuid)
	if err != nil {
		return nodeRequestedResources{}, fmt.Errorf("连接 Kubernetes 集群失败: %w", err)
	}
	kubeClient := clusterClient.GetKubeClient()

	nodeList, err := kubeClient.CoreV1().Nodes().List(l.ctx, metav1.ListOptions{})
	if err != nil {
		return nodeRequestedResources{}, fmt.Errorf("读取节点列表失败: %w", err)
	}
	podList, err := kubeClient.CoreV1().Pods(metav1.NamespaceAll).List(l.ctx, metav1.ListOptions{})
	if err != nil {
		return nodeRequestedResources{}, fmt.Errorf("读取 Pod 列表失败: %w", err)
	}

	usedByNode := map[string]nodeRequestedResources{}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !podConsumesNodeResources(pod) {
			continue
		}
		used := usedByNode[pod.Spec.NodeName]
		requested := podRequestedResources(pod)
		used.cpuCores += requested.cpuCores
		used.memoryGiB += requested.memoryGiB
		used.gpu += requested.gpu
		used.pods++
		usedByNode[pod.Spec.NodeName] = used
	}

	total := nodeRequestedResources{}
	missingPower := false
	powerByNode := l.loadNodePowerReadings(clusterUuid, nodeList.Items)
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		if currentPowerW, ok := nodePowerReadingFor(node, powerByNode); ok {
			total.currentPowerW += currentPowerW
		} else {
			missingPower = true
		}
		if nodeReadyStatus(node) != "Ready" || node.Spec.Unschedulable {
			continue
		}
		allocatable := nodeAllocatableResources(node)
		used := usedByNode[node.Name]
		total.cpuCores += math.Max(0, allocatable.cpuCores-used.cpuCores)
		total.memoryGiB += math.Max(0, allocatable.memoryGiB-used.memoryGiB)
		total.gpu += math.Max(0, allocatable.gpu-used.gpu)
		podsFree := allocatable.pods - used.pods
		if podsFree > 0 {
			total.pods += podsFree
		}
	}
	total.hasCurrentPower = len(nodeList.Items) > 0 && !missingPower
	return total, nil
}

func (l *BuildRlSchedulePlanLogic) buildClusterCandidate(cluster *pb.OnecProjectCluster, workspace *pb.OnecProjectWorkspace, workspaceBound bool, demand types.RlScheduleServiceDemand) types.RlScheduleClusterCandidate {
	requiredCpu := demand.CpuRequestCores * float64(demand.Replicas)
	requiredMem := demand.MemoryRequestGiB * float64(demand.Replicas)
	requiredGpu := demand.GpuRequest * float64(demand.Replicas)

	realtimeFree := nodeRequestedResources{}
	var realtimeErr error
	if strings.TrimSpace(cluster.ClusterUuid) != "" {
		realtimeFree, realtimeErr = l.loadClusterRealtimeFreeResources(cluster.ClusterUuid)
		if realtimeErr != nil {
			l.Errorf("load realtime cluster resources failed: clusterUuid=%s err=%v", cluster.ClusterUuid, realtimeErr)
		}
	}
	region := l.loadClusterRegion(cluster.ClusterUuid)

	candidate := types.RlScheduleClusterCandidate{
		ProjectClusterId:      cluster.Id,
		ClusterUuid:           cluster.ClusterUuid,
		ClusterName:           cluster.ClusterName,
		Region:                region,
		Authorized:            workspaceBound,
		Status:                cluster.Status,
		CpuFreeCores:          roundFloat(realtimeFree.cpuCores, 3),
		MemoryFreeGiB:         roundFloat(realtimeFree.memoryGiB, 3),
		GpuFree:               roundFloat(realtimeFree.gpu, 3),
		RealtimeResourceReady: realtimeErr == nil && strings.TrimSpace(cluster.ClusterUuid) != "",
		RealtimeResourceError: errorText(realtimeErr),
		GridPricePerKwh:       0,
		HasEnergyProfile:      false,
		StorageSoc:            0,
		HasStorageSoc:         false,
		HasCurrentPower:       realtimeFree.hasCurrentPower,
		CurrentPowerW:         roundFloat(realtimeFree.currentPowerW, 3),
		MaskReason:            "可作为跨集群动作候选",
	}
	if workspace != nil {
		candidate.TargetWorkspaceId = workspace.Id
		candidate.Namespace = workspace.Namespace
		candidate.WorkspaceName = workspace.Name
	}

	switch {
	case candidate.ClusterUuid == "":
		candidate.ActionMasked = true
		candidate.MaskReason = "集群 UUID 为空"
	case !workspaceBound:
		candidate.ActionMasked = true
		candidate.MaskReason = "不在当前工作空间可调度范围内"
	case !isNormalClusterStatus(candidate.Status):
		candidate.ActionMasked = true
		candidate.MaskReason = "集群状态不可调度"
	case realtimeErr != nil:
		candidate.ActionMasked = true
		candidate.MaskReason = "集群实时资源不可用: " + errorText(realtimeErr)
	case realtimeFree.cpuCores+1e-9 < requiredCpu:
		candidate.ActionMasked = true
		candidate.MaskReason = "CPU 余量不足"
	case realtimeFree.memoryGiB+1e-9 < requiredMem:
		candidate.ActionMasked = true
		candidate.MaskReason = "内存余量不足"
	case realtimeFree.gpu+1e-9 < requiredGpu:
		candidate.ActionMasked = true
		candidate.MaskReason = "GPU 余量不足"
	case realtimeFree.pods < demand.Replicas:
		candidate.ActionMasked = true
		candidate.MaskReason = "Pod 余量不足"
	case candidate.HasStorageSoc && candidate.StorageSoc < rlStorageSocMin:
		candidate.ActionMasked = true
		candidate.MaskReason = "储能 SOC 低于安全阈值"
	}

	return candidate
}

func (l *BuildRlSchedulePlanLogic) selectClusterIndex(candidates []types.RlScheduleClusterCandidate, demand types.RlScheduleServiceDemand) int {
	if index, ok := l.predictClusterIndex(candidates, demand); ok {
		return index
	}
	if l.rlPolicyRequired() && hasSelectableCluster(candidates) {
		return -1
	}

	best := -1
	bestScore := math.Inf(-1)
	for i := range candidates {
		if candidates[i].ActionMasked {
			continue
		}
		score := l.clusterPolicyScore(candidates[i], demand)
		if best < 0 || score > bestScore+1e-9 || (math.Abs(score-bestScore) <= 1e-9 && compareCluster(candidates[i], candidates[best]) < 0) {
			best = i
			bestScore = score
		}
	}
	return best
}

func hasSelectableCluster(candidates []types.RlScheduleClusterCandidate) bool {
	for i := range candidates {
		if !candidates[i].ActionMasked {
			return true
		}
	}
	return false
}

func compareCluster(a, b types.RlScheduleClusterCandidate) int {
	ap := sortablePrice(a)
	bp := sortablePrice(b)
	if ap != bp {
		if ap < bp {
			return -1
		}
		return 1
	}
	if a.StorageSoc != b.StorageSoc {
		if a.StorageSoc > b.StorageSoc {
			return -1
		}
		return 1
	}
	apw := sortablePower(a.HasCurrentPower, a.CurrentPowerW)
	bpw := sortablePower(b.HasCurrentPower, b.CurrentPowerW)
	if apw != bpw {
		if apw < bpw {
			return -1
		}
		return 1
	}
	if a.CpuFreeCores != b.CpuFreeCores {
		if a.CpuFreeCores > b.CpuFreeCores {
			return -1
		}
		return 1
	}
	return strings.Compare(a.ClusterName, b.ClusterName)
}

func sortClusterCandidates(candidates []types.RlScheduleClusterCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Selected != candidates[j].Selected {
			return candidates[i].Selected
		}
		if candidates[i].ActionMasked != candidates[j].ActionMasked {
			return !candidates[i].ActionMasked
		}
		return compareCluster(candidates[i], candidates[j]) < 0
	})
}

func syncSelectedClusterPowerFromNodes(
	target *types.RlScheduleClusterCandidate,
	candidates []types.RlScheduleClusterCandidate,
	nodes []types.RlScheduleNodeCandidate,
) {
	if target == nil || len(nodes) == 0 {
		return
	}
	total := 0.0
	for _, node := range nodes {
		if !node.HasCurrentPower || !validCollectedPower(node.CurrentPowerW) {
			return
		}
		total += node.CurrentPowerW
	}
	total = roundFloat(total, 3)
	target.HasCurrentPower = true
	target.CurrentPowerW = total
	for i := range candidates {
		if !candidates[i].Selected {
			continue
		}
		if candidates[i].ClusterUuid != target.ClusterUuid {
			continue
		}
		candidates[i].HasCurrentPower = true
		candidates[i].CurrentPowerW = total
		return
	}
}

func (l *BuildRlSchedulePlanLogic) placeReplicas(
	targetCluster types.RlScheduleClusterCandidate,
	nodes []types.RlScheduleNodeCandidate,
	demand types.RlScheduleServiceDemand,
) ([]types.RlScheduleNodeCandidate, []types.RlReplicaPlacement, bool, string) {
	placements := make([]types.RlReplicaPlacement, 0, demand.Replicas)
	assigned := map[string]int64{}

	for replica := int64(0); replica < demand.Replicas; replica++ {
		best := -1
		if index, ok := l.predictNodeIndex(targetCluster.ClusterUuid, nodes, demand, assigned, replica); ok {
			best = index
		} else if l.rlPolicyRequired() && hasFeasibleNode(nodes, demand) {
			applyAssigned(nodes, assigned)
			sortNodeCandidates(nodes, assigned)
			return nodes, placements, false, "RL node policy service is not ready or returned an invalid node action; strict mode stops heuristic fallback"
		}
		bestScore := math.Inf(-1)
		for i := range nodes {
			if best >= 0 {
				break
			}
			if nodes[i].ActionMasked {
				continue
			}
			if !nodeCanFitReplica(nodes[i], demand) {
				continue
			}
			score := l.nodePolicyScore(targetCluster.ClusterUuid, nodes[i], demand, assigned)
			if best < 0 || score > bestScore+1e-9 || (math.Abs(score-bestScore) <= 1e-9 && compareNode(nodes[i], nodes[best], assigned) < 0) {
				best = i
				bestScore = score
			}
		}
		if best < 0 {
			applyAssigned(nodes, assigned)
			return nodes, placements, false, fmt.Sprintf("已放置 %d/%d 个副本，剩余副本没有可行动作候选", len(placements), demand.Replicas)
		}
		name := nodes[best].NodeName
		assigned[name]++
		reserveReplicaOnNode(&nodes[best], demand)
		placements = append(placements, types.RlReplicaPlacement{
			ReplicaName: fmt.Sprintf("%s-%d", normalizedServiceName(demand.ServiceName), replica),
			NodeName:    name,
		})
	}

	applyAssigned(nodes, assigned)
	sortNodeCandidates(nodes, assigned)
	return nodes, placements, true, fmt.Sprintf("RL 策略选择 %s，完成 %d 个副本的节点分布", targetCluster.ClusterName, len(placements))
}

func compareNode(a, b types.RlScheduleNodeCandidate, assigned map[string]int64) int {
	aa := assigned[a.NodeName]
	ba := assigned[b.NodeName]
	if aa != ba {
		if aa < ba {
			return -1
		}
		return 1
	}
	ap := sortablePower(a.HasCurrentPower, a.CurrentPowerW)
	bp := sortablePower(b.HasCurrentPower, b.CurrentPowerW)
	if ap != bp {
		if ap < bp {
			return -1
		}
		return 1
	}
	if a.CpuFreeCores != b.CpuFreeCores {
		if a.CpuFreeCores > b.CpuFreeCores {
			return -1
		}
		return 1
	}
	if a.MemoryFreeGiB != b.MemoryFreeGiB {
		if a.MemoryFreeGiB > b.MemoryFreeGiB {
			return -1
		}
		return 1
	}
	if a.GpuFree != b.GpuFree {
		if a.GpuFree > b.GpuFree {
			return -1
		}
		return 1
	}
	if a.PodsFree != b.PodsFree {
		if a.PodsFree > b.PodsFree {
			return -1
		}
		return 1
	}
	return strings.Compare(a.NodeName, b.NodeName)
}

func sortNodeCandidates(nodes []types.RlScheduleNodeCandidate, assigned map[string]int64) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].AssignedReplicas != nodes[j].AssignedReplicas {
			return nodes[i].AssignedReplicas > nodes[j].AssignedReplicas
		}
		if nodes[i].ActionMasked != nodes[j].ActionMasked {
			return !nodes[i].ActionMasked
		}
		return compareNode(nodes[i], nodes[j], assigned) < 0
	})
}

func applyAssigned(nodes []types.RlScheduleNodeCandidate, assigned map[string]int64) {
	for i := range nodes {
		nodes[i].AssignedReplicas = assigned[nodes[i].NodeName]
	}
}

func nodeCanFitReplica(node types.RlScheduleNodeCandidate, demand types.RlScheduleServiceDemand) bool {
	return node.CpuFreeCores+1e-9 >= demand.CpuRequestCores &&
		node.MemoryFreeGiB+1e-9 >= demand.MemoryRequestGiB &&
		node.GpuFree+1e-9 >= demand.GpuRequest &&
		node.PodsFree >= 1
}

func hasFeasibleNode(nodes []types.RlScheduleNodeCandidate, demand types.RlScheduleServiceDemand) bool {
	for i := range nodes {
		if nodes[i].ActionMasked {
			continue
		}
		if nodeCanFitReplica(nodes[i], demand) {
			return true
		}
	}
	return false
}

func reserveReplicaOnNode(node *types.RlScheduleNodeCandidate, demand types.RlScheduleServiceDemand) {
	if node == nil {
		return
	}
	node.CpuFreeCores = roundFloat(math.Max(0, node.CpuFreeCores-demand.CpuRequestCores), 3)
	node.MemoryFreeGiB = roundFloat(math.Max(0, node.MemoryFreeGiB-demand.MemoryRequestGiB), 3)
	node.GpuFree = roundFloat(math.Max(0, node.GpuFree-demand.GpuRequest), 3)
	if node.PodsFree > 0 {
		node.PodsFree--
	}
}

type nodeRequestedResources struct {
	cpuCores        float64
	memoryGiB       float64
	gpu             float64
	pods            int64
	hasCurrentPower bool
	currentPowerW   float64
}

type nodePowerReading struct {
	currentPowerW float64
}

type prometheusInstantSample struct {
	metric map[string]string
	value  float64
}

type prometheusServiceRef struct {
	namespace string
	name      string
	port      string
	scheme    string
	score     int
}

func nodeAllocatableResources(node *corev1.Node) nodeRequestedResources {
	if node == nil {
		return nodeRequestedResources{}
	}
	return nodeRequestedResources{
		cpuCores:  quantityCores(node.Status.Allocatable[corev1.ResourceCPU]),
		memoryGiB: quantityGiB(node.Status.Allocatable[corev1.ResourceMemory]),
		gpu:       resourceListGpu(node.Status.Allocatable),
		pods:      node.Status.Allocatable.Pods().Value(),
	}
}

func podConsumesNodeResources(pod *corev1.Pod) bool {
	if pod == nil || strings.TrimSpace(pod.Spec.NodeName) == "" {
		return false
	}
	return pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

func podRequestedResources(pod *corev1.Pod) nodeRequestedResources {
	if pod == nil {
		return nodeRequestedResources{}
	}
	app := nodeRequestedResources{}
	for _, container := range pod.Spec.Containers {
		app.add(resourceRequests(container.Resources.Requests))
	}

	initMax := nodeRequestedResources{}
	for _, container := range pod.Spec.InitContainers {
		initMax.max(resourceRequests(container.Resources.Requests))
	}

	app.max(initMax)
	if pod.Spec.Overhead != nil {
		app.add(resourceRequests(pod.Spec.Overhead))
	}
	return app
}

func resourceRequests(list corev1.ResourceList) nodeRequestedResources {
	return nodeRequestedResources{
		cpuCores:  quantityCores(list[corev1.ResourceCPU]),
		memoryGiB: quantityGiB(list[corev1.ResourceMemory]),
		gpu:       resourceListGpu(list),
	}
}

func (r *nodeRequestedResources) add(other nodeRequestedResources) {
	r.cpuCores += other.cpuCores
	r.memoryGiB += other.memoryGiB
	r.gpu += other.gpu
}

func (r *nodeRequestedResources) max(other nodeRequestedResources) {
	if other.cpuCores > r.cpuCores {
		r.cpuCores = other.cpuCores
	}
	if other.memoryGiB > r.memoryGiB {
		r.memoryGiB = other.memoryGiB
	}
	if other.gpu > r.gpu {
		r.gpu = other.gpu
	}
}

func nodeReadyStatus(node *corev1.Node) string {
	if node == nil {
		return "Unknown"
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		if condition.Status == corev1.ConditionTrue {
			return "Ready"
		}
		return "NotReady"
	}
	return "Unknown"
}

func nodeUnschedulableValue(node *corev1.Node) int64 {
	if node != nil && node.Spec.Unschedulable {
		return 2
	}
	return 1
}

func nodeCurrentPowerW(node *corev1.Node) (float64, bool) {
	if node == nil || len(node.Annotations) == 0 {
		return 0, false
	}
	for _, key := range nodeCurrentPowerAnnotationKeys {
		raw := strings.TrimSpace(node.Annotations[key])
		if raw == "" {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || !validCollectedPower(value) {
			continue
		}
		return value, true
	}
	return 0, false
}

func (l *BuildRlSchedulePlanLogic) loadNodePowerReadings(clusterUuid string, nodes []corev1.Node) map[string]nodePowerReading {
	if strings.TrimSpace(clusterUuid) == "" || len(nodes) == 0 || l == nil || l.svcCtx == nil {
		return nil
	}

	nodeLookup := buildNodeLookup(nodes)
	if len(nodeLookup) == 0 {
		return nil
	}

	readings := map[string]nodePowerReading{}
	for _, query := range prometheusNodePowerQueries {
		results, err := l.queryPrometheusInstant(clusterUuid, query)
		if err != nil {
			l.Errorf("query Prometheus node power failed: clusterUuid=%s query=%s err=%v", clusterUuid, query, err)
			continue
		}
		for _, result := range results {
			if !validCollectedPower(result.value) {
				continue
			}
			nodeName := prometheusMetricNodeName(result.metric, nodeLookup)
			if nodeName == "" {
				continue
			}
			if _, exists := readings[nodeName]; exists {
				continue
			}
			readings[nodeName] = nodePowerReading{currentPowerW: result.value}
		}
		if len(readings) >= len(nodes) {
			l.Infof("loaded node power from Prometheus: clusterUuid=%s nodes=%d", clusterUuid, len(readings))
			return readings
		}
	}
	if len(readings) > 0 {
		l.Infof("loaded partial node power from Prometheus: clusterUuid=%s nodes=%d/%d", clusterUuid, len(readings), len(nodes))
		return readings
	}
	return nil
}

func (l *BuildRlSchedulePlanLogic) loadClusterStorageSoc(clusterUuid string) (float64, bool) {
	if strings.TrimSpace(clusterUuid) == "" || l == nil || l.svcCtx == nil {
		return 0, false
	}
	for _, query := range prometheusStorageSocQueries {
		results, err := l.queryPrometheusInstant(clusterUuid, query)
		if err != nil {
			l.Errorf("query Prometheus storage SOC failed: clusterUuid=%s query=%s err=%v", clusterUuid, query, err)
			continue
		}
		for _, result := range results {
			if soc, ok := normalizeStorageSoc(result.value); ok {
				l.Infof("loaded storage SOC from Prometheus: clusterUuid=%s query=%s soc=%.2f", clusterUuid, query, soc)
				return soc, true
			}
		}
	}
	return 0, false
}

func (l *BuildRlSchedulePlanLogic) queryPrometheusInstant(clusterUuid, query string) ([]prometheusInstantSample, error) {
	if l == nil || l.svcCtx == nil {
		return nil, fmt.Errorf("schedule service context is nil")
	}
	if l.svcCtx.PrometheusManager != nil {
		prometheusClient, err := l.svcCtx.PrometheusManager.Get(clusterUuid)
		if err == nil {
			results, err := prometheusClient.Query(query, nil)
			if err != nil {
				return nil, err
			}
			samples := make([]prometheusInstantSample, 0, len(results))
			for _, result := range results {
				samples = append(samples, prometheusInstantSample{
					metric: result.Metric,
					value:  result.Value,
				})
			}
			return samples, nil
		}
		l.Infof("configured Prometheus unavailable, fallback to Kubernetes service proxy: clusterUuid=%s err=%v", clusterUuid, err)
	}
	return l.queryPrometheusViaKubernetesProxy(clusterUuid, query)
}

func (l *BuildRlSchedulePlanLogic) queryPrometheusViaKubernetesProxy(clusterUuid, query string) ([]prometheusInstantSample, error) {
	clusterClient, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUuid)
	if err != nil {
		return nil, fmt.Errorf("query cluster client failed: %w", err)
	}
	kubeClient := clusterClient.GetKubeClient()
	services, err := discoverPrometheusServices(l.ctx, kubeClient)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("no Prometheus service discovered in cluster")
	}
	var lastErr error
	for _, svc := range services {
		samples, err := queryPrometheusServiceProxy(l.ctx, kubeClient, svc, query)
		if err == nil {
			return samples, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("query Prometheus service proxy failed")
}

func discoverPrometheusServices(ctx context.Context, kubeClient kubernetes.Interface) ([]prometheusServiceRef, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client is nil")
	}
	seen := map[string]bool{}
	services := []prometheusServiceRef{}

	addServices := func(list *corev1.ServiceList) {
		if list == nil {
			return
		}
		for i := range list.Items {
			service := &list.Items[i]
			ref, ok := prometheusServiceCandidate(service)
			if !ok {
				continue
			}
			key := ref.namespace + "/" + ref.name + ":" + ref.port
			if seen[key] {
				continue
			}
			seen[key] = true
			services = append(services, ref)
		}
	}

	if list, err := kubeClient.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{}); err == nil {
		addServices(list)
	} else {
		for _, namespace := range prometheusDiscoveryNamespaces {
			list, nsErr := kubeClient.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
			if nsErr == nil {
				addServices(list)
			}
		}
	}

	sort.SliceStable(services, func(i, j int) bool {
		if services[i].score != services[j].score {
			return services[i].score > services[j].score
		}
		if services[i].namespace != services[j].namespace {
			return services[i].namespace < services[j].namespace
		}
		return services[i].name < services[j].name
	})
	return services, nil
}

func prometheusServiceCandidate(service *corev1.Service) (prometheusServiceRef, bool) {
	if service == nil || len(service.Spec.Ports) == 0 {
		return prometheusServiceRef{}, false
	}
	score := prometheusServiceScore(service)
	if score <= 0 {
		return prometheusServiceRef{}, false
	}
	port, scheme, ok := prometheusServicePort(service)
	if !ok {
		return prometheusServiceRef{}, false
	}
	return prometheusServiceRef{
		namespace: service.Namespace,
		name:      service.Name,
		port:      port,
		scheme:    scheme,
		score:     score,
	}, true
}

func prometheusServiceScore(service *corev1.Service) int {
	text := strings.ToLower(service.Namespace + " " + service.Name)
	for key, value := range service.Labels {
		text += " " + strings.ToLower(key) + "=" + strings.ToLower(value)
	}
	for key, value := range service.Annotations {
		text += " " + strings.ToLower(key) + "=" + strings.ToLower(value)
	}
	if !strings.Contains(text, "prometheus") {
		return 0
	}
	score := 10
	if strings.Contains(text, "alertmanager") || strings.Contains(text, "operator") || strings.Contains(text, "node-exporter") || strings.Contains(text, "pushgateway") {
		score -= 8
	}
	if strings.Contains(text, "server") || strings.Contains(text, "k8s") || strings.Contains(text, "stack") {
		score += 3
	}
	if service.Namespace == "monitoring" {
		score += 2
	}
	return score
}

func prometheusServicePort(service *corev1.Service) (string, string, bool) {
	best := -1
	bestScore := -1
	for i, port := range service.Spec.Ports {
		score := 0
		name := strings.ToLower(port.Name)
		if port.Port == 9090 {
			score += 10
		}
		if strings.Contains(name, "web") || strings.Contains(name, "http") || strings.Contains(name, "prometheus") {
			score += 5
		}
		if strings.Contains(name, "grpc") {
			score -= 10
		}
		if score > bestScore {
			best = i
			bestScore = score
		}
	}
	if best < 0 || bestScore < 0 {
		return "", "", false
	}
	port := service.Spec.Ports[best]
	scheme := "http"
	if strings.Contains(strings.ToLower(port.Name), "https") || port.Port == 443 {
		scheme = "https"
	}
	return strconv.Itoa(int(port.Port)), scheme, true
}

func queryPrometheusServiceProxy(ctx context.Context, kubeClient kubernetes.Interface, svc prometheusServiceRef, query string) ([]prometheusInstantSample, error) {
	raw, err := kubeClient.CoreV1().Services(svc.namespace).ProxyGet(
		svc.scheme,
		svc.name,
		svc.port,
		"/api/v1/query",
		map[string]string{"query": query},
	).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("query %s/%s proxy failed: %w", svc.namespace, svc.name, err)
	}
	var response struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode Prometheus response failed: %w", err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus query failed: %s", response.Error)
	}
	samples := make([]prometheusInstantSample, 0, len(response.Data.Result))
	for _, item := range response.Data.Result {
		if len(item.Value) != 2 {
			continue
		}
		valueText, ok := item.Value[1].(string)
		if !ok {
			continue
		}
		value, err := strconv.ParseFloat(valueText, 64)
		if err != nil {
			continue
		}
		samples = append(samples, prometheusInstantSample{
			metric: item.Metric,
			value:  value,
		})
	}
	return samples, nil
}

func nodePowerReadingFor(node *corev1.Node, prometheusReadings map[string]nodePowerReading) (float64, bool) {
	if node != nil && len(prometheusReadings) > 0 {
		if reading, ok := prometheusReadings[node.Name]; ok && validCollectedPower(reading.currentPowerW) {
			return reading.currentPowerW, true
		}
	}
	return nodeCurrentPowerW(node)
}

func buildNodeLookup(nodes []corev1.Node) map[string]string {
	lookup := map[string]string{}
	for i := range nodes {
		node := &nodes[i]
		addNodeLookup(lookup, node.Name, node.Name)
		for _, address := range node.Status.Addresses {
			addNodeLookup(lookup, address.Address, node.Name)
		}
	}
	return lookup
}

func addNodeLookup(lookup map[string]string, raw string, nodeName string) {
	key := normalizeNodeLookupKey(raw)
	if key == "" || strings.TrimSpace(nodeName) == "" {
		return
	}
	lookup[key] = nodeName
	if dot := strings.Index(key, "."); dot > 0 {
		lookup[key[:dot]] = nodeName
	}
}

func prometheusMetricNodeName(metric map[string]string, lookup map[string]string) string {
	for _, label := range []string{"node_name", "node", "kubernetes_node", "nodename", "host", "hostname", "Hostname", "instance"} {
		if nodeName := lookupNodeName(metric[label], lookup); nodeName != "" {
			return nodeName
		}
	}
	for _, value := range metric {
		if nodeName := lookupNodeName(value, lookup); nodeName != "" {
			return nodeName
		}
	}
	return ""
}

func lookupNodeName(raw string, lookup map[string]string) string {
	key := normalizeNodeLookupKey(raw)
	if key == "" {
		return ""
	}
	if nodeName, ok := lookup[key]; ok {
		return nodeName
	}
	if dot := strings.Index(key, "."); dot > 0 {
		if nodeName, ok := lookup[key[:dot]]; ok {
			return nodeName
		}
	}
	return ""
}

func normalizeNodeLookupKey(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.TrimPrefix(key, "http://")
	key = strings.TrimPrefix(key, "https://")
	key = strings.Trim(key, "[]")
	if slash := strings.Index(key, "/"); slash >= 0 {
		key = key[:slash]
	}
	if colon := strings.LastIndex(key, ":"); colon > 0 && strings.Count(key, ":") == 1 {
		key = key[:colon]
	}
	return strings.TrimSpace(key)
}

func validCollectedPower(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func normalizeStorageSoc(value float64) (float64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	if value <= 1 {
		value *= 100
	}
	if value > 100 {
		return 0, false
	}
	return value, true
}

func quotaHasFloatFree(quota *corev1.ResourceQuota, name corev1.ResourceName, required float64, convert func(resource.Quantity) float64) (bool, string) {
	if required <= 0 {
		return true, ""
	}
	hard, exists := quota.Spec.Hard[name]
	if !exists || hard.IsZero() {
		return false, "未配置硬配额"
	}
	used := quota.Status.Used[name]
	free := convert(hard) - convert(used)
	if free+1e-9 < required {
		return false, fmt.Sprintf("需要 %.3f，剩余 %.3f", required, free)
	}
	return true, ""
}

func quotaHasGpuFree(quota *corev1.ResourceQuota, required float64) (bool, string) {
	if required <= 0 {
		return true, ""
	}
	for _, name := range gpuResourceNames {
		hard, exists := quota.Spec.Hard[name]
		if !exists || hard.IsZero() {
			continue
		}
		used := quota.Status.Used[name]
		free := quantityFloat(hard) - quantityFloat(used)
		if free+1e-9 < required {
			return false, fmt.Sprintf("需要 %.3f，剩余 %.3f", required, free)
		}
		return true, ""
	}
	return false, "未配置硬配额"
}

func quotaHasIntFree(quota *corev1.ResourceQuota, name corev1.ResourceName, required int64) (bool, string) {
	if required <= 0 {
		return true, ""
	}
	hard, exists := quota.Spec.Hard[name]
	if !exists || hard.IsZero() {
		return false, "未配置硬配额"
	}
	used := quota.Status.Used[name]
	free := hard.Value() - used.Value()
	if free < required {
		return false, fmt.Sprintf("需要 %d，剩余 %d", required, free)
	}
	return true, ""
}

func resourceListGpu(list corev1.ResourceList) float64 {
	for _, name := range gpuResourceNames {
		if value, ok := list[name]; ok && !value.IsZero() {
			return quantityFloat(value)
		}
	}
	return 0
}

func quantityCores(q resource.Quantity) float64 {
	return float64(q.MilliValue()) / 1000.0
}

func quantityGiB(q resource.Quantity) float64 {
	return float64(q.Value()) / bytesPerGiB
}

func quantityFloat(q resource.Quantity) float64 {
	return q.AsApproximateFloat64()
}

func (l *BuildRlSchedulePlanLogic) persistSchedulePlan(req *types.BuildRlSchedulePlanRequest, resp *types.BuildRlSchedulePlanResponse) error {
	if req == nil || resp == nil {
		return nil
	}

	clusterSnapshot, err := json.Marshal(resp.ClusterCandidates)
	if err != nil {
		return errorx.Msg("序列化跨集群调度快照失败")
	}
	nodeSnapshot, err := json.Marshal(resp.NodeCandidates)
	if err != nil {
		return errorx.Msg("序列化集群内调度快照失败")
	}
	placements, err := json.Marshal(resp.Placements)
	if err != nil {
		return errorx.Msg("序列化副本节点分布失败")
	}
	plan, err := json.Marshal(resp)
	if err != nil {
		return errorx.Msg("序列化调度计划失败")
	}

	status := "PLANNED"
	if !resp.Executable {
		status = "FAILED"
	}
	operator := scheduleOperator(l.ctx)

	_, err = l.svcCtx.ManagerRpc.SchedulePlanSave(l.ctx, &pb.SaveSchedulePlanReq{
		PlanId:                 resp.PlanId,
		ProjectId:              req.ProjectId,
		WorkspaceId:            req.WorkspaceId,
		TargetWorkspaceId:      resp.TargetCluster.TargetWorkspaceId,
		TargetProjectClusterId: resp.TargetCluster.ProjectClusterId,
		TargetClusterUuid:      resp.TargetCluster.ClusterUuid,
		Namespace:              resp.TargetCluster.Namespace,
		ServiceName:            req.Service.ServiceName,
		ModelVersion:           resp.ModelVersion,
		ClusterSnapshotJson:    string(clusterSnapshot),
		NodeSnapshotJson:       string(nodeSnapshot),
		PlacementsJson:         string(placements),
		PlanJson:               string(plan),
		Executable:             resp.Executable,
		Status:                 status,
		Reason:                 resp.Reason,
		CreatedBy:              operator,
		UpdatedBy:              operator,
	})
	if err != nil {
		l.Errorf("persist schedule plan failed: %v", err)
		return errorx.Msg("保存调度计划失败")
	}
	return nil
}

func (l *BuildRlSchedulePlanLogic) recordSchedulePlanAudit(req *types.BuildRlSchedulePlanRequest, resp *types.BuildRlSchedulePlanResponse) {
	if req == nil || resp == nil || req.WorkspaceId == 0 {
		return
	}
	status := int64(0)
	if resp.Executable {
		status = 1
	}
	targetCluster := resp.TargetCluster.ClusterName
	if strings.TrimSpace(targetCluster) == "" {
		targetCluster = resp.TargetCluster.ClusterUuid
	}
	placementNodes := make([]string, 0, len(resp.Placements))
	for _, placement := range resp.Placements {
		if strings.TrimSpace(placement.NodeName) != "" {
			placementNodes = append(placementNodes, placement.NodeName)
		}
	}
	parts := []string{
		fmt.Sprintf("调度计划ID=%s", resp.PlanId),
		fmt.Sprintf("算法版本=%s", resp.ModelVersion),
		fmt.Sprintf("服务=%s", req.Service.ServiceName),
		fmt.Sprintf("副本数=%d", req.Service.Replicas),
		fmt.Sprintf("资源请求=%.3fC/%.3fGi/%.3fGPU", req.Service.CpuRequestCores, req.Service.MemoryRequestGiB, req.Service.GpuRequest),
		fmt.Sprintf("候选集群数=%d", len(resp.ClusterCandidates)),
		fmt.Sprintf("候选节点数=%d", len(resp.NodeCandidates)),
		fmt.Sprintf("可执行=%t", resp.Executable),
	}
	if strings.TrimSpace(targetCluster) != "" {
		parts = append(parts, fmt.Sprintf("目标集群=%s", targetCluster))
	}
	if resp.TargetCluster.TargetWorkspaceId > 0 {
		parts = append(parts, fmt.Sprintf("目标工作空间绑定ID=%d", resp.TargetCluster.TargetWorkspaceId))
	}
	if len(placementNodes) > 0 {
		parts = append(parts, fmt.Sprintf("目标节点分布=%s", strings.Join(placementNodes, ",")))
	}
	if strings.TrimSpace(resp.Reason) != "" {
		parts = append(parts, fmt.Sprintf("原因=%s", resp.Reason))
	}
	_, err := l.svcCtx.ManagerRpc.ProjectAuditLogAdd(l.ctx, &pb.AddOnecProjectAuditLogReq{
		WorkspaceId:  req.WorkspaceId,
		Title:        "生成调度计划",
		ActionDetail: strings.Join(parts, "；"),
		Status:       status,
	})
	if err != nil {
		l.Errorf("record schedule plan audit failed: %v", err)
	}
}

func parseResourceFloat(raw string) float64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	lower := strings.ToLower(s)
	multiplier := 1.0
	switch {
	case strings.HasSuffix(lower, "mi"):
		multiplier = 1.0 / 1024.0
		s = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(lower, "gi"):
		s = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(lower, "m"):
		multiplier = 0.001
		s = strings.TrimSpace(s[:len(s)-1])
	}
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return value * multiplier
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func sortablePrice(candidate types.RlScheduleClusterCandidate) float64 {
	if !candidate.HasEnergyProfile || candidate.GridPricePerKwh <= 0 {
		return math.MaxFloat64
	}
	return candidate.GridPricePerKwh
}

func sortablePower(hasCurrentPower bool, currentPowerW float64) float64 {
	if !hasCurrentPower || currentPowerW < 0 {
		return math.MaxFloat64
	}
	return currentPowerW
}

func isNormalClusterStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "" || s == "normal" || s == "healthy" || s == "ready" || s == "1"
}

func isReadyNode(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "ready" || s == "normal" || s == "healthy"
}

func isUnschedulableNode(value int64) bool {
	return value == 2
}

func normalizedServiceName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "service"
	}
	return strings.TrimSpace(name)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len([]rune(text)) <= 120 {
		return text
	}
	return string([]rune(text)[:120]) + "..."
}

func roundFloat(value float64, digits int) float64 {
	factor := math.Pow10(digits)
	return math.Round(value*factor) / factor
}

func makeRlPlanId() string {
	return fmt.Sprintf("rl-plan-%d", time.Now().UnixNano())
}

func scheduleOperator(ctx context.Context) string {
	if ctx == nil {
		return "system"
	}
	if username, ok := ctx.Value("username").(string); ok && strings.TrimSpace(username) != "" {
		return strings.TrimSpace(username)
	}
	return "system"
}

func workspacePolicyName(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "ikubeops"
	}
	return "ikubeops-" + namespace
}

func legacyWorkspacePolicyName(workspaceName string) string {
	workspaceName = strings.TrimSpace(workspaceName)
	if workspaceName == "" {
		return "ikubeops"
	}
	return "ikubeops" + workspaceName
}
