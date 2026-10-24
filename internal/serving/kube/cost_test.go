package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type costTestObjects struct {
	objects map[string][]byte
	opens   map[string]int
	listErr error
}

func (o *costTestObjects) List(_ context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	if o.listErr != nil {
		return nil, o.listErr
	}
	identities := make([]evidencestore.ObjectIdentity, 0, len(o.objects))
	for key, raw := range o.objects {
		if strings.HasPrefix(key, prefix) {
			identities = append(identities, evidencestore.ObjectIdentity{Store: "store", Key: key, Size: int64(len(raw))})
		}
	}
	return identities, nil
}

func (o *costTestObjects) Open(_ context.Context, identity evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	o.opens[identity.Key]++
	return io.NopCloser(bytes.NewReader(o.objects[identity.Key])), nil
}

var costTestMeter = MeterDeclaration{ID: "wall", Node: serving.NodeReference{Name: "node", UID: "uid"}, Scope: measurementWallScope,
	Source: metricobserver.SourceIdentity{Kind: "edge-metrics", NodeName: "node", Endpoint: "http://node", Metric: "watts"}}

func costTestRecord(t *testing.T, id, mode string, end time.Time) (string, []byte) {
	t.Helper()
	start := end.Add(-time.Second)
	joules := 20.0
	operation := Operation{ID: id, Kind: operationPrepare, Node: costTestMeter.Node}
	record := OperationMeasurement{
		SchemaVersion: OperationMeasurementSchema, Verdict: measurementAccepted, WallJoules: &joules,
		Request: MeasurementRequest{Operation: operation, Meter: costTestMeter, Store: "store",
			Sampling: metricobserver.Request{Interval: time.Second, Duration: time.Second, RequestTimeout: time.Millisecond, MaxEvents: 10},
			Coverage: powerwindow.Policy{MaximumBoundaryDistance: time.Millisecond, MinimumCoverage: 1, MaximumGap: time.Second}},
		Result: OperationResult{Operation: operation, Status: nativeOperationComplete, StartedAt: start, EndedAt: end,
			Before: OperationState{PowerMode: mode}, Actual: OperationState{PowerMode: mode, States: []string{"profile:[0]::1024:1"}}},
		Power: metricobserver.Result{Source: costTestMeter.Source,
			Samples: []metricobserver.Sample{{ObservedAt: start, Watts: 20}, {ObservedAt: end, Watts: 20}}},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return OperationMeasurementKey(operation), raw
}

func TestCostEvidenceDerivesLatestAcceptedCostPerCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	for _, r := range []struct {
		id, mode string
		end      time.Time
	}{{"old-fixed", "fixed", at}, {"new-fixed", "fixed", at.Add(time.Hour)}, {"eco", "eco", at}} {
		key, raw := costTestRecord(t, r.id, r.mode, r.end)
		objects.objects[key] = raw
	}
	invalid := OperationMeasurementPrefix + "uid/invalid.json"
	objects.objects[invalid] = []byte(`{"verdict":"Accepted"}`)
	evidence, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().WithScheme(scheme).Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 2 {
		view, err := evidence.Refresh(ctx, []string{"measurements"}, []MeterDeclaration{costTestMeter}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]time.Time{}
		for _, cost := range view.Costs {
			ids[cost.ID] = cost.MeasuredAt
			if cost.Trials[0].Joules != 20 || cost.Trials[0].EnergyUncertaintyJoules != 0 {
				t.Fatalf("legacy Accepted nominal/absent-interval semantics changed: %+v", cost)
			}
		}
		// Each Node power mode keeps its latest cost; the invalid record does not block the others.
		if len(ids) != 2 || !ids["new-fixed"].Equal(at.Add(time.Hour)) || ids["eco"].IsZero() ||
			len(view.Issues) != 1 || view.Issues[0].Source != invalid || view.Issues[0].Code != CostEvidenceInvalid {
			t.Fatalf("derived costs %+v issues %+v", ids, view.Issues)
		}
	}
	for key, opens := range objects.opens {
		if opens != 1 {
			t.Fatalf("immutable %s read %d times", key, opens)
		}
	}
	other := costTestMeter
	other.ID = "other"
	view, err := evidence.Refresh(ctx, nil, []MeterDeclaration{other}, nil)
	undeclared := 0
	for _, issue := range view.Issues {
		if issue.Code == MeterUndeclared {
			undeclared++
		}
	}
	if err != nil || len(view.Costs) != 0 || undeclared != 3 {
		t.Fatalf("records with an unconfigured meter contributed: %+v %v", view, err)
	}
	objects.listErr = errors.New("list denied")
	if _, err = evidence.Refresh(ctx, nil, []MeterDeclaration{costTestMeter}, nil); err == nil {
		t.Fatal("listing failure produced a partial cost view")
	}
}

func TestCostEvidenceIgnoresNonAcceptedAttempts(t *testing.T) {
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	insufficient := func(id string, end time.Time) (string, []byte) {
		key, raw := costTestRecord(t, id, "fixed", end)
		return key, bytes.Replace(raw, []byte(`"verdict":"Accepted"`), []byte(`"verdict":"Insufficient"`), 1)
	}
	for _, withAccepted := range []bool{true, false} {
		objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
		key, raw := insufficient("retry-gap", at.Add(time.Hour))
		objects.objects[key] = raw
		if withAccepted {
			key, raw = costTestRecord(t, "accepted", "fixed", at)
			objects.objects[key] = raw
		}
		evidence, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().Build()}, objects, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		view, err := evidence.Refresh(context.Background(), nil, []MeterDeclaration{costTestMeter}, nil)
		wantCosts := 0
		if withAccepted {
			wantCosts = 1
		}
		if err != nil || len(view.Issues) != 0 || len(view.Costs) != wantCosts || (withAccepted && view.Costs[0].ID != "accepted") {
			t.Fatalf("withAccepted=%v: costs %+v issues %+v err %v", withAccepted, view.Costs, view.Issues, err)
		}
	}
}

func TestCostEvidenceProjectsUncertainAcceptedEnergyWithoutPrecisionRejection(t *testing.T) {
	at := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	for _, kind := range []string{operationPrepare, operationIdle} {
		_, raw := costTestRecord(t, kind, "fixed", at)
		var record OperationMeasurement
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		record.Request.Operation.Kind, record.Result.Operation.Kind = kind, kind
		record.Request.Timing = &edgev1alpha1.MeasurementTimingSpec{
			ChangeIntervalP95Seconds: "0.5", MaximumRequestSeconds: "0.25", MaximumReceiptGapSeconds: "0.25", Source: "approved-observation",
		}
		// New measurements have real expanded support, not a precision or
		// nominal-coverage admission threshold. This slope gives 15 +/- 10 J.
		record.Request.Coverage = powerwindow.Policy{MaximumGap: time.Second}
		start := record.Result.StartedAt
		record.Power.Samples = []metricobserver.Sample{
			{ObservedAt: start.Add(-time.Second), Watts: 0},
			{ObservedAt: start, Watts: 10},
			{ObservedAt: at, Watts: 20},
			{ObservedAt: at.Add(time.Second), Watts: 30},
		}
		joules, bound, gap := 15.0, 10.0, 0.0
		record.WallJoules, record.EnergyUncertaintyJoules = &joules, &bound
		record.PhaseSensitivityJoules, record.MissingGapJoules = &bound, &gap
		if kind == operationIdle {
			record.MeanWatts = &joules
			record.Result.Before = record.Result.Actual
			record.StateObservations = []OperationState{record.Result.Before, record.Result.Actual}
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		objects.objects[OperationMeasurementKey(record.Request.Operation)] = raw
	}
	evidence, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	view, err := evidence.Refresh(context.Background(), nil, []MeterDeclaration{costTestMeter}, nil)
	if err != nil || len(view.Issues) != 0 || len(view.Costs) != 1 || len(view.Baselines) != 1 {
		t.Fatalf("physically valid high-B records were rejected: %+v %v", view, err)
	}
	if cost := view.Costs[0]; cost.Trials[0].Joules != 15 || cost.Trials[0].EnergyUncertaintyJoules != 10 {
		t.Fatalf("projection changed nominal or lost B: %+v", cost)
	}
	if baseline := view.Baselines[0]; baseline.Watts != 15 || baseline.WattsUncertainty != 10 {
		t.Fatalf("idle B/window did not survive projection: %+v", baseline)
	}
}

func TestCostEvidencePowerCostNeedsConfiguredMeterAndKeepsLoadedStatesApart(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	measurement := &edgev1alpha1.PowerModeTransitionCost{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measurements", Name: "power", UID: "power-uid", Generation: 1},
		Status: edgev1alpha1.PowerModeTransitionCostStatus{ObservedGeneration: 1, Phase: edgev1alpha1.PowerModeTransitionCostPhaseComplete,
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{Store: "store", Key: "power/result.json", Size: 1}},
	}
	objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	evidence, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(measurement).Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	power := func(id string, state []string, measuredAt time.Time) serving.OperationCost {
		return serving.OperationCost{ID: id, Node: costTestMeter.Node, Kind: operationPower, From: "fixed", To: "eco", BeforeState: state, MeasuredAt: measuredAt}
	}
	// Verified trials are cached per UID and result identity; only the meter binding is per refresh.
	evidence.power["measurements/power/power-uid/store/power/result.json"] = powerEvidence{
		node: costTestMeter.Node, source: costTestMeter.Source, costs: []serving.OperationCost{power("measurements/power/fixed->eco", []string{"a"}, at)},
	}
	// A serving-operation power record on the empty Node is the empty-state condition of the same pair.
	key, raw := costTestRecord(t, "raise", "fixed", at)
	var record OperationMeasurement
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Request.Operation.Kind, record.Result.Operation.Kind = operationPower, operationPower
	record.Result.Actual = OperationState{PowerMode: "eco"}
	if objects.objects[key], err = json.Marshal(record); err != nil {
		t.Fatal(err)
	}
	view, err := evidence.Refresh(context.Background(), []string{"measurements"}, nil, nil)
	if err != nil || len(view.Costs) != 0 || len(view.Issues) != 2 || view.Issues[0].Code != MeterUndeclared || view.Issues[1].Code != MeterUndeclared {
		t.Fatalf("power cost without a configured meter contributed: %+v %v", view, err)
	}
	view, err = evidence.Refresh(context.Background(), []string{"measurements"}, []MeterDeclaration{costTestMeter}, nil)
	empty := slices.IndexFunc(view.Costs, func(c serving.OperationCost) bool { return c.ID == "raise" })
	if err != nil || len(view.Costs) != 2 || view.Costs[0].Meter != costTestMeter.ID || empty < 0 || view.Costs[empty].From != record.Result.Before.PowerMode || view.Costs[empty].To != record.Result.Actual.PowerMode || len(view.Costs[empty].BeforeState) != 0 {
		t.Fatalf("configured meter not bound or empty-Node power record missing: %+v %v", view, err)
	}
	latest := latestCosts([]serving.OperationCost{
		power("old-a", []string{"a"}, at), power("new-a", []string{"a"}, at.Add(time.Hour)), power("b", []string{"b"}, at),
	})
	if len(latest) != 2 || latest[0].ID != "new-a" || latest[1].ID != "b" {
		t.Fatalf("power costs mixed loaded states or ignored trial completion time: %+v", latest)
	}
}
