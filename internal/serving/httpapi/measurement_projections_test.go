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
