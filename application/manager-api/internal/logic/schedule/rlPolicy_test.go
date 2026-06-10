package schedule

import (
	"testing"

	"github.com/yanshicheng/kube-nova/application/manager-api/internal/types"
)

func TestClusterResourceHeuristicIgnoresMissingEnergyInputs(t *testing.T) {
	score := clusterResourceHeuristic(types.RlScheduleClusterCandidate{
		CpuFreeCores:  8,
		MemoryFreeGiB: 16,
		GpuFree:       0,
	}, types.RlScheduleServiceDemand{
		Replicas:         2,
		CpuRequestCores:  1,
		MemoryRequestGiB: 2,
		GpuRequest:       0,
	})
	if score <= 0 {
		t.Fatalf("expected positive heuristic score, got %f", score)
	}
}
