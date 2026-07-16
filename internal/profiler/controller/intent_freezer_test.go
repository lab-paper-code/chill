package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIntentFreezerCreatesRecordBeforePublishingStatus(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	preparer := &countingPreparer{prepared: testPreparedRun(t)}
	freezer := mustIntentFreezer(t, kubeClient, preparer, records)

	result, err := freezer.Freeze(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RecordCreated || !result.StatusUpdated || result.RecordRecovered || preparer.calls != 1 {
		t.Fatalf("unexpected first freeze result: %#v calls=%d", result, preparer.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if stored.Status.IntentDigest != result.PreparedRun.IntentDigest ||
		stored.Status.ObservedGeneration != run.Generation {
		t.Fatalf("published status = %#v", stored.Status)
	}
	if _, err := records.Load(ctx, stored); err != nil {
		t.Fatalf("status was published without a recoverable record: %v", err)
	}
}

func TestIntentFreezerRestartDoesNotResolveFactsAgain(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	first := &countingPreparer{prepared: testPreparedRun(t)}
	if _, err := mustIntentFreezer(t, kubeClient, first, records).Freeze(ctx, run); err != nil {
		t.Fatal(err)
	}

	restartedRun := getIntentFreezerRun(t, ctx, kubeClient, run)
	restarted := &countingPreparer{err: errors.New("must not prepare")}
	result, err := mustIntentFreezer(t, kubeClient, restarted, records).Freeze(ctx, restartedRun)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.calls != 0 || result.StatusUpdated || result.RecordCreated || result.RecordRecovered {
		t.Fatalf("restart recomputed frozen facts: %#v calls=%d", result, restarted.calls)
	}
}

func TestIntentFreezerRecoversCrashBetweenRecordAndStatus(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	prepared := testPreparedRun(t)
	if _, created, err := records.Ensure(ctx, run, prepared); err != nil || !created {
		t.Fatalf("precreate record: created=%v err=%v", created, err)
	}
	preparer := &countingPreparer{err: errors.New("must not prepare")}

	result, err := mustIntentFreezer(t, kubeClient, preparer, records).Freeze(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RecordRecovered || !result.StatusUpdated || result.RecordCreated || preparer.calls != 0 {
		t.Fatalf("crash recovery result: %#v calls=%d", result, preparer.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if stored.Status.IntentDigest != prepared.IntentDigest {
		t.Fatalf("recovered digest = %q", stored.Status.IntentDigest)
	}
}

func TestIntentFreezerNeverRecreatesRecordClaimedByStatus(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	run.Generation = 3
	run.Status.IntentDigest = testPreparedRun(t).IntentDigest
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.ProfilingRun{}).WithObjects(run).Build()
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	preparer := &countingPreparer{prepared: testPreparedRun(t)}

	_, err = mustIntentFreezer(t, kubeClient, preparer, records).Freeze(ctx, stored)
	if err == nil || !apierrors.IsNotFound(err) || preparer.calls != 0 {
		t.Fatalf("missing claimed record should stop: err=%v calls=%d", err, preparer.calls)
	}
}

func TestIntentFreezerPreservesPreparationFailureAndStatus(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	sentinel := errors.New("resolver unavailable")
	preparer := &countingPreparer{err: sentinel}

	_, err := mustIntentFreezer(t, kubeClient, preparer, records).Freeze(ctx, run)
	if !errors.Is(err, sentinel) || preparer.calls != 1 {
		t.Fatalf("preparation error = %v calls=%d", err, preparer.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if stored.Status.IntentDigest != "" || stored.Status.ObservedGeneration != 0 {
		t.Fatalf("failed preparation changed status: %#v", stored.Status)
	}
	if _, err := records.Load(ctx, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("failed preparation retained a record: %v", err)
	}
}

func TestIntentFreezerStopsForDeletingRun(t *testing.T) {
	run := testRun()
	now := metav1.NewTime(time.Now())
	run.DeletionTimestamp = &now
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	preparer := &countingPreparer{err: errors.New("must not prepare")}

	result, err := mustIntentFreezer(t, kubeClient, preparer, records).
		Freeze(context.Background(), run)
	if err != nil || !result.Stopped || preparer.calls != 0 {
		t.Fatalf("deletion result: %#v err=%v calls=%d", result, err, preparer.calls)
	}
}

func TestIntentFreezerRejectsStatusAndRecordDigestMismatch(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	prepared := testPreparedRun(t)
	if _, _, err := records.Ensure(ctx, run, prepared); err != nil {
		t.Fatal(err)
	}
	base := run.DeepCopy()
	run.Status.IntentDigest = "sha256:" + strings.Repeat("0", 64)
	if err := kubeClient.Status().Patch(ctx, run, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
	preparer := &countingPreparer{prepared: prepared}

	_, err := mustIntentFreezer(t, kubeClient, preparer, records).Freeze(ctx, run)
	if err == nil || !strings.Contains(err.Error(), "does not match") || preparer.calls != 0 {
		t.Fatalf("digest mismatch = %v calls=%d", err, preparer.calls)
	}
}

func TestIntentFreezerStatusPatchPreservesConditions(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	run.Generation = 3
	run.Status.Conditions = []metav1.Condition{{
		Type: "Independent", Status: metav1.ConditionTrue, Reason: "Retained",
	}}
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.ProfilingRun{}).WithObjects(run).Build()
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mustIntentFreezer(t, kubeClient, &countingPreparer{prepared: testPreparedRun(t)}, records).
		Freeze(ctx, stored); err != nil {
		t.Fatal(err)
	}
	after := getIntentFreezerRun(t, ctx, kubeClient, stored)
	if len(after.Status.Conditions) != 1 || after.Status.Conditions[0].Reason != "Retained" {
		t.Fatalf("status patch replaced conditions: %#v", after.Status.Conditions)
	}
}

type countingPreparer struct {
	prepared profiler.PreparedRun
	err      error
	calls    int
}

func (p *countingPreparer) Prepare(
	context.Context,
	profiler.PreparationRequest,
) (profiler.PreparedRun, error) {
	p.calls++
	return p.prepared, p.err
}

func intentFreezerFixture(
	t *testing.T,
) (*edgev1alpha1.ProfilingRun, client.Client, *IntentRecords) {
	t.Helper()
	run := testRun()
	run.Generation = 3
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.ProfilingRun{}).WithObjects(run).Build()
	stored := getIntentFreezerRun(t, context.Background(), kubeClient, run)
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	return stored, kubeClient, records
}

func getIntentFreezerRun(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	run *edgev1alpha1.ProfilingRun,
) *edgev1alpha1.ProfilingRun {
	t.Helper()
	stored := &edgev1alpha1.ProfilingRun{}
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
	if err := kubeClient.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

func mustIntentFreezer(
	t *testing.T,
	kubeClient client.Client,
	preparer RunPreparer,
	records IntentRecordRepository,
) *IntentFreezer {
	t.Helper()
	freezer, err := NewIntentFreezer(kubeClient, preparer, records)
	if err != nil {
		t.Fatal(err)
	}
	return freezer
}
