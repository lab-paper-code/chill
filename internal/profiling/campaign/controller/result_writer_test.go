package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestResultWriterRecordsRecoversAndRejectsDrift(t *testing.T) {
	parent := testParent()
	runs := resultWriterRuns(t, parent)
	store := newFakeCurvePlanStore()
	writer := newResultWriter(t, store, 1<<20)
	curvePlan := resultWriterCurvePlanReference()
	detail := json.RawMessage(`{"selectedBatch":2,"method":{"z":1,"a":[3,2,1]}}`)
	result := batchenergyoptimization.Result{
		Outcome: "MinimumFound/LocalMinimum",
		Detail:  detail,
	}

	first, err := writer.Write(context.Background(), parent, curvePlan, runs, result)
	if err != nil {
		t.Fatalf("record result: %v", err)
	}
	assertCreatedResultReference(t, first, store)

	storedPayload := store.objects[store.lastIdentity.Location()]
	stored := assertStoredResult(t, parent, storedPayload, detail, curvePlan)

	// Equivalent stored bytes deliberately have a different size. Recovery must
	// return their observed reference, not the recomputed payload's reference.
	// Reordering the Runs also models an unordered Kubernetes List response.
	storedPayload = append([]byte(" \n"), storedPayload...)
	store.objects[store.lastIdentity.Location()] = storedPayload
	reorderedRuns := []edgev1alpha1.ProfilingRun{runs[2], runs[1], runs[0]}
	second, err := writer.Write(context.Background(), parent, curvePlan, reorderedRuns, result)
	if err != nil {
		t.Fatalf("recover existing result: %v", err)
	}
	if second.Size != int64(len(storedPayload)) || second.Size == first.Size ||
		second.SchemaVersion != first.SchemaVersion || store.puts != 2 || store.opens != 1 {
		t.Fatalf("stored recovery = reference %+v, puts %d, opens %d", second, store.puts, store.opens)
	}

	driftedSpec := stored.Spec()
	driftedSpec.Outcome = "MinimumFound/SearchSpaceExhausted"
	drifted, err := batchenergyoptimization.NewResultRecord(driftedSpec)
	if err != nil {
		t.Fatal(err)
	}
	driftedPayload, err := drifted.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store.objects[store.lastIdentity.Location()] = driftedPayload
	if _, err := writer.Write(context.Background(), parent, curvePlan, reorderedRuns, result); err == nil ||
		!strings.Contains(err.Error(), "stored batch-energy-optimization result disagrees") {
		t.Fatalf("recomputed result drift error = %v", err)
	}
}

func TestResultWriterRejectsPendingRun(t *testing.T) {
	parent := testParent()
	run := resultWriterRun(t, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 1, "pending-uid")
	store := newFakeCurvePlanStore()
	writer := newResultWriter(t, store, 1<<20)

	_, err := writer.Write(
		context.Background(), parent, resultWriterCurvePlanReference(),
		[]edgev1alpha1.ProfilingRun{*run}, resultWriterResult(),
	)
	if err == nil || !strings.Contains(err.Error(), `ProfilingRun "`+run.Name+`" is still pending`) {
		t.Fatalf("pending Run error = %v", err)
	}
	if store.puts != 0 {
		t.Fatalf("pending Run reached immutable storage: puts = %d", store.puts)
	}
}

func TestResultWriterRecordsTerminalConditionReason(t *testing.T) {
	parent := testParent()
	run := resultWriterRun(t, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 4,
	}, 2, "terminal-condition-uid")
	run.Status.Conditions = []metav1.Condition{{
		Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed",
	}}
	store := newFakeCurvePlanStore()
	writer := newResultWriter(t, store, 1<<20)

	if _, err := writer.Write(
		context.Background(), parent, resultWriterCurvePlanReference(),
		[]edgev1alpha1.ProfilingRun{*run}, resultWriterResult(),
	); err != nil {
		t.Fatalf("record terminal-condition exclusion: %v", err)
	}
	stored, err := batchenergyoptimization.DecodeResultRecord(store.objects[store.lastIdentity.Location()])
	if err != nil {
		t.Fatal(err)
	}
	excluded := stored.Spec().ExcludedTrials
	if len(excluded) != 1 || excluded[0].Attempt != 2 ||
		!reflect.DeepEqual(excluded[0].Reasons, []string{"JobFailed"}) {
		t.Fatalf("terminal-condition exclusion = %+v", excluded)
	}
}

func TestResultWriterRejectsInvalidReceipts(t *testing.T) {
	tests := map[string]func(evidencestore.Receipt) evidencestore.Receipt{
		"invalid receipt": func(receipt evidencestore.Receipt) evidencestore.Receipt {
			receipt.SchemaVersion = "unsupported"
			return receipt
		},
		"different object": func(receipt evidencestore.Receipt) evidencestore.Receipt {
			receipt.Object.Key += ".different"
			return receipt
		},
	}
	for name, mutateReceipt := range tests {
		t.Run(name, func(t *testing.T) {
			parent := testParent()
			store := newFakeCurvePlanStore()
			store.receiptMutator = mutateReceipt
			writer := newResultWriter(t, store, 1<<20)

			_, err := writer.Write(
				context.Background(), parent, resultWriterCurvePlanReference(),
				resultWriterRuns(t, parent), resultWriterResult(),
			)
			if err == nil || !strings.Contains(err.Error(), "result writer returned an invalid receipt") {
				t.Fatalf("invalid receipt error = %v", err)
			}
		})
	}
}

func TestResultWriterBoundsRecoveryRead(t *testing.T) {
	parent := testParent()
	runs := resultWriterRuns(t, parent)
	store := newFakeCurvePlanStore()
	writer := newResultWriter(t, store, 1<<20)
	if _, err := writer.Write(
		context.Background(), parent, resultWriterCurvePlanReference(), runs, resultWriterResult(),
	); err != nil {
		t.Fatalf("record result: %v", err)
	}
	payload := store.objects[store.lastIdentity.Location()]
	boundedWriter := newResultWriter(t, store, int64(len(payload)-1))
	_, err := boundedWriter.Write(
		context.Background(), parent, resultWriterCurvePlanReference(), runs, resultWriterResult(),
	)
	if err == nil || !strings.Contains(err.Error(), "batch-energy-optimization result object exceeds") {
		t.Fatalf("oversized recovery error = %v", err)
	}
}

func assertCreatedResultReference(
	t *testing.T,
	reference batchenergyoptimization.StoredObjectReference,
	store *fakeCurvePlanStore,
) {
	t.Helper()
	wantKey := "batch-energy-optimizations/parent-uid/result.json"
	storedPayload := store.objects[store.lastIdentity.Location()]
	if reference.Store != "evidence" || reference.Key != wantKey ||
		reference.SchemaVersion != "chill.dacs.io/batch-energy-optimization-result.v1alpha4" ||
		reference.Size != int64(len(storedPayload)) || reference.Size != store.lastIdentity.Size {
		t.Fatalf("created reference = %+v, identity = %+v, payload bytes = %d", reference, store.lastIdentity, len(storedPayload))
	}
	if store.puts != 1 || store.opens != 0 {
		t.Fatalf("first write calls = puts %d, opens %d", store.puts, store.opens)
	}
}

func assertStoredResult(
	t *testing.T,
	parent *edgev1alpha1.BatchEnergyOptimization,
	payload []byte,
	detail json.RawMessage,
	curvePlan batchenergyoptimization.StoredObjectReference,
) batchenergyoptimization.ResultRecord {
	t.Helper()
	stored, err := batchenergyoptimization.DecodeResultRecord(payload)
	if err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	spec := stored.Spec()
	wantState := batchenergyoptimization.ResultMeasurementState{
		TargetNode: parent.Spec.State.TargetNode, ModelSpec: parent.Spec.State.Candidate.ModelSpec,
		DeviceClass:   parent.Spec.State.Candidate.DeviceClass,
		ExecutionPath: parent.Spec.State.Candidate.ExecutionPath,
		PowerMode:     parent.Spec.State.Candidate.PowerMode,
	}
	if spec.MeasurementState != wantState {
		t.Fatalf("measurement state = %+v, want %+v", spec.MeasurementState, wantState)
	}
	if spec.CurvePlan != curvePlan {
		t.Fatalf("curve-plan reference = %+v, want %+v", spec.CurvePlan, curvePlan)
	}
	if len(spec.CompletedTrials) != 2 || len(spec.ExcludedTrials) != 1 {
		t.Fatalf("classified trials = completed %+v, excluded %+v", spec.CompletedTrials, spec.ExcludedTrials)
	}
	failed := spec.ExcludedTrials[0]
	completedRetry := spec.CompletedTrials[0]
	active := batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2}
	if failed.Run.Namespace != "chill-system" || failed.Run.Name != RunName(parent.Name, active, 2) ||
		failed.Run.UID != "failed-uid" || failed.Attempt != 2 ||
		failed.Measurement != (batchenergyoptimization.ResultMeasurement{
			WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
		}) || len(failed.Reasons) != 1 || failed.Reasons[0] != "JobFailed" {
		t.Fatalf("failed attempt projection = %+v", failed)
	}
	if completedRetry.Run.Namespace != "chill-system" || completedRetry.Run.Name != RunName(parent.Name, active, 3) ||
		completedRetry.Run.UID != "completed-uid" ||
		completedRetry.Measurement != failed.Measurement || completedRetry.Evidence.Key !=
		"profiling-runs/completed-uid/execution.json" {
		t.Fatalf("completed retry projection = %+v", completedRetry)
	}
	var gotDetail, wantDetail any
	if err := json.Unmarshal(spec.Detail, &gotDetail); err != nil {
		t.Fatalf("decode stored detail: %v", err)
	}
	if err := json.Unmarshal(detail, &wantDetail); err != nil {
		t.Fatalf("decode expected detail: %v", err)
	}
	if !reflect.DeepEqual(gotDetail, wantDetail) {
		t.Fatalf("stored detail value = %s, want %s", spec.Detail, detail)
	}
	return stored
}

func resultWriterRuns(
	t *testing.T,
	parent *edgev1alpha1.BatchEnergyOptimization,
) []edgev1alpha1.ProfilingRun {
	t.Helper()
	failed := resultWriterRun(t, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 2, "failed-uid")
	failed.Status.Conditions = []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed"}}

	completed := resultWriterRun(t, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 3, "completed-uid")

	completed.Status.Evidence = resultWriterEvidence("completed-uid", 1002)

	idle := resultWriterRun(t, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1,
	}, 1, "idle-uid")

	idle.Status.Evidence = resultWriterEvidence("idle-uid", 1003)
	return []edgev1alpha1.ProfilingRun{*completed, *failed, *idle}
}

func resultWriterRun(
	t *testing.T,
	parent *edgev1alpha1.BatchEnergyOptimization,
	measurement batchenergyoptimization.Measurement,
	attempt int,
	uid types.UID,
) *edgev1alpha1.ProfilingRun {
	t.Helper()
	run, err := BuildRun(parent, measurement, attempt)
	if err != nil {
		t.Fatal(err)
	}
	run.UID = uid
	return run
}

func resultWriterEvidence(uid string, size int64) *edgev1alpha1.ProfilingRunEvidenceReference {
	return &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: "evidence", Key: "profiling-runs/" + uid + "/execution.json",
		SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha2", Size: size,
	}
}

func newResultWriter(t *testing.T, store ResultStore, maximumBytes int64) *ResultWriter {
	t.Helper()
	writer, err := NewResultWriter(store, ResultWriterOptions{
		Store: "evidence", MaxObjectBytes: maximumBytes,
	})
	if err != nil {
		t.Fatalf("construct result writer: %v", err)
	}
	return writer
}

func resultWriterCurvePlanReference() batchenergyoptimization.StoredObjectReference {
	return batchenergyoptimization.StoredObjectReference{
		Store: "evidence", Key: "batch-energy-optimizations/parent-uid/curve-plan.json",
		SchemaVersion: "chill.dacs.io/curve-plan.v1alpha6", Size: 4096,
	}
}

func resultWriterResult() batchenergyoptimization.Result {
	return batchenergyoptimization.Result{
		Outcome: "MinimumFound/LocalMinimum",
		Detail:  json.RawMessage(`{"selectedBatch":2}`),
	}
}
