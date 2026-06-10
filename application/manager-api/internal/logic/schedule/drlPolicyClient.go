package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yanshicheng/kube-nova/application/manager-api/internal/types"
)

const (
	defaultRlPolicyTimeout        = 3 * time.Second
	defaultRlPolicyPricePerKwh    = 0.8
	clusterPolicyPredictPath      = "/v1/predict/cluster"
	nodePolicyPredictPath         = "/v1/predict/node"
	defaultClusterPolicyModelName = "maskable-ppo-cluster-v1"
	defaultNodePolicyModelName    = "maskable-ppo-node-v1"
)

type drlPredictRequest struct {
	Model       string               `json:"model"`
	Level       string               `json:"level"`
	Service     drlServiceDemand     `json:"service"`
	Candidates  []drlActionCandidate `json:"candidates"`
	ActionMask  []bool               `json:"actionMask"`
	Replica     int64                `json:"replica,omitempty"`
	PricePerKwh float64              `json:"pricePerKwh"`
	PriceSource string               `json:"priceSource"`
}

type drlServiceDemand struct {
	Replicas         int64   `json:"replicas"`
	CpuRequestCores  float64 `json:"cpuRequestCores"`
	MemoryRequestGiB float64 `json:"memoryRequestGiB"`
	GpuRequest       float64 `json:"gpuRequest"`
}

type drlActionCandidate struct {
	Id       string             `json:"id"`
	Name     string             `json:"name"`
	Allowed  bool               `json:"allowed"`
	Features map[string]float64 `json:"features"`
}

type drlPredictResponse struct {
	ActionIndex  int    `json:"actionIndex"`
	ModelVersion string `json:"modelVersion"`
	Reason       string `json:"reason,omitempty"`
}

func (l *BuildRlSchedulePlanLogic) predictClusterIndex(candidates []types.RlScheduleClusterCandidate, demand types.RlScheduleServiceDemand) (int, bool) {
	if !l.rlPolicyServiceEnabled() {
		return -1, false
	}
	request := drlPredictRequest{
		Model:       l.clusterPolicyModelName(),
		Level:       "cluster",
		Service:     toDrlServiceDemand(demand),
		PricePerKwh: l.defaultRlPricePerKwh(),
		PriceSource: "default",
		Candidates:  make([]drlActionCandidate, 0, len(candidates)),
		ActionMask:  make([]bool, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		allowed := !candidate.ActionMasked
		request.Candidates = append(request.Candidates, drlActionCandidate{
			Id:       candidate.ClusterUuid,
			Name:     candidate.ClusterName,
			Allowed:  allowed,
			Features: clusterDrlFeatures(candidate, demand, request.PricePerKwh),
		})
		request.ActionMask = append(request.ActionMask, allowed)
	}
	response, ok := l.callDrlPredict(clusterPolicyPredictPath, request)
	if !ok || response.ActionIndex < 0 || response.ActionIndex >= len(candidates) {
		return -1, false
	}
	if candidates[response.ActionIndex].ActionMasked {
		l.Errorf("DRL cluster policy returned masked action: index=%d model=%s reason=%s", response.ActionIndex, request.Model, response.Reason)
		return -1, false
	}
	return response.ActionIndex, true
}

func (l *BuildRlSchedulePlanLogic) predictNodeIndex(
	clusterUuid string,
	nodes []types.RlScheduleNodeCandidate,
	demand types.RlScheduleServiceDemand,
	assigned map[string]int64,
	replica int64,
) (int, bool) {
	if !l.rlPolicyServiceEnabled() {
		return -1, false
	}
	request := drlPredictRequest{
		Model:       l.nodePolicyModelName(),
		Level:       "node",
		Service:     toDrlServiceDemand(demand),
		Replica:     replica,
		PricePerKwh: l.defaultRlPricePerKwh(),
		PriceSource: "default",
		Candidates:  make([]drlActionCandidate, 0, len(nodes)),
		ActionMask:  make([]bool, 0, len(nodes)),
	}
	for _, node := range nodes {
		allowed := !node.ActionMasked && nodeCanFitReplica(node, demand)
		request.Candidates = append(request.Candidates, drlActionCandidate{
			Id:       node.NodeName,
			Name:     node.NodeName,
			Allowed:  allowed,
			Features: nodeDrlFeatures(clusterUuid, node, demand, assigned, request.PricePerKwh),
		})
		request.ActionMask = append(request.ActionMask, allowed)
	}
	response, ok := l.callDrlPredict(nodePolicyPredictPath, request)
	if !ok || response.ActionIndex < 0 || response.ActionIndex >= len(nodes) {
		return -1, false
	}
	selected := nodes[response.ActionIndex]
	if selected.ActionMasked || !nodeCanFitReplica(selected, demand) {
		l.Errorf("DRL node policy returned masked or infeasible action: index=%d node=%s model=%s reason=%s", response.ActionIndex, selected.NodeName, request.Model, response.Reason)
		return -1, false
	}
	return response.ActionIndex, true
}

func (l *BuildRlSchedulePlanLogic) callDrlPredict(path string, request drlPredictRequest) (drlPredictResponse, bool) {
	endpoint := strings.TrimRight(strings.TrimSpace(l.svcCtx.Config.RlPolicy.Endpoint), "/")
	if endpoint == "" {
		return drlPredictResponse{}, false
	}
	payload, err := json.Marshal(request)
	if err != nil {
		l.Errorf("marshal DRL predict request failed: %v", err)
		return drlPredictResponse{}, false
	}

	timeout := defaultRlPolicyTimeout
	if l.svcCtx.Config.RlPolicy.Timeout > 0 {
		timeout = time.Duration(l.svcCtx.Config.RlPolicy.Timeout) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(l.ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+path, bytes.NewReader(payload))
	if err != nil {
		l.Errorf("build DRL predict request failed: %v", err)
		return drlPredictResponse{}, false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		l.Errorf("call DRL policy service failed: path=%s err=%v", path, err)
		return drlPredictResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		l.Errorf("DRL policy service returned HTTP %d: path=%s body=%s", resp.StatusCode, path, strings.TrimSpace(string(body)))
		return drlPredictResponse{}, false
	}

	var result drlPredictResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		l.Errorf("decode DRL predict response failed: %v", err)
		return drlPredictResponse{}, false
	}
	return result, true
}

func (l *BuildRlSchedulePlanLogic) rlPolicyServiceEnabled() bool {
	return l != nil && l.svcCtx != nil && l.svcCtx.Config.RlPolicy.Enabled && strings.TrimSpace(l.svcCtx.Config.RlPolicy.Endpoint) != ""
}

func (l *BuildRlSchedulePlanLogic) rlPolicyRequired() bool {
	return l.rlPolicyServiceEnabled() && l.svcCtx.Config.RlPolicy.Required
}

func (l *BuildRlSchedulePlanLogic) clusterPolicyModelName() string {
	if l == nil || l.svcCtx == nil || strings.TrimSpace(l.svcCtx.Config.RlPolicy.ClusterModel) == "" {
		return defaultClusterPolicyModelName
	}
	return strings.TrimSpace(l.svcCtx.Config.RlPolicy.ClusterModel)
}

func (l *BuildRlSchedulePlanLogic) nodePolicyModelName() string {
	if l == nil || l.svcCtx == nil || strings.TrimSpace(l.svcCtx.Config.RlPolicy.NodeModel) == "" {
		return defaultNodePolicyModelName
	}
	return strings.TrimSpace(l.svcCtx.Config.RlPolicy.NodeModel)
}

func (l *BuildRlSchedulePlanLogic) defaultRlPricePerKwh() float64 {
	if l == nil || l.svcCtx == nil || l.svcCtx.Config.RlPolicy.DefaultPricePerKwh <= 0 {
		return defaultRlPolicyPricePerKwh
	}
	return l.svcCtx.Config.RlPolicy.DefaultPricePerKwh
}

func toDrlServiceDemand(demand types.RlScheduleServiceDemand) drlServiceDemand {
	return drlServiceDemand{
		Replicas:         demand.Replicas,
		CpuRequestCores:  demand.CpuRequestCores,
		MemoryRequestGiB: demand.MemoryRequestGiB,
		GpuRequest:       demand.GpuRequest,
	}
}

func clusterDrlFeatures(candidate types.RlScheduleClusterCandidate, demand types.RlScheduleServiceDemand, pricePerKwh float64) map[string]float64 {
	requiredCpu := demand.CpuRequestCores * float64(demand.Replicas)
	requiredMem := demand.MemoryRequestGiB * float64(demand.Replicas)
	requiredGpu := demand.GpuRequest * float64(demand.Replicas)
	return map[string]float64{
		"authorized":              boolFeature(candidate.Authorized),
		"status_normal":           boolFeature(isNormalClusterStatus(candidate.Status)),
		"realtime_resource_ready": boolFeature(candidate.RealtimeResourceReady),
		"cpu_free_ratio":          cappedRatio(candidate.CpuFreeCores, requiredCpu),
		"memory_free_ratio":       cappedRatio(candidate.MemoryFreeGiB, requiredMem),
		"gpu_free_ratio":          cappedRatio(candidate.GpuFree, requiredGpu),
		"has_current_power":       boolFeature(candidate.HasCurrentPower),
		"current_power_norm":      normalizedPower(candidate.HasCurrentPower, candidate.CurrentPowerW),
		"price_per_kwh":           pricePerKwh,
		"price_is_default":        1,
		"storage_soc":             0.5,
		"has_storage_soc":         0,
	}
}

func nodeDrlFeatures(clusterUuid string, node types.RlScheduleNodeCandidate, demand types.RlScheduleServiceDemand, assigned map[string]int64, pricePerKwh float64) map[string]float64 {
	return map[string]float64{
		"cluster_present":    boolFeature(strings.TrimSpace(clusterUuid) != ""),
		"node_ready":         boolFeature(isReadyNode(node.NodeStatus)),
		"unschedulable":      boolFeature(isUnschedulableNode(node.Unschedulable)),
		"cpu_free_ratio":     cappedRatio(node.CpuFreeCores, demand.CpuRequestCores),
		"memory_free_ratio":  cappedRatio(node.MemoryFreeGiB, demand.MemoryRequestGiB),
		"gpu_free_ratio":     cappedRatio(node.GpuFree, demand.GpuRequest),
		"pods_free_norm":     clampFloat(float64(node.PodsFree)/10, 0, 1),
		"assigned_replicas":  float64(assigned[node.NodeName]),
		"has_current_power":  boolFeature(node.HasCurrentPower),
		"current_power_norm": normalizedPower(node.HasCurrentPower, node.CurrentPowerW),
		"price_per_kwh":      pricePerKwh,
		"price_is_default":   1,
	}
}

func boolFeature(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func cappedRatio(value, base float64) float64 {
	if base <= 0 {
		return 1
	}
	return clampFloat(value/base, 0, 4) / 4
}

func normalizedPower(hasPower bool, powerW float64) float64 {
	if !hasPower || powerW < 0 {
		return 0
	}
	return clampFloat(powerW/5000, 0, 1)
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (r drlPredictRequest) String() string {
	return fmt.Sprintf("%s/%s candidates=%d", r.Level, r.Model, len(r.Candidates))
}
