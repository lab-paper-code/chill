package kube

import (
	"context"
	"encoding/json"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCostEvidenceRetainsHistoricalMeasurementCells(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := serving.Profile{ID: "profile"}
	profile.Condition.LogicalModel = "model"
	profile.Condition.RuntimeFamily = "firstfloor"
	profile.Condition.Backend = "ort-cpu"
	profile.Condition.Artifact.ExecutionPath = catalogOccupancyPath
	key, raw := costTestRecord(t, servingstate.OperationPrepare, identityTestMode, time.Now().UTC())
	var record OperationMeasurement
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Request.Operation.Prepare = &servingstate.PrepareSpec{Instance: serving.SchedulerInstance{Profile: &profile}}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	objects := &costTestObjects{objects: map[string][]byte{key: raw}, opens: map[string]int{}}
	e, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().WithScheme(scheme).Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	view, err := e.Refresh(context.Background(), nil, []MeterDeclaration{costTestMeter}, nil)
	if err != nil || len(view.Costs) != 1 || view.Costs[0].StateIdentities["profile"] != serving.MeasurementProfileIdentity(&profile) {
		t.Fatalf("historical prepare identity lost: %+v %v", view, err)
	}
	cell := view.Costs[0].StateIdentities["profile"]
	latest := view.Costs[0]
	latest.ID = "latest"
	latest.MeasuredAt = latest.MeasuredAt.Add(time.Second)
	latest.To = "replacement::1024"
	latest.StateIdentities = map[string]string{"replacement": cell}
	if costs := latestCosts([]serving.OperationCost{view.Costs[0], latest}); len(costs) != 1 || costs[0].ID != "latest" {
		t.Fatalf("cost cells still use profile IDs: %+v", costs)
	}
	old := serving.NodeBaseline{ID: "old", Node: costTestMeter.Node, PowerMode: identityTestMode, State: []string{"profile:[0]::1024:1"}, StateIdentities: map[string]string{"profile": cell}}
	new := old
	new.ID = identityLatestBaseline
	new.MeasuredAt = old.MeasuredAt.Add(time.Second)
	new.State = []string{"replacement::2048"}
	new.StateIdentities = latest.StateIdentities
	if baselines := latestBaselines([]serving.NodeBaseline{old, new}); len(baselines) != 1 || baselines[0].ID != identityLatestBaseline {
		t.Fatalf("baseline cells still use profile IDs: %+v", baselines)
	}
	power := view.Costs[0]
	power.Kind = servingstate.OperationPower
	power.From = identityTestMode
	power.To = lowMode
	power.BeforeState = old.State
	power.TargetState = old.State
	changed := power
	changed.ID = "latest-power"
	changed.MeasuredAt = power.MeasuredAt.Add(time.Second)
	changed.BeforeState = new.State
	changed.TargetState = new.State
	changed.StateIdentities = new.StateIdentities
	if costs := latestCosts([]serving.OperationCost{power, changed}); len(costs) != 1 || costs[0].ID != "latest-power" {
		t.Fatalf("power cells still use profile IDs: %+v", costs)
	}
}

const (
	identityTestMode       = "identity-mode"
	identityLatestBaseline = "identity-latest"
)
