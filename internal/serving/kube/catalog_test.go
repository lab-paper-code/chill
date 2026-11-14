package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/test/fixtures"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func catalogServiceSamples(duration int64) []firstfloor.ServiceSample {
	return fixtures.ServiceSamples("catalog-physical-trial", duration)
}

func TestCatalogKeysObservedRuntimeVersionAndProvider(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t)
	view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("catalog=%+v error=%v", view, err)
	}
	profile := view.Profiles[0]
	if profile.RuntimeMetadata == nil || profile.Condition.RuntimeVersion == "" ||
		profile.Condition.RuntimeVersion != profile.RuntimeMetadata.RuntimeVersion ||
		profile.Condition.RuntimeProvider == "" {
		t.Fatalf("observed runtime semantics missing from condition: %+v", profile.Condition)
	}
	if facts := view.MeasurementFacts[profile.ID]; facts.Condition.RuntimeVersion != profile.Condition.RuntimeVersion ||
		facts.Condition.RuntimeProvider != profile.Condition.RuntimeProvider {
		t.Fatalf("Query/Serving runtime identity diverged: %+v %+v", facts.Condition, profile.Condition)
	}
}

func TestCatalogSupportsExactlyInitializedMeasuredSparseBatches(t *testing.T) {
	profile := serving.Profile{
		Condition: profilederivation.CurvePlanSpec{RuntimeFamily: "firstfloor", Backend: "ort-cpu",
			Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}, PlannedBatchSizes: []int{1, 2, 3, 4}},
		RuntimeMetadata: &firstfloor.RuntimeMetadata{Runtime: "ort-cpu", InitializedBatches: []int{1, 2, 4}},
		Batches:         []serving.BatchProfile{{Batch: 1}, {Batch: 2}, {Batch: 4}},
	}
	if !completeNativeProfile(profile) {
		t.Fatal("exact measured sparse domain excluded")
	}
	uninitialized := profile
	uninitialized.Batches = []serving.BatchProfile{{Batch: 1}, {Batch: 3}}
	if completeNativeProfile(uninitialized) {
		t.Fatal("uninitialized batch accepted")
	}
	missingOne := profile
	missingOne.Batches = profile.Batches[1:]
	if completeNativeProfile(missingOne) {
		t.Fatal("missing batch 1 accepted")
	}
	standalone := profile
	standalone.Condition.RuntimeFamily = "onnxruntime"
	if completeNativeProfile(standalone) {
		t.Fatal("standalone became native evidence")
	}
	profile.RuntimeMetadata.CPUAffinity = []int{7}
	profile.RuntimeMetadata.IntraOpThreads = 0
	if !completeNativeProfile(profile) {
		t.Fatal("shared runtime observation became a CPU allocation gate")
	}
}

func TestCatalogLatestSuccessUsesFrozenConditionAndSourceCompletion(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	old := serving.Profile{ID: "old-campaign/uid", MeasuredAt: now,
		Condition: profilederivation.CurvePlanSpec{Name: "old-plan", NodeUID: "node-a", Backend: "ort-cpu", RuntimeFamily: "firstfloor", PlannedBatchSizes: []int{1, 2}}}
	newer := old
	newer.ID, newer.MeasuredAt, newer.Condition.Name = "new-campaign/uid", now.Add(time.Hour), "new-plan"
	newer.Condition.PlannedBatchSizes = []int{1, 2, 4}
	newer.Condition.RuntimeImage = "current-runtime"
	otherNode := old
	otherNode.ID, otherNode.Condition.NodeUID = "other-node/uid", "node-b"
	otherMeter := old
	otherMeter.ID = "other-meter/uid"
	otherMeter.MetricSource.MetricLabels = map[string]string{"meter": "independent"}
	otherRuntime := old
	otherRuntime.ID = "other-runtime/uid"
	otherRuntime.Condition.RuntimeImage = "new-runtime"
	result, err := serving.SelectConditionProfiles([]serving.Profile{newer, old, otherNode, otherMeter, otherRuntime}, nil)
	if err != nil || len(result) != 4 || result[0].ID != newer.ID || result[1].ID != old.ID || result[2].ID != otherNode.ID || result[3].ID != otherRuntime.ID || result[0].Condition.RuntimeImage != "current-runtime" {
		t.Fatalf("latest semantic condition/source completion changed selection: %+v %v", result, err)
	}
}

func TestCatalogBoundsAndStrictlyDecodesTerminalObjects(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	campaign := &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "campaign", UID: "campaign-uid", Generation: 1},
		Status:     edgev1alpha1.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1, Outcome: "MinimumFound"},
	}
	payload := []byte(`{"schemaVersion":"retired","result":{"detail":{"memoryBytes":4096}}}`)
	campaign.Status.Result = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "evidence", Key: "result.json", SchemaVersion: "retired", Size: int64(len(payload))}
	objects := &catalogTestObjects{payload: payload}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(campaign).Build()}
	catalog, err := NewCatalog(store, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	source := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
	view, err := catalog.Refresh(context.Background(), []string{source.Namespace, source.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || objects.opens != 1 {
		t.Fatalf("malformed source became eligible or was read twice: %+v, opens=%d, err=%v", view, objects.opens, err)
	}
	stored := campaign.Status.Result
	identity := evidencestore.ObjectIdentity{Store: stored.Store, Key: stored.Key, Size: stored.Size}
	objects.payload = append(payload, ' ')
	if _, err := catalog.readObject(context.Background(), identity); err != nil {
		t.Fatalf("harmless retained formatting change blocked reading: %v", err)
	}
	objects.payload = make([]byte, 1025)
	if _, err := catalog.readObject(context.Background(), identity); err == nil {
		t.Fatal("oversized object escaped bounded reader")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := catalog.Refresh(ctx, []string{source.Namespace}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh returned publishable view: %v", err)
	}
}

func TestCatalogSkipsIncompleteAndFailedMeasurementWithoutEvidenceReads(t *testing.T) {
	for _, phase := range []string{"Measuring", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := edgev1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			campaign := &edgev1alpha1.BatchEnergyOptimization{
				ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "campaign", Generation: 1},
				Status: edgev1alpha1.BatchEnergyOptimizationStatus{
					Phase: phase, ObservedGeneration: 1,
					Conditions: []metav1.Condition{{Type: "Ready", Reason: "ActualReason", Message: "actual failure detail"}},
				},
			}
			objects := &catalogTestObjects{}
			catalog, err := NewCatalog(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(campaign).Build()}, objects, 1024)
			if err != nil {
				t.Fatal(err)
			}
			view, err := catalog.Refresh(context.Background(), []string{campaign.Namespace})
			if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 0 || objects.opens != 0 {
				t.Fatalf("terminal gate: %+v %v", view, err)
			}
		})
	}
}

type catalogTestObjects struct {
	payload []byte
	opens   int
}

func (s *catalogTestObjects) OpenLocation(context.Context, evidencestore.ObjectLocation) (io.ReadCloser, error) {
	s.opens++
	return io.NopCloser(bytes.NewReader(s.payload)), nil
}

func TestCatalogLatestFailureDoesNotHideHistoryOrReviveSuccess(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t)
	ctx := context.Background()
	before, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(before.CurrentProfiles()) != 1 {
		t.Fatalf("fixture current selection: %+v %v", before.CurrentMeasurements, err)
	}
	original := &edgev1alpha1.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, original); err != nil {
		t.Fatal(err)
	}
	failed := original.DeepCopy()
	failed.Name, failed.UID, failed.ResourceVersion = "newer-failure", "failed-uid", ""
	completedAt := before.Profiles[0].MeasuredAt.Add(time.Hour)
	failed.Status = edgev1alpha1.BatchEnergyOptimizationStatus{Phase: "Failed", ObservedGeneration: failed.Generation,
		Conditions: []metav1.Condition{
			{Type: "Other", LastTransitionTime: metav1.NewTime(completedAt.Add(time.Hour))},
			// Catalog recency uses the first completion even without a current generation.
			{Type: "MeasurementComplete", ObservedGeneration: 0, LastTransitionTime: metav1.NewTime(completedAt)},
			{Type: "MeasurementComplete", ObservedGeneration: failed.Generation, LastTransitionTime: metav1.NewTime(completedAt.Add(-2 * time.Hour))},
		}}
	if err := catalog.store.client.Create(ctx, failed); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 || len(view.CurrentProfiles()) != 0 || len(view.CurrentMeasurements) != 1 ||
		view.CurrentMeasurements[0].ID != failed.Namespace+"/"+failed.Name+"/"+string(failed.UID) ||
		!view.CurrentMeasurements[0].MeasuredAt.Equal(completedAt) {
		t.Fatalf("latest failure fell back or erased history: %+v %v", view, err)
	}
}

func TestCatalogOfflineSemanticSelectionPreservesEvidence(t *testing.T) {
	directory := os.Getenv("CHILL_PROFILE_BASELINE")
	if directory == "" {
		t.Skip("set CHILL_PROFILE_BASELINE to the read-only advisor1-profile-baseline")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "after-1702", "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Profiles []serving.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	profiles, err := serving.SelectConditionProfiles(snapshot.Profiles, nil)
	if err != nil || len(snapshot.Profiles) != 259 || len(profiles) == 0 || len(profiles) > 242 {
		t.Fatalf("semantic public population = %d -> %d: %v", len(snapshot.Profiles), len(profiles), err)
	}
	original := make(map[string]serving.Profile, len(snapshot.Profiles))
	for _, profile := range snapshot.Profiles {
		original[profile.ID] = profile
	}
	for _, profile := range profiles {
		old := original[profile.ID]
		if len(profile.Batches) != len(old.Batches) {
			t.Fatal("batch population changed", profile.ID)
		}
		for index, batch := range profile.Batches {
			if batch.Batch != old.Batches[index].Batch || batch.MemoryBytes != old.Batches[index].MemoryBytes {
				t.Fatal("historical batch changed", profile.ID, batch.Batch)
			}
		}
		delete(original, profile.ID)
	}
	raw, err = os.ReadFile(filepath.Join(directory, "expected-dropped.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	dropped := make(map[string]bool, len(original))
	for id := range original {
		dropped[id] = true
	}
	for _, id := range expected {
		if !dropped[id] {
			t.Fatalf("historical superseded source revived: %s", id)
		}
	}
}
