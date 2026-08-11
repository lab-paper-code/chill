package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"k8s.io/apimachinery/pkg/types"
)

func TestResultWriterRecordsRecoversAndRejectsDrift(t *testing.T) {
	parent := testParent()
	runs := resultWriterRuns(t, parent)
	store := newFakeCurvePlanStore()
	writer, err := NewResultWriter(store, ResultWriterOptions{
		Store: "evidence", MaxObjectBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("construct result writer: %v", err)
	}
	curvePlan := batchsaturation.StoredObjectReference{
		Store: "evidence", Key: "batch-saturations/parent-uid/curve-plan.json",
		SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 4096,
	}
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
	stored := assertStoredResult(t, storedPayload, detail)

	// Equivalent stored bytes deliberately have a different size. Recovery must
	// return their observed reference, not the recomputed payload's reference.
	storedPayload = append([]byte(" \n"), storedPayload...)
	store.objects[store.lastIdentity.Location()] = storedPayload
	second, err := writer.Write(context.Background(), parent, curvePlan, runs, result)
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
	if _, err := writer.Write(context.Background(), parent, curvePlan, runs, result); err == nil ||
		!strings.Contains(err.Error(), "stored batch-saturation result disagrees") {
		t.Fatalf("recomputed result drift error = %v", err)
	}
}

func assertCreatedResultReference(
	t *testing.T,
	reference batchsaturation.StoredObjectReference,
	store *fakeCurvePlanStore,
) {
	t.Helper()
	wantKey := "batch-saturations/parent-uid/result.json"
	if reference.Store != "evidence" || reference.Key != wantKey ||
		reference.SchemaVersion != "chill.dacs.io/batch-saturation-result.v1alpha1" || reference.Size < 1 {
		t.Fatalf("created reference = %+v", reference)
	}
	if store.puts != 1 || store.opens != 0 {
		t.Fatalf("first write calls = puts %d, opens %d", store.puts, store.opens)
	}
}

func assertStoredResult(
	t *testing.T,
	payload []byte,
	detail json.RawMessage,
) batchsaturation.ResultRecord {
	t.Helper()
	stored, err := batchsaturation.DecodeResultRecord(payload)
	if err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	spec := stored.Spec()
	if len(spec.AdmittedTrials) != 2 || len(spec.ExcludedTrials) != 1 {
		t.Fatalf("classified trials = admitted %+v, excluded %+v", spec.AdmittedTrials, spec.ExcludedTrials)
	}
	failed := spec.ExcludedTrials[0]
	acceptedRetry := spec.AdmittedTrials[0]
	if failed.Run.Name != "active-b2-a1" || failed.Run.UID != "failed-uid" || failed.Attempt != 1 ||
		failed.Measurement != (batchsaturation.ResultMeasurement{
			WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
		}) || len(failed.Reasons) != 1 || failed.Reasons[0] != "QualityGateFailed" {
		t.Fatalf("failed attempt projection = %+v", failed)
	}
	if acceptedRetry.Run.Name != "active-b2-a2" || acceptedRetry.Run.UID != "accepted-uid" ||
		acceptedRetry.Measurement != failed.Measurement || acceptedRetry.Evidence.Key !=
		"profiling-runs/accepted-uid/execution.json" {
		t.Fatalf("accepted retry projection = %+v", acceptedRetry)
	}
	if !bytes.Equal(spec.Detail, detail) {
		t.Fatalf("stored detail = %s, want byte-identical %s", spec.Detail, detail)
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
	}, 1, "active-b2-a1", "failed-uid")
	failed.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Rejected", PolicyVersion: "wall-power-v1", ReasonCodes: []string{"QualityGateFailed"},
	}
	failed.Status.Evidence = resultWriterEvidence("failed-uid", 1001)

	accepted := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 2, "active-b2-a2", "accepted-uid")
	accepted.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Accepted", PolicyVersion: "wall-power-v1",
	}
	accepted.Status.Evidence = resultWriterEvidence("accepted-uid", 1002)

	idle := resultWriterRun(t, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeIdle, Batch: 1,
	}, 1, "idle-b1-a1", "idle-uid")
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
	name string,
	uid types.UID,
) *edgev1alpha1.ProfilingRun {
	t.Helper()
	run, err := BuildRun(parent, measurement, attempt)
	if err != nil {
		t.Fatal(err)
	}
	run.Name = name
	run.UID = uid
	return run
}

func resultWriterEvidence(uid string, size int64) *edgev1alpha1.ProfilingRunEvidenceReference {
	return &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: "evidence", Key: "profiling-runs/" + uid + "/execution.json",
		SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha2", Size: size,
	}
}
