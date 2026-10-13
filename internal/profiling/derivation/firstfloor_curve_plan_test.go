package profilederivation

import (
	"bytes"
	"slices"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

func TestFirstfloorCurveFreezesGPUAllocation(t *testing.T) {
	shared := validCurvePlanSpec()
	shared.RuntimeFamily = "firstfloor"
	shared.Backend = "ort-cuda"
	shared.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	shared.RuntimeAccess = &CurveRuntimeAccessContract{
		GPUUUID: "GPU-actual-device", Resources: map[string]string{"nvidia.com/gpu": "1"},
		SupplementalGroups: []int64{44},
	}
	plan, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	shared.RuntimeAccess.Resources["nvidia.com/gpu"] = "2"
	shared.RuntimeAccess.SupplementalGroups[0] = 999
	copy := plan.Shared()
	copy.RuntimeAccess.Resources["nvidia.com/gpu"] = "3"
	if plan.Shared().RuntimeAccess.Resources["nvidia.com/gpu"] != "1" || plan.Shared().RuntimeAccess.SupplementalGroups[0] != 44 {
		t.Fatal("frozen curve access aliases caller allocation")
	}
	changed := plan.Shared()
	changed.RuntimeAccess.GPUUUID = "GPU-other-device"
	other, err := NewCurvePlan(changed)
	if err != nil {
		t.Fatal(err)
	}
	otherJSON, err := other.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(canonical, otherJSON) {
		t.Fatal("GPU replacement retained curve identity")
	}
	verified := testVerifiedState(plan)
	verified.Shared = changed
	if _, err := VerifyCurvePlan(plan, verified); err == nil {
		t.Fatal("GPU drift was accepted")
	}
	for _, mutate := range []func(*CurvePlanSpec){
		func(s *CurvePlanSpec) { s.Firstfloor = nil },
		func(s *CurvePlanSpec) { s.Firstfloor.MaxQueueSize = 0 },
		func(s *CurvePlanSpec) { s.RuntimeAccess = nil },
		func(s *CurvePlanSpec) { s.RuntimeAccess.GPUUUID = "" },
		func(s *CurvePlanSpec) { s.RuntimeAccess.Resources["nvidia.com/gpu"] = "2" },
		func(s *CurvePlanSpec) { s.Backend = "ort-cpu" },
		func(s *CurvePlanSpec) { s.PlannedBatchSizes = []int{5} },
	} {
		candidate := plan.Shared()
		mutate(&candidate)
		if _, err := NewCurvePlan(candidate); err == nil {
			t.Fatal("contradictory accelerator curve was accepted")
		}
	}
}

func TestFirstfloorCurveAffinityCanonicalIdentityAndClones(t *testing.T) {
	shared := validCurvePlanSpec()
	shared.RuntimeFamily, shared.Backend = "firstfloor", "ort-cpu"
	shared.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	shared.CPU.ORTIntraOpThreads, shared.CPU.ORTInterOpThreads = 2, 1
	shared.CPU.AffinityCPUs = []int{31, 8}
	plan, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(shared.CPU.AffinityCPUs, []int{31, 8}) ||
		!slices.Equal(plan.Shared().CPU.AffinityCPUs, []int{8, 31}) {
		t.Fatal("curve construction did not sort a detached mask")
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	shared.CPU.AffinityCPUs[0] = 99
	copy := plan.Shared()
	copy.CPU.AffinityCPUs[0] = 9
	if !slices.Equal(plan.Shared().CPU.AffinityCPUs, []int{8, 31}) {
		t.Fatal("frozen curve affinity aliases caller masks")
	}
	decoded, err := DecodeCurvePlan(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(decoded.Shared().CPU.AffinityCPUs, []int{8, 31}) {
		t.Fatal("curve reconstruction lost affinity")
	}
	other, err := NewCurvePlan(copy)
	if err != nil {
		t.Fatal(err)
	}
	otherJSON, err := other.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(canonical, otherJSON) {
		t.Fatal("CPU mask replacement retained curve identity")
	}
	state := testVerifiedState(plan)
	verified, err := VerifyCurvePlan(plan, state)
	if err != nil {
		t.Fatal(err)
	}
	state.Shared.CPU.AffinityCPUs[0] = 9
	if verified.state.Shared.CPU.AffinityCPUs[0] != 8 {
		t.Fatal("verified curve aliases supplied affinity")
	}
	if _, err := VerifyCurvePlan(plan, state); err == nil {
		t.Fatal("verified curve accepted a different exact mask")
	}
	for _, mask := range [][]int{{-1, 8}, {8, 8}, {8}} {
		invalid := plan.Shared()
		invalid.CPU.AffinityCPUs = mask
		if _, err := NewCurvePlan(invalid); err == nil {
			t.Fatalf("invalid curve affinity accepted: %v", mask)
		}
	}
}
