package schedule

import (
	"math"

	"github.com/yanshicheng/kube-nova/application/manager-api/internal/types"
)

func (l *BuildRlSchedulePlanLogic) clusterPolicyScore(candidate types.RlScheduleClusterCandidate, demand types.RlScheduleServiceDemand) float64 {
	return clusterResourceHeuristic(candidate, demand)
}

func (l *BuildRlSchedulePlanLogic) nodePolicyScore(clusterUuid string, node types.RlScheduleNodeCandidate, demand types.RlScheduleServiceDemand, assigned map[string]int64) float64 {
	score := nodeResourceHeuristic(node, demand)
	score -= float64(assigned[node.NodeName]) * 0.15
	return score
}

func clusterResourceHeuristic(candidate types.RlScheduleClusterCandidate, demand types.RlScheduleServiceDemand) float64 {
	requiredCpu := demand.CpuRequestCores * float64(demand.Replicas)
	requiredMem := demand.MemoryRequestGiB * float64(demand.Replicas)
	requiredGpu := demand.GpuRequest * float64(demand.Replicas)

	score := 0.45 * fitScore(candidate.CpuFreeCores, requiredCpu)
	score += 0.35 * fitScore(candidate.MemoryFreeGiB, requiredMem)
	score += 0.10 * fitScore(candidate.GpuFree, requiredGpu)
	score += 0.10 * powerScore(candidate.HasCurrentPower, candidate.CurrentPowerW)
	return score
}

func nodeResourceHeuristic(node types.RlScheduleNodeCandidate, demand types.RlScheduleServiceDemand) float64 {
	score := 0.40 * fitScore(node.CpuFreeCores, demand.CpuRequestCores)
	score += 0.35 * fitScore(node.MemoryFreeGiB, demand.MemoryRequestGiB)
	score += 0.10 * fitScore(node.GpuFree, demand.GpuRequest)
	score += 0.05 * fitScore(float64(node.PodsFree), 1)
	score += 0.10 * powerScore(node.HasCurrentPower, node.CurrentPowerW)
	return score
}

func fitScore(free, required float64) float64 {
	if required <= 0 {
		return 1
	}
	if free <= 0 {
		return 0
	}
	return math.Min(free/required, 4) / 4
}

func powerScore(hasPower bool, powerW float64) float64 {
	if !hasPower || powerW < 0 || math.IsNaN(powerW) || math.IsInf(powerW, 0) {
		return 0.5
	}
	return 1 / (1 + powerW/1000)
}
