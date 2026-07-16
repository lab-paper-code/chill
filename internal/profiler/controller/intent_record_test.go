package controller

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIntentRecordsCreateReuseAndRestartLoad(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	prepared := testPreparedRun(t)
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}

	record, created, err := records.Ensure(ctx, run, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !created || record.Immutable == nil || !*record.Immutable ||
		len(record.BinaryData) != 2 || record.Annotations[intentDigestAnnotation] != prepared.IntentDigest {
		t.Fatalf("created frozen intent record is incomplete: %#v", record)
	}
	if len(record.OwnerReferences) != 1 || record.OwnerReferences[0].UID != run.UID ||
		record.OwnerReferences[0].Controller == nil || !*record.OwnerReferences[0].Controller {
		t.Fatalf("frozen intent owner = %#v", record.OwnerReferences)
	}

	if _, created, err = records.Ensure(ctx, run, prepared); err != nil || created {
		t.Fatalf("second ensure should reuse exact record: created=%v err=%v", created, err)
	}
	restarted, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Load(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IntentDigest != prepared.IntentDigest ||
		!bytes.Equal(loaded.CandidateReportBytes, prepared.CandidateReportBytes) {
		t.Fatal("restarted manager did not recover exact frozen inputs")
	}
}

func TestIntentRecordsNeverOverwriteConflictingRecord(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	prepared := testPreparedRun(t)
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := records.Ensure(ctx, run, prepared)
	if err != nil {
		t.Fatal(err)
	}

	retained := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: record.Namespace, Name: record.Name}
	if err := kubeClient.Get(ctx, key, retained); err != nil {
		t.Fatal(err)
	}
	retained.BinaryData[intentRecordCandidateKey] = []byte(`{"scope":"foreign"}`)
	if err := kubeClient.Update(ctx, retained); err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.Ensure(ctx, run, prepared); err == nil {
		t.Fatal("conflicting frozen intent record was accepted")
	}
	after := &corev1.ConfigMap{}
	if err := kubeClient.Get(ctx, key, after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after.BinaryData[intentRecordCandidateKey], retained.BinaryData[intentRecordCandidateKey]) {
		t.Fatal("conflicting frozen intent record was overwritten")
	}
}

func TestIntentRecordsRejectForeignSameNamedRecord(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	prepared := testPreparedRun(t)
	scheme := intentRecordTestScheme(t)
	foreign, err := buildIntentRecord(run, prepared, scheme)
	if err != nil {
		t.Fatal(err)
	}
	foreign.OwnerReferences[0].UID = "other-run"
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.Ensure(ctx, run, prepared); err == nil || !strings.Contains(err.Error(), "ownership mismatch") {
		t.Fatalf("expected foreign owner rejection, got %v", err)
	}
}

func TestIntentRecordsRejectPreparedRunSpecDrift(t *testing.T) {
	run := testRun()
	prepared := testPreparedRun(t)
	prepared.Intent.Measurement.Batch = 2
	prepared.IntentDigest, _ = prepared.Intent.Digest()
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.Ensure(context.Background(), run, prepared); err == nil ||
		!strings.Contains(err.Error(), "does not match immutable ProfilingRun spec") {
		t.Fatalf("expected spec drift rejection, got %v", err)
	}
}

func TestIntentRecordNameIsStableAndRunUIDScoped(t *testing.T) {
	run := testRun()
	first, err := intentRecordName(run)
	if err != nil {
		t.Fatal(err)
	}
	second, err := intentRecordName(run.DeepCopy())
	if err != nil {
		t.Fatal(err)
	}
	other := run.DeepCopy()
	other.UID = "other-run-uid"
	third, err := intentRecordName(other)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == third || len(first) > 63 {
		t.Fatalf("unexpected deterministic names: %q %q %q", first, second, third)
	}
}

func testPreparedRun(t *testing.T) profiler.PreparedRun {
	t.Helper()
	intent := testFrozenIntent(t)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile("../../../spikes/profilingrun/fixtures/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	return profiler.PreparedRun{
		Intent: intent, IntentDigest: digest, CandidateReportBytes: candidate,
	}
}

func intentRecordTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}
