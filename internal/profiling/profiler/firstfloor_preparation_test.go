package profiler

import (
	"slices"
	"testing"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestFirstfloorORTThreadsFollowTheSelectedCPUQuota(t *testing.T) {
	for _, backend := range []string{profilingrun.FirstfloorORTCPUBackend, profilingrun.FirstfloorORTCUDABackend, profilingrun.FirstfloorRKNNLiteBackend} {
		for _, quota := range []string{"", "2"} {
			candidate := ResolvedCandidate{State: profilingrun.ExecutionState{RuntimeFamily: profilingrun.FirstfloorRuntimeFamily, Backend: backend}}
			contract, err := freezeCPUContract(PreparationRequest{CPURequest: "100m", CPUQuota: quota}, candidate, profilingrun.NodeSnapshot{AllocatableCPU: "4"})
			if err != nil {
				t.Fatal(err)
			}
			threads, inter := 4, 1
			if quota != "" {
				threads = 2
			}
			if backend == profilingrun.FirstfloorRKNNLiteBackend {
				threads, inter = 0, 0
			}
			if contract.ORTIntraOpThreads != threads || contract.ORTInterOpThreads != inter {
				t.Fatalf("backend %s quota %s: threads %d/%d, want %d/%d", backend, quota, contract.ORTIntraOpThreads, contract.ORTInterOpThreads, threads, inter)
			}
		}
	}
}

func TestFirstfloorAffinityFreezesExactSortedDetachedCPUIds(t *testing.T) {
	mask := []int{31, 8}
	request := PreparationRequest{CPURequest: "100m", CPUQuota: "2", CPUAffinity: mask}
	candidate := ResolvedCandidate{State: profilingrun.ExecutionState{
		RuntimeFamily: profilingrun.FirstfloorRuntimeFamily, Backend: profilingrun.FirstfloorORTCPUBackend,
	}}
	contract, err := freezeCPUContract(request, candidate, profilingrun.NodeSnapshot{AllocatableCPU: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(contract.AffinityCPUs, []int{8, 31}) || !slices.Equal(mask, []int{31, 8}) ||
		contract.ORTIntraOpThreads != 2 || contract.ExclusivityClaimed {
		t.Fatalf("explicit mask or quota policy drifted: %+v, request %v", contract, mask)
	}
	mask[0] = 99
	if !slices.Equal(contract.AffinityCPUs, []int{8, 31}) {
		t.Fatal("frozen CPU affinity aliases the caller")
	}
	for _, invalid := range [][]int{{8, 8}, {-1, 8}, {8}} {
		request.CPUAffinity = invalid
		if _, err := freezeCPUContract(request, candidate, profilingrun.NodeSnapshot{AllocatableCPU: "4"}); err == nil {
			t.Fatalf("invalid affinity accepted: %v", invalid)
		}
	}
	request.CPUAffinity = []int{8, 31}
	candidate.State.RuntimeFamily = firstRuntimeFamily
	candidate.State.Backend = firstRuntimeBackend
	if _, err := freezeCPUContract(request, candidate, profilingrun.NodeSnapshot{AllocatableCPU: "4"}); err == nil {
		t.Fatal("explicit affinity exposed on an independent reference producer")
	}
	request.CPUAffinity = nil
	if _, err := freezeCPUContract(request, candidate, profilingrun.NodeSnapshot{AllocatableCPU: "4"}); err != nil {
		t.Fatalf("default reference experiment changed: %v", err)
	}
}
