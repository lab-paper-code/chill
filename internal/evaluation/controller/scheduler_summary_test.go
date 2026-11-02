package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
)

func decodeSchedulerSummary(path string, summary string, value any) error {
	if path != "/api/v1/scheduler/state?view=summary" {
		return fmt.Errorf("expected scheduler summary request, got %q", path)
	}
	return json.Unmarshal([]byte(summary), value)
}

func TestReleaseRequestsSchedulerSummary(t *testing.T) {
	parent := fixtureRun("large-state")
	startRun(parent)
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionPoolAcquired, metav1.ConditionTrue, "Acquired", "")
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionCleanupComplete, metav1.ConditionFalse, api.EvaluationReasonReturnTimeout, "")
	r, _ := fixtureRuntime(t, parent)
	r.HTTP = func(_ context.Context, _ string, _ map[string]string, path string, value any) error {
		return decodeSchedulerSummary(path, `{"at":"2026-11-01T12:00:00Z","poolReturn":{"reservationCount":0,"outstandingCount":0,"activeActionCount":0,"latestResultEndedAt":"2026-11-01T11:59:59Z"}}`, value)
	}
	annotation := `{"reason":"writers stopped and pool returned","at":"2026-11-01T12:00:00Z"}`
	if _, err := (&RunReconciler{Runtime: r}).release(context.Background(), parent, annotation); err != nil {
		t.Fatal(err)
	}
	if occupied(parent) || isTrue(parent.Status.Conditions, api.EvaluationConditionPoolAcquired) || len(parent.Status.ReleaseRecords) != 1 {
		t.Fatal("scheduler summary prevented pool release")
	}
	if isTrue(parent.Status.Conditions, api.EvaluationConditionCleanupComplete) {
		t.Fatal("operator release erased cleanup failure")
	}
}

func TestInjectionSummaryKeepsEvidence(t *testing.T) {
	r, _, parent, stage := fixtureStageExecution(t)
	baseHTTP := r.HTTP
	var summary json.RawMessage
	if err := baseHTTP(context.Background(), parent.Namespace, parent.Status.FrozenConfig.Values, schedulerStatePath, &summary); err != nil {
		t.Fatal(err)
	}
	r.HTTP = func(ctx context.Context, namespace string, config map[string]string, path string, value any) error {
		if strings.Contains(path, "/scheduler/state") {
			return decodeSchedulerSummary(path, string(summary), value)
		}
		return baseHTTP(ctx, namespace, config, path, value)
	}
	evidence := &injectionEvidence{}
	if err := r.selectInjection(context.Background(), parent, stage, stage.Spec.FailureInjection[0], evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Record.PodRef == nil || evidence.Record.PodRef.UID != "pod-uid" || len(evidence.Candidates) != 1 {
		t.Fatalf("summary lost injection target identity: %+v", evidence)
	}
	if string(evidence.StateRaw) != string(summary) || strings.Contains(string(evidence.StateRaw), `"snapshot"`) {
		t.Fatal("injection evidence must retain the exact summary used for selection")
	}
}

func TestSchedulerSummaryPoolReturnGuards(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		returned     bool
	}{
		{"returned", `"poolReturn":{}`, true},
		{"missing return facts", `"instances":[]`, false},
		{"reservation", `"poolReturn":{"reservationCount":1}`, false},
		{"outstanding", `"poolReturn":{"outstandingCount":1}`, false},
		{"active action", `"poolReturn":{"activeActionCount":1}`, false},
		{"new result", `"poolReturn":{"latestResultEndedAt":"2026-11-01T12:00:00Z"}`, false},
		{"future result", `"poolReturn":{"latestResultEndedAt":"2026-11-01T12:00:01Z"}`, false},
		{"owned instance", `"poolReturn":{},"instances":[{"ownerWorkloadUID":"owned"}]`, false},
		{"positive routing", `"poolReturn":{},"routing":{"w":{"i":1}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var state schedulerState
			if err := json.Unmarshal([]byte(`{"at":"2026-11-01T12:00:00Z",`+tc.fields+`}`), &state); err != nil {
				t.Fatal(err)
			}
			if got := state.returned(fixtureTime, fixtureConfig().Data); got != tc.returned {
				t.Fatalf("returned=%v, want %v", got, tc.returned)
			}
		})
	}
}
