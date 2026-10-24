package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/serving"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCatalogPreservesRawDistributionAndMissingMemory(t *testing.T) {
	batch := catalogBatch{profile: serving.BatchProfile{Batch: 2}}
	trial := profilederivation.Trial{Batch: 2, LatencyMilliseconds: []float64{1.25, 4, 2}}
	trial.NativeSamples = []profilederivation.NativeServiceSample{
		{BatchID: 1, BatchSize: 2, RunnerServiceMilliseconds: 0.25},
		{BatchID: 2, BatchSize: 2, RunnerServiceMilliseconds: 3},
		{BatchID: 3, BatchSize: 2, RunnerServiceMilliseconds: 1},
	}
	energy := profilederivation.EnergyTrial{Batch: 2, IncrementalWorkJoulesPerItem: 0.7}
	if err := batch.add(trial, energy, 1024); err != nil {
		t.Fatal(err)
	}
	if err := batch.add(trial, energy, 2048); err != nil {
		t.Fatal(err)
	}
	want := []float64{250000, 3000000, 1000000, 250000, 3000000, 1000000}
	for index, sample := range want {
		if batch.profile.DurationNanos[index] != sample {
			t.Fatalf("raw duration %d = %g, want %g", index, batch.profile.DurationNanos[index], sample)
		}
	}
	if batch.profile.MemoryBytes != 2048 || batch.missingMemory {
		t.Fatalf("measured trial aggregation = %+v", batch)
	}
	if err := batch.add(trial, energy, 0); err != nil || !batch.missingMemory {
		t.Fatalf("absent measured memory was filled: %+v, %v", batch, err)
	}
	trial.NativeSamples[0].RunnerServiceMilliseconds = math.NaN()
	if err := batch.add(trial, energy, 4096); err == nil {
		t.Fatal("invalid raw latency became a profile sample")
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
	profile.Condition.CPU.AffinityCPUs = []int{0, 1}
	profile.RuntimeMetadata.CPUAffinity = []int{0, 1}
	if !completeNativeProfile(profile) {
		t.Fatal("exact explicitly measured CPU mask excluded")
	}
	profile.Condition.CPU.AffinityCPUs = []int{2, 3}
	if completeNativeProfile(profile) {
		t.Fatal("profile relabeled to alternate CPU IDs")
	}
}

func TestCatalogLatestSuccessUsesFrozenConditionAndSourceCompletion(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	old := serving.Profile{ID: "old-campaign/uid", MeasuredAt: now,
		Condition: profilederivation.CurvePlanSpec{Name: "old-plan", NodeUID: "node-a", Backend: "ort-cpu", RuntimeFamily: "firstfloor", PlannedBatchSizes: []int{1, 2}}}
	newer := old
	newer.ID, newer.MeasuredAt, newer.Condition.Name = "new-campaign/uid", now.Add(time.Hour), "new-plan"
	newer.Condition.PlannedBatchSizes = []int{1, 2, 4}
	otherNode := old
	otherNode.ID, otherNode.Condition.NodeUID = "other-node/uid", "node-b"
	otherMeter := old
	otherMeter.ID = "other-meter/uid"
	otherMeter.MetricSource.MetricLabels = map[string]string{"meter": "independent"}
	otherMask := old
	otherMask.ID = "other-mask/uid"
	otherMask.Condition.CPU.AffinityCPUs = []int{4, 5}
	result, err := latestConditionProfiles([]serving.Profile{newer, old, otherNode, otherMeter, otherMask})
	if err != nil || len(result) != 4 || result[0].ID != newer.ID || result[1].ID != otherNode.ID || result[2].ID != otherMeter.ID || result[3].ID != otherMask.ID {
		t.Fatalf("latest source matching changed identity/scope: %+v %v", result, err)
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
	catalog, err := NewCatalog(store, objects, 1024, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	source := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
	view, err := catalog.Refresh(context.Background(), []string{source.Namespace, source.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || objects.opens != 1 {
		t.Fatalf("malformed source became eligible or was read twice: %+v, opens=%d, err=%v", view, objects.opens, err)
	}
	identity := apiObjectIdentity(campaign.Status.Result)
	objects.payload = append(payload, ' ')
	if _, err := catalog.readObject(context.Background(), identity); !errors.Is(err, evidencestore.ErrContentMismatch) {
		t.Fatalf("declared size mismatch accepted: %v", err)
	}
	opens := objects.opens
	identity.Size = 1025
	if _, err := catalog.readObject(context.Background(), identity); err == nil || objects.opens != opens {
		t.Fatalf("oversized reference reached object reader: opens=%d, err=%v", objects.opens, err)
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
			catalog, err := NewCatalog(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(campaign).Build()}, objects, 1024, 0.95)
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

func TestCatalogBindsEvidenceToRecordedRun(t *testing.T) {
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "active", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			Job:      &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion},
			ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: run.Namespace, Name: "job", UID: "other-job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: run.Namespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
	}
	if err := validateCatalogExecution(run, execution); err == nil || !strings.Contains(err.Error(), "identities") {
		t.Fatalf("foreign retained evidence accepted: %v", err)
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
