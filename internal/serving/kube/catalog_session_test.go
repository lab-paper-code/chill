package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type catalogEvidenceObjects map[evidencestore.ObjectLocation][]byte

func (objects catalogEvidenceObjects) OpenLocation(_ context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	payload, exists := objects[location]
	if !exists {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

// catalogTestParameter decodes a session parameter's wire form; only the
// derivation constructs one otherwise.
func catalogTestParameter(_ testing.TB, wire string) profilederivation.SessionParameter {
	var p profilederivation.SessionParameter
	if err := json.Unmarshal([]byte(wire), &p); err != nil {
		panic(err)
	}
	return p
}

// catalogTestSlope and catalogTestPower give hand-built profiles the session
// parameters the scheduler's node power model reads; no awake step.
func catalogTestSlope(joules string) *profilederivation.SessionParameter {
	p := catalogTestParameter(nil, `{"status":"Identified","value":`+joules+`}`)
	return &p
}

func catalogTestPower(idle string) *serving.ProfilePower {
	return &serving.ProfilePower{IdleWatts: *catalogTestSlope(idle), AwakeStepWatts: *catalogTestSlope("0"), HoldSeconds: *catalogTestSlope("0")}
}

var catalogTestMeter = metricobserver.SourceIdentity{Kind: "edgemetrics", NodeName: "node-a", Endpoint: "http://node-a:9100/metrics", Metric: "node_watts"}

// catalogSessionResult is a small hand-made session result for batches 1 and 2
// of an ort-cpu firstfloor configuration on node-a.
func catalogSessionResult(t testing.TB) profilederivation.SessionResult {
	t.Helper()
	meter, err := json.Marshal(catalogTestMeter)
	if err != nil {
		t.Fatal(err)
	}
	identified := func(value string) profilederivation.SessionParameter {
		return catalogTestParameter(t, `{"status":"Identified","value":`+value+`}`)
	}
	actor := firstfloor.Config{Model: "model", Backend: "ort-cpu", RuntimeImage: "registry/runtime@sha256:" + strings.Repeat("a", 64),
		BatchSizes: []int{1, 2}, MaxBatchSize: 2, MaxQueueRequests: 8, WarmupBatchSizes: []int{1, 2}, WarmupIterations: 1,
		MemoryRequestBytes: 64 << 20, MemoryLimitBytes: 64 << 20}
	result := profilederivation.SessionResult{
		SchemaVersion: profilederivation.SessionResultSchemaVersion,
		Condition: profilederivation.SessionCondition{NodeName: "node-a", NodeUID: "node-a-uid", DeviceClass: "cpu", PowerMode: "fixed",
			LogicalModel: "model", RuntimeFamily: "firstfloor", Backend: "ort-cpu", RuntimeImage: actor.RuntimeImage,
			Artifact:     profilederivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Files: []string{"/model/model.onnx"}},
			Preprocessor: profilederivation.CurvePreprocessorContract{Name: "pre", Inputs: []string{"x"}, Outputs: []string{"y"}},
			Dataset:      &profilederivation.CurveDatasetContract{Name: "data", UID: "data-uid"},
			Firstfloor:   &modelcompat.FirstfloorSettings{MaxBatchSize: 2, MaxQueueSize: 8}, Actor: actor},
		Power: profilederivation.SessionPower{IdleWatts: identified("10"), AwakeStepWatts: identified("4"), HoldSeconds: identified("0.5")},
		// A session keeps the metadata of its batch-1 instance, which
		// initialized only its own batch size.
		RuntimeMetadata: &firstfloor.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "1.19.0", Providers: []string{"CPUExecutionProvider"},
			ModelFiles: []string{"/model/model.onnx"}, Inputs: []string{"x"}, Outputs: []string{"y"}, InitializedBatches: []int{1}},
		MeterSource: meter,
	}
	for _, b := range []int{1, 2} {
		rate := float64(100 * b)
		result.Batches = append(result.Batches, profilederivation.SessionBatch{Batch: b,
			ServiceNanos: []int64{int64(b) * 9e6, int64(b) * 10e6, int64(b) * 11e6}, PrecedingIdleNanos: []int64{0, 0, 1e9},
			DispatchOccupancyNanos: []float64{300, 100, 200}, SaturationInputRate: &rate,
			JoulesPerInput: identified([]string{"0.3", "0.2"}[b-1]), PeakMemoryBytes: 32 << 20, MemoryKind: "peak"})
	}
	return result
}

func catalogSessionCampaign(name string, uid types.UID, result *edgev1alpha1.ProfilingRunEvidenceReference, completed time.Time) *edgev1alpha1.BatchEnergyOptimization {
	return &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: name, UID: uid, Generation: 1},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{State: edgev1alpha1.MeasurementStateSpec{TargetNode: "node-a",
			Candidate: edgev1alpha1.ProfilingRunCandidateSpec{ModelSpec: "model", DeviceClass: "cpu", ExecutionPath: "cpu", PowerMode: "fixed"}}},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{Phase: catalogCompletePhase, ObservedGeneration: 1, Result: result,
			Conditions: []metav1.Condition{{Type: "MeasurementComplete", Status: metav1.ConditionTrue, Reason: "Complete", LastTransitionTime: metav1.NewTime(completed)}}},
	}
}

// catalogEvidenceFixture is one Complete measurement whose stored result is
// the session result above.
func catalogEvidenceFixture(t *testing.T) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	t.Helper()
	payload, err := json.Marshal(catalogSessionResult(t))
	if err != nil {
		t.Fatal(err)
	}
	return catalogStoredFixture(t, payload, profilederivation.SessionResultSchemaVersion)
}

func catalogStoredFixture(t *testing.T, payload []byte, schema string) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	t.Helper()
	identity := evidencestore.ObjectIdentity{Store: "evidence", Key: "batch-energy-optimizations/campaign-uid/result.json", Size: int64(len(payload))}
	objects := catalogEvidenceObjects{identity.Location(): payload}
	campaign := catalogSessionCampaign("catalog-campaign", "campaign-uid", &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: identity.Store, Key: identity.Key, SchemaVersion: schema, Size: identity.Size}, time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(client.Object(campaign)).Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}, objects, identity
}

// A stored session result becomes the profile the scheduler reads: power,
// per-batch slopes, records, occupancy, memory, runtime facts and the meter
// a ChillServer meter declaration binds.
func TestCatalogBuildsProfileFromSessionResult(t *testing.T) {
	catalog, source, _, identity := catalogEvidenceFixture(t)
	view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 || len(view.CurrentProfiles()) != 1 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	p := view.Profiles[0]
	idle, idleOK := p.Power.IdleWatts.Value()
	step, _ := p.Power.AwakeStepWatts.Value()
	slope, slopeOK := p.Batches[1].InputSlopeJoules.Value()
	if p.ID != "chill-system/catalog-campaign/campaign-uid" || p.Reference != identity || p.NodeName != "node-a" ||
		!idleOK || idle != 10 || step != 4 || !slopeOK || slope != 0.2 || *p.Batches[1].SaturationInputRate != 200 ||
		len(p.Batches) != 2 || len(p.Batches[0].ServiceSamples) != 3 || p.Batches[0].ServiceSamples[2].TrialID != "campaign-uid" ||
		*p.Batches[0].ServiceSamples[2].PrecedingIdleNanos != 1e9 || p.Batches[0].ServiceDistribution == nil ||
		p.Batches[0].MemoryBytes != 32<<20 || p.Batches[0].MemoryBasis.Observation != "peak" || len(p.Batches[0].DispatchOccupancyNanos) != 3 ||
		!p.MeasuredAt.Equal(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("profile mapping: %+v", p)
	}
	assertCatalogSessionCondition(t, p)
	instance := serving.SchedulerInstance{Node: serving.NodeReference{Name: "node-a", UID: "node-a-uid"}, Profile: &p}
	bindProfileMeter(&instance, []servingops.MeterDeclaration{{ID: "wall-node-a", Node: instance.Node, Scope: servingstate.MeasurementWallScope, Source: catalogTestMeter}})
	if instance.IdleMeter != "wall-node-a" {
		t.Fatalf("session profile meter not bound: %+v", instance)
	}
}

// assertCatalogSessionCondition checks the condition the catalog projects from
// the session result's condition, runtime report and meter.
func assertCatalogSessionCondition(t *testing.T, p serving.Profile) {
	t.Helper()
	c := p.Condition
	if c.RuntimeVersion != "1.19.0" || c.RuntimeProvider != "CPUExecutionProvider" || len(c.PlannedBatchSizes) != 2 ||
		c.ActiveProtocol.Dataset == nil || c.ActiveProtocol.Dataset.UID != "data-uid" || c.NodeUID != "node-a-uid" || c.Actor.MaxBatchSize != 2 ||
		p.RuntimeMetadata == nil || !sameWallSeries(p.MetricSource, catalogTestMeter) || p.MetricSource.NodeName != "node-a" {
		t.Fatalf("condition projection: %+v", c)
	}
}

// A Complete measurement whose result is from before profile sessions, or is
// not a session result, is a catalog issue saying so, never a profile.
func TestCatalogReportsOldSchemaResultAsIssue(t *testing.T) {
	for _, tc := range []struct {
		payload, schema, want string
	}{
		{`{"schemaVersion":"chill.dacs.io/batch-energy-optimization-result.v1alpha5","result":{}}`, "chill.dacs.io/batch-energy-optimization-result.v1alpha5",
			`has schema "chill.dacs.io/batch-energy-optimization-result.v1alpha5", not "` + profilederivation.SessionResultSchemaVersion + `"; a measurement from before profile sessions is not read: delete it and measure again`},
		{`not json`, profilederivation.SessionResultSchemaVersion, "is not a decodable session result"},
	} {
		catalog, source, _, _ := catalogStoredFixture(t, []byte(tc.payload), tc.schema)
		view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
		if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || view.Insufficiencies[0].Code != measurementResultUnreadableCode ||
			!strings.Contains(view.Insufficiencies[0].Reason, tc.want) {
			t.Fatalf("payload %q: view=%+v err=%v", tc.payload, view, err)
		}
	}
}
