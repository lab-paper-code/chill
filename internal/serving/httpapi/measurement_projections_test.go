package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMeasurementProjectionWhitelistAndOriginalReferences(t *testing.T) {
	metadata := meta.ObjectMeta{Namespace: "allowed", Name: "measurement", UID: "original-uid", ResourceVersion: "42", Annotations: map[string]string{"secret": "hidden"}}
	for _, phase := range []api.MeasurementPhase{api.MeasurementPhaseMeasuring, api.MeasurementPhaseFailed, api.MeasurementPhaseInsufficient} {
		occupancy := api.DispatchOccupancyMeasurement{ObjectMeta: metadata, Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Workload: "workload", BatchEnergyOptimization: "option", Batch: 8, Workers: 16, DurationSeconds: 60, Conditions: api.DispatchOccupancyConditions{InputsJSON: "secret-input"}}, Status: api.DispatchOccupancyMeasurementStatus{Phase: phase, Reason: "original-reason"}}
		p := ProjectDispatchOccupancyMeasurement(occupancy)
		if len(p.References.Evidence) != 0 || p.References.CR.UID != "original-uid" {
			t.Fatalf("lost original identity: %+v", p)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"inputsJSON", "secret-input", "annotations", "perRequestNanos", "supplyMode", "\"result\"", "\"spec\""} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("projection leaked %s: %s", forbidden, raw)
			}
		}
		if !strings.Contains(string(raw), string(phase)) || !strings.Contains(string(raw), "original-reason") {
			t.Fatalf("lost status: %s", raw)
		}
		occupancy.Status.Result = &api.ProfilingRunEvidenceReference{Store: "original-store", Key: "original-key", Size: 123}
		p = ProjectDispatchOccupancyMeasurement(occupancy)
		if p.References.Evidence[0] != (MeasurementEvidenceLocation{Store: "original-store", Key: "original-key"}) {
			t.Fatal("result reference changed")
		}
		raw, _ = json.Marshal(p)
		if strings.Contains(string(raw), "size") {
			t.Fatalf("result exposed non-whitelisted metadata: %s", raw)
		}
	}
	idle := int32(7)
	operation := api.OperationCostMeasurement{ObjectMeta: metadata, Spec: api.OperationCostMeasurementSpec{Node: "node", Steps: []api.OperationCostStep{{Name: "idle", Kind: api.OperationCostStepIdle, IdleDurationSeconds: &idle}}}, Status: api.OperationCostMeasurementStatus{Phase: api.MeasurementPhaseFailed, Steps: []api.OperationCostStepStatus{{Name: "idle", Key: "saved-key", Verdict: "Rejected"}, {Name: "pending"}}}}
	duration, zero := int64(123456789), 0.0
	p := ProjectOperationCostMeasurement(operation, "configured-store", []servingkube.OperationStepProjection{
		{OperationID: "original-uid.0.idle", DurationNanos: &duration, WallJoules: &zero, Reason: "recorded-reason", FromPowerMode: "MAXN", ToPowerMode: "MODE_15W", Model: "frozen-model"},
		{},
	})
	if len(p.References.Evidence) != 1 || p.References.Evidence[0].Key != "saved-key" || p.References.Evidence[0].Store != "configured-store" {
		t.Fatalf("invented or lost evidence: %+v", p)
	}
	conditions := p.Conditions.(operationConditions)
	if *conditions.Steps[0].IdleDurationSeconds != idle {
		t.Fatal("condition changed")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"operationID":"original-uid.0.idle"`, `"durationNanos":123456789`, `"wallJoules":0`, `"reason":"recorded-reason"`, `"durationNanos":null`, `"wallJoules":null`, `"phase":"Failed"`, `"verdict":"Rejected"`, `"fromPowerMode":"MAXN"`, `"toPowerMode":"MODE_15W"`, `"model":"frozen-model"`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("lost operation status %s: %s", required, raw)
		}
	}
	for _, forbidden := range []string{"stateObservations", "sampling", "prepare", "annotations", "secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("operation projection leaked %s: %s", forbidden, raw)
		}
	}
	status := p.Status.(operationStatus)
	if status.Steps[0].FromPowerMode != "MAXN" || status.Steps[0].ToPowerMode != "MODE_15W" || status.Steps[0].Model != "frozen-model" {
		t.Fatalf("lost safe recorded metadata: %+v", status.Steps[0])
	}
	if status.Steps[1].FromPowerMode != "" || status.Steps[1].ToPowerMode != "" || status.Steps[1].Model != "" {
		t.Fatalf("invented missing metadata: %+v", status.Steps[1])
	}
}
