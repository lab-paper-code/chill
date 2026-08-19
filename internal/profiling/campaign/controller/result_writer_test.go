package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchsaturation "github.com/lab-paper-code/chill/internal/profiling/campaign"
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
	result := batchsaturation.Result{
		Outcome: "Stopped/NoFurtherImprovement",
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
	driftedSpec.Outcome = "Stopped/EnergyDivergence"
	drifted, err := batchsaturation.NewResultRecord(driftedSpec)
	if err != nil {
		t.Fatal(err)
	}
	driftedPayload, err := drifted.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store.objects[store.lastIdentity.Location()] = driftedPayload
	if _, err := writer.Write(context.Background(), parent, curvePlan, reorderedRuns, result); err == nil ||
		!strings.Contains(err.Error(), "stored batch-saturation result disagrees") {
		t.Fatalf("recomputed result drift error = %v", err)
	}
}

func TestResultWriterRejectsPendingRun(t *testing.T) {
	parent := testParent()
	run := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
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

func TestResultWriterRejectsEmptyAdmissionVerdict(t *testing.T) {
	parent := testParent()
	run := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 1, "empty-verdict-uid")
	run.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{}
	store := newFakeCurvePlanStore()
	writer := newResultWriter(t, store, 1<<20)

	_, err := writer.Write(
		context.Background(), parent, resultWriterCurvePlanReference(),
		[]edgev1alpha1.ProfilingRun{*run}, resultWriterResult(),
	)
	if err == nil || !strings.Contains(err.Error(), `ProfilingRun "`+run.Name+`" has an empty admission verdict`) {
		t.Fatalf("empty admission verdict error = %v", err)
	}
	if store.puts != 0 {
		t.Fatalf("empty admission verdict reached immutable storage: puts = %d", store.puts)
	}
}

func TestResultWriterRejectsAdmittedRunWithoutEvidence(t *testing.T) {
	parent := testParent()
	run := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 1, "missing-evidence-uid")
	run.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Accepted", PolicyVersion: "wall-power-v1",
	}
	store := newFakeCurvePlanStore()
	writer := newResultWriter(t, store, 1<<20)

	_, err := writer.Write(
		context.Background(), parent, resultWriterCurvePlanReference(),
		[]edgev1alpha1.ProfilingRun{*run}, resultWriterResult(),
	)
	if err == nil || !strings.Contains(err.Error(), `admitted ProfilingRun "`+run.Name+`" has no evidence reference`) {
		t.Fatalf("missing evidence error = %v", err)
	}
	if store.puts != 0 {
		t.Fatalf("missing admitted evidence reached immutable storage: puts = %d", store.puts)
	}
}

func TestResultWriterRecordsTerminalConditionReason(t *testing.T) {
	parent := testParent()
	run := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 4,
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
	stored, err := batchsaturation.DecodeResultRecord(store.objects[store.lastIdentity.Location()])
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
	if err == nil || !strings.Contains(err.Error(), "batch-saturation result object exceeds") {
		t.Fatalf("oversized recovery error = %v", err)
	}
}

func assertCreatedResultReference(
	t *testing.T,
	reference batchsaturation.StoredObjectReference,
	store *fakeCurvePlanStore,
) {
	t.Helper()
	wantKey := "batch-saturations/parent-uid/result.json"
	storedPayload := store.objects[store.lastIdentity.Location()]
	if reference.Store != "evidence" || reference.Key != wantKey ||
		reference.SchemaVersion != "chill.dacs.io/batch-saturation-result.v1alpha2" ||
		reference.Size != int64(len(storedPayload)) || reference.Size != store.lastIdentity.Size {
		t.Fatalf("created reference = %+v, identity = %+v, payload bytes = %d", reference, store.lastIdentity, len(storedPayload))
	}
	if store.puts != 1 || store.opens != 0 {
		t.Fatalf("first write calls = puts %d, opens %d", store.puts, store.opens)
	}
}

func assertStoredResult(
	t *testing.T,
	parent *edgev1alpha1.BatchSaturation,
	payload []byte,
	detail json.RawMessage,
	curvePlan batchsaturation.StoredObjectReference,
) batchsaturation.ResultRecord {
	t.Helper()
	stored, err := batchsaturation.DecodeResultRecord(payload)
	if err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	spec := stored.Spec()
	if spec.CurvePlan != curvePlan {
		t.Fatalf("curve-plan reference = %+v, want %+v", spec.CurvePlan, curvePlan)
	}
	if len(spec.AdmittedTrials) != 2 || len(spec.ExcludedTrials) != 1 {
		t.Fatalf("classified trials = admitted %+v, excluded %+v", spec.AdmittedTrials, spec.ExcludedTrials)
	}
	failed := spec.ExcludedTrials[0]
	acceptedRetry := spec.AdmittedTrials[0]
	active := batchsaturation.Measurement{WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2}
	if failed.Run.Namespace != "chill-system" || failed.Run.Name != RunName(parent.Name, active, 2) ||
		failed.Run.UID != "failed-uid" || failed.Attempt != 2 ||
		failed.Measurement != (batchsaturation.ResultMeasurement{
			WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
		}) || len(failed.Reasons) != 1 || failed.Reasons[0] != "QualityGateFailed" {
		t.Fatalf("failed attempt projection = %+v", failed)
	}
	if acceptedRetry.Run.Namespace != "chill-system" || acceptedRetry.Run.Name != RunName(parent.Name, active, 3) ||
		acceptedRetry.Run.UID != "accepted-uid" ||
		acceptedRetry.Measurement != failed.Measurement || acceptedRetry.Evidence.Key !=
		"profiling-runs/accepted-uid/execution.json" {
		t.Fatalf("accepted retry projection = %+v", acceptedRetry)
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
	parent *edgev1alpha1.BatchSaturation,
) []edgev1alpha1.ProfilingRun {
	t.Helper()
	failed := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 2, "failed-uid")
	failed.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Rejected", PolicyVersion: "wall-power-v1", ReasonCodes: []string{"QualityGateFailed"},
	}
	failed.Status.Evidence = resultWriterEvidence("failed-uid", 1001)

	accepted := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 3, "accepted-uid")
	accepted.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Accepted", PolicyVersion: "wall-power-v1",
	}
	accepted.Status.Evidence = resultWriterEvidence("accepted-uid", 1002)

	idle := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeIdle, Batch: 1,
	}, 1, "idle-uid")
	idle.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Accepted", PolicyVersion: "wall-power-v1",
	}
	idle.Status.Evidence = resultWriterEvidence("idle-uid", 1003)
	return []edgev1alpha1.ProfilingRun{*accepted, *failed, *idle}
}

func resultWriterRun(
	t *testing.T,
	parent *edgev1alpha1.BatchSaturation,
	measurement batchsaturation.Measurement,
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

func resultWriterCurvePlanReference() batchsaturation.StoredObjectReference {
	return batchsaturation.StoredObjectReference{
		Store: "evidence", Key: "batch-saturations/parent-uid/curve-plan.json",
		SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 4096,
	}
}

func resultWriterResult() batchsaturation.Result {
	return batchsaturation.Result{
		Outcome: "Stopped/NoFurtherImprovement",
		Detail:  json.RawMessage(`{"selectedBatch":2}`),
	}
}
