package profilederivation

import (
	"bytes"
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

func TestSharedCurveRejectsOldFrozenCPUAllocation(t *testing.T) {
	plan, err := NewCurvePlan(validCurvePlanSpec())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(payload, []byte(`"shared":{`), []byte(`"shared":{"cpu":{"limit":"4"},`), 1)
	if _, err := DecodeCurvePlan(payload); err == nil {
		t.Fatal("old quota-controlled curve was accepted as shared evidence")
	}
}
