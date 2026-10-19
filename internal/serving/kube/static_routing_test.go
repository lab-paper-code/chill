package kube

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestStaticRoutingRequiresSourceRevision(t *testing.T) {
	controller := &SchedulerController{config: SchedulerControllerConfig{
		Settings: serving.SchedulerSettings{Algorithm: serving.SchedulerAlgorithmFixedHorizon},
	}}
	if err := controller.ApplyStaticRouting(context.Background(), nil, ""); err == nil {
		t.Fatal("manual routing reached capture/apply without source revision")
	}
}

func TestStaticRoutingCompletionRequiresCompleteReadbackAndAppearsInState(t *testing.T) {
	wanted := map[string]map[string]int32{"w1": {"physical-a": 1}, "w2": {"physical-b": 2}}
	for _, test := range []struct {
		name      string
		actual    map[string]map[string]int32
		wantError bool
	}{
		{name: "exact", actual: map[string]map[string]int32{"w2": {"physical-b": 2}, "w1": {"physical-a": 1}}},
		{name: "accepted drain retains extra route", actual: map[string]map[string]int32{"w1": {"physical-a": 1, "draining-old": 1}, "w2": {"physical-b": 2}}, wantError: true},
		{name: "extra UID", actual: map[string]map[string]int32{"w1": {"physical-a": 1}, "w2": {"physical-b": 2}, "stale": {}}, wantError: true},
		{name: "missing UID", actual: map[string]map[string]int32{"w1": {"physical-a": 1}}, wantError: true},
		{name: "missing target", actual: map[string]map[string]int32{"w1": {}, "w2": {"physical-b": 2}}, wantError: true},
		{name: "wrong weight", actual: map[string]map[string]int32{"w1": {"physical-a": 3}, "w2": {"physical-b": 2}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &SchedulerController{}
			result := OperationResult{Operation: Operation{
				ID: "manual-static-routing:42", Kind: manualStaticRoutingOperation, SourceConfigRV: "42",
				Target: &serving.SchedulerCandidate{Routing: wanted},
			}}
			var recorded OperationResult
			err := controller.finishStaticRouting(result, test.actual, nil, func(result OperationResult) error {
				recorded = result
				return nil
			})
			if (err != nil) != test.wantError {
				t.Fatalf("complete readback error = %v, want error %v", err, test.wantError)
			}
			state := controller.State()
			if len(state.Results) != 1 {
				t.Fatalf("manual result missing from scheduler state: %+v", state.Results)
			}
			actual := state.Results[0]
			if actual.Operation.Kind != manualStaticRoutingOperation || actual.Operation.SourceConfigRV != "42" || actual.Operation.Target != result.Operation.Target {
				t.Fatalf("manual provenance/expected target lost: %+v", actual.Operation)
			}
			if !maps.EqualFunc(actual.Actual.Routing, test.actual, func(left, right map[string]int32) bool { return maps.Equal(left, right) }) {
				t.Fatalf("physical actual readback lost: %v", actual.Actual.Routing)
			}
			wantStatus := nativeOperationComplete
			if test.wantError {
				wantStatus = nativeOperationFailed
			}
			if actual.Status != wantStatus || recorded.Status != wantStatus || actual.EndedAt.IsZero() {
				t.Fatalf("observation log/state completion disagree: state=%+v recorded=%+v", actual, recorded)
			}
			controller.reconcileSnapshot(serving.SchedulerSnapshot{}, nil, actual.EndedAt.Add(2*time.Second), time.Second)
			if len(controller.State().Results) != 0 {
				t.Fatal("manual result bypassed existing observation-window expiry")
			}
		})
	}
}

func TestStaticRoutingObservationLogFailureRemainsVisibleAsPartial(t *testing.T) {
	controller := &SchedulerController{}
	routing := map[string]map[string]int32{"w": {"physical": 1}}
	recordError := errors.New("observation log unavailable")
	err := controller.finishStaticRouting(OperationResult{
		Operation: Operation{Kind: manualStaticRoutingOperation, SourceConfigRV: "42", Target: &serving.SchedulerCandidate{Routing: routing}},
	}, routing, nil, func(OperationResult) error { return recordError })
	if !errors.Is(err, recordError) {
		t.Fatalf("observation log failure lost: %v", err)
	}
	state := controller.State()
	if len(state.Results) != 1 || state.Results[0].Status != nativeOperationPartial || state.Results[0].Error == "" {
		t.Fatalf("observation log failure incorrectly completed or hidden: %+v", state.Results)
	}
}

func TestStaticRoutingPreflightFailuresRemainVisibleAndUnapplied(t *testing.T) {
	for _, captureFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "physical source unavailable", true: "snapshot capture failure"}[captureFailure], func(t *testing.T) {
			ctx := context.Background()
			manager, _ := newManagerHarness(t)
			createManagerCatalog(t, manager)
			if _, err := manager.CreateWorkload(ctx, "workload", managerWorkloadSpec()); err != nil {
				t.Fatal(err)
			}
			definitions := manager.runtime.SchedulerInputs(time.Now).Workloads
			if len(definitions) != 1 {
				t.Fatalf("expected one current Workload, got %v", definitions)
			}
			routing := map[string]map[string]int{definitions[0].UID: {"unavailable-physical-source": 1}}
			var recorded []OperationResult
			controller := &SchedulerController{
				store: manager.store, kube: manager.observer.kube, runtime: manager.runtime,
				observer: manager.observer, observationLog: manager.observationLog,
				config: SchedulerControllerConfig{
					Settings: serving.SchedulerSettings{Algorithm: serving.SchedulerAlgorithmFixedHorizon, ObservationWindow: time.Minute},
					OnAction: func(result OperationResult) error {
						recorded = append(recorded, result)
						return nil
					},
				},
			}
			if captureFailure {
				controller.kube.(*kubefake.Clientset).PrependReactor("list", "nodes", func(ktesting.Action) (bool, kruntime.Object, error) {
					return true, nil, errors.New("actual snapshot unavailable")
				})
			}
			err := controller.ApplyStaticRouting(ctx, routing, "42")
			if err == nil {
				t.Fatal("unavailable static target was reported applied")
			}
			if !captureFailure && !strings.Contains(err.Error(), "not currently ready") {
				t.Fatalf("expected actual readiness failure, got %v", err)
			}
			state := controller.State()
			if len(recorded) != 1 || len(state.Results) != 1 {
				t.Fatalf("preflight failure hidden: recorded=%d state=%+v", len(recorded), state.Results)
			}
			result := state.Results[0]
			if result.Status != nativeOperationFailed || result.Error != err.Error() || result.Operation.SourceConfigRV != "42" || result.Operation.Kind != "manual-static-routing" {
				t.Fatalf("manual failure provenance lost: %+v", result)
			}
			if !maps.EqualFunc(result.Operation.StaticRouting, routing, func(left, right map[string]int) bool { return maps.Equal(left, right) }) {
				t.Fatalf("unapplied declaration lost: %v", result.Operation.StaticRouting)
			}
			if result.Actual.Routing[definitions[0].UID]["unavailable-physical-source"] != 0 {
				t.Fatal("failed declaration fabricated actual routing")
			}
		})
	}
}

func TestManualStaticRoutingRecordsPendingWithoutOverwritingRunningGlobalRoute(t *testing.T) {
	controller, target, _ := acknowledgedHandoffFixture(t)
	for _, run := range controller.runs {
		run.action.Status = nativeOperationRunning
	}
	var recorded []OperationResult
	controller.config.OnAction = func(result OperationResult) error {
		recorded = append(recorded, result)
		return nil
	}
	wanted := map[string]map[string]int{}
	for uid, weights := range target.Routing {
		wanted[uid] = map[string]int{}
		for id, weight := range weights {
			wanted[uid][id] = int(weight)
		}
	}
	err := controller.ApplyStaticRouting(context.Background(), wanted, "pending-owner-rv")
	if err == nil || !strings.Contains(err.Error(), "running global route") {
		t.Fatalf("manual write did not wait for the existing native ACK: %v", err)
	}
	if len(recorded) != 1 || recorded[0].Status != nativeOperationFailed ||
		recorded[0].Operation.SourceConfigRV != "pending-owner-rv" || len(recorded[0].Actual.Routing) == 0 {
		t.Fatalf("pending manual configuration lost its actual readback/provenance: %+v", recorded)
	}
	for _, run := range controller.runs {
		if run.action.Status != nativeOperationRunning {
			t.Fatal("manual pending configuration canceled or completed the existing route")
		}
	}
}

func TestStaticRoutingPartialFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name       string
		persisted  bool
		nativeACK  bool
		wantStatus string
	}{
		{name: "no physical or desired effect", wantStatus: nativeOperationFailed},
		{name: "desired persisted without native ACK", persisted: true, wantStatus: nativeOperationPartial},
		{name: "native ACK before later failure", nativeACK: true, wantStatus: nativeOperationPartial},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &SchedulerController{}
			routing := map[string]map[string]int32{"w": {"physical": 1}}
			result := OperationResult{
				Operation: Operation{Kind: manualStaticRoutingOperation, Target: &serving.SchedulerCandidate{Routing: routing}},
			}
			if test.persisted {
				result.Status = nativeOperationPartial
				result.Progress = []string{"physical: desired policy configuration persisted"}
			}
			if test.nativeACK {
				result.Actual.Native = map[string]firstfloor.State{
					"physical": {Ready: true, At: time.Now().UTC(), Metadata: firstfloor.Metadata{DecisionID: "route"}},
				}
			}
			applyErr := errors.New("later native state unavailable")
			var recorded OperationResult
			err := controller.finishStaticRouting(result, routing, applyErr, func(result OperationResult) error {
				recorded = result
				return nil
			})
			if !errors.Is(err, applyErr) {
				t.Fatalf("physical failure lost: %v", err)
			}
			actual := controller.State().Results[0]
			if actual.Status != test.wantStatus || recorded.Status != test.wantStatus {
				t.Fatalf("physical effect classification lost: state=%s recorded=%s", actual.Status, recorded.Status)
			}
		})
	}
}
