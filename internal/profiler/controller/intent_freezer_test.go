package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	"github.com/lab-paper-code/chill/internal/profilingrun"
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
	policies := testPolicyResolver(t)
	freezer := mustIntentFreezer(t, kubeClient, preparer, policies, records)

	result, err := freezer.Freeze(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RecordCreated || !result.StatusUpdated || result.RecordRecovered || preparer.calls != 1 {
		t.Fatalf("unexpected first freeze result: %#v calls=%d", result, preparer.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if !stored.Status.IntentFrozen || stored.Status.ObservedGeneration != run.Generation {
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
	if _, err := mustIntentFreezer(t, kubeClient, first, testPolicyResolver(t), records).Freeze(ctx, run); err != nil {
		t.Fatal(err)
	}

	restartedRun := getIntentFreezerRun(t, ctx, kubeClient, run)
	restarted := &countingPreparer{err: errors.New("must not prepare")}
	policies := &countingPolicyResolver{err: errors.New("must not resolve policy")}
	executionPolicies := &countingExecutionPolicyResolver{err: errors.New("must not resolve execution policy")}
	result, err := mustIntentFreezer(t, kubeClient, restarted, policies, records, executionPolicies).
		Freeze(ctx, restartedRun)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.calls != 0 || policies.calls != 0 || executionPolicies.calls != 0 ||
		result.StatusUpdated || result.RecordCreated || result.RecordRecovered {
		t.Fatalf(
			"restart recomputed frozen facts: %#v preparer=%d admission=%d execution=%d",
			result,
			restarted.calls,
			policies.calls,
			executionPolicies.calls,
		)
	}
}

func TestIntentFreezerRecoversCrashBetweenRecordAndStatus(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	frozen := testFrozenRun(t)
	if _, created, err := records.Ensure(ctx, run, frozen); err != nil || !created {
		t.Fatalf("precreate record: created=%v err=%v", created, err)
	}
	preparer := &countingPreparer{err: errors.New("must not prepare")}

	result, err := mustIntentFreezer(t, kubeClient, preparer, testPolicyResolver(t), records).Freeze(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RecordRecovered || !result.StatusUpdated || result.RecordCreated || preparer.calls != 0 {
		t.Fatalf("crash recovery result: %#v calls=%d", result, preparer.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if !stored.Status.IntentFrozen {
		t.Fatal("recovered intent was not marked frozen")
	}
}

func TestIntentFreezerNeverRecreatesRecordClaimedByStatus(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	run.Generation = 3
	run.Status.IntentFrozen = true
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.ProfilingRun{}).WithObjects(run).Build()
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	preparer := &countingPreparer{prepared: testPreparedRun(t)}

	_, err = mustIntentFreezer(t, kubeClient, preparer, testPolicyResolver(t), records).Freeze(ctx, stored)
	if err == nil || !apierrors.IsNotFound(err) || preparer.calls != 0 {
		t.Fatalf("missing claimed record should stop: err=%v calls=%d", err, preparer.calls)
	}
}

func TestIntentFreezerPreservesPreparationFailureAndStatus(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	sentinel := errors.New("resolver unavailable")
	preparer := &countingPreparer{err: sentinel}

	_, err := mustIntentFreezer(t, kubeClient, preparer, testPolicyResolver(t), records).Freeze(ctx, run)
	if !errors.Is(err, sentinel) || preparer.calls != 1 {
		t.Fatalf("preparation error = %v calls=%d", err, preparer.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if stored.Status.IntentFrozen || stored.Status.ObservedGeneration != 0 {
		t.Fatalf("failed preparation changed status: %#v", stored.Status)
	}
	if _, err := records.Load(ctx, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("failed preparation retained a record: %v", err)
	}
}

func TestIntentFreezerPreservesPolicyResolutionFailureAndStatus(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	sentinel := errors.New("policy catalog unavailable")
	preparer := &countingPreparer{prepared: testPreparedRun(t)}
	policies := &countingPolicyResolver{err: sentinel}

	_, err := mustIntentFreezer(t, kubeClient, preparer, policies, records).Freeze(ctx, run)
	if !errors.Is(err, sentinel) || preparer.calls != 1 || policies.calls != 1 {
		t.Fatalf("policy error = %v preparer=%d policies=%d", err, preparer.calls, policies.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if stored.Status.IntentFrozen || stored.Status.ObservedGeneration != 0 {
		t.Fatalf("failed policy resolution changed status: %#v", stored.Status)
	}
	if _, err := records.Load(ctx, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("failed policy resolution retained a record: %v", err)
	}
}

func TestIntentFreezerClassifiesAdmissionWorkloadMismatchBeforeExecutionPolicyResolution(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	raw := strings.ReplaceAll(controllerPolicyCatalog, "operator-meter-policy", run.Spec.AdmissionPolicy)
	raw = strings.ReplaceAll(raw, "saturated-serial-fixed-batch", "idle-model-loaded")
	policy, err := profileradmission.FreezeConfiguredPolicy(
		[]byte(raw),
		run.Spec.AdmissionPolicy,
		profileradmission.PolicySource{
			Kind: profileradmission.ConfigMapPolicySourceKind, Namespace: testSystemNamespace,
			Name: "facts", UID: testFactsUID, ResourceVersion: "1", Key: "admission-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	execution := &countingExecutionPolicyResolver{err: errors.New("must not resolve execution policy")}
	freezer := mustIntentFreezer(
		t,
		kubeClient,
		&countingPreparer{prepared: testPreparedRun(t)},
		&countingPolicyResolver{policy: policy},
		records,
		execution,
	)

	_, err = freezer.Freeze(ctx, run)
	reason, terminal := terminalFreezeReason(err)
	if err == nil || !terminal || reason != freezeReasonAdmissionPolicyInvalid || execution.calls != 0 {
		t.Fatalf(
			"workload mismatch error = %v, reason=%q terminal=%t executionCalls=%d",
			err,
			reason,
			terminal,
			execution.calls,
		)
	}
}

func TestIntentFreezerPreservesExecutionPolicyResolutionFailureAndStatus(t *testing.T) {
	ctx := context.Background()
	run, kubeClient, records := intentFreezerFixture(t)
	sentinel := errors.New("execution-policy catalog unavailable")
	preparer := &countingPreparer{prepared: testPreparedRun(t)}
	executionPolicies := &countingExecutionPolicyResolver{err: sentinel}

	_, err := mustIntentFreezer(
		t,
		kubeClient,
		preparer,
		testPolicyResolver(t),
		records,
		executionPolicies,
	).Freeze(ctx, run)
	if !errors.Is(err, sentinel) || preparer.calls != 1 || executionPolicies.calls != 1 {
		t.Fatalf("execution-policy error = %v preparer=%d execution=%d", err, preparer.calls, executionPolicies.calls)
	}
	stored := getIntentFreezerRun(t, ctx, kubeClient, run)
	if stored.Status.IntentFrozen || stored.Status.ObservedGeneration != 0 {
		t.Fatalf("failed execution-policy resolution changed status: %#v", stored.Status)
	}
	if _, err := records.Load(ctx, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("failed execution-policy resolution retained a record: %v", err)
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

	result, err := mustIntentFreezer(t, kubeClient, preparer, testPolicyResolver(t), records).
		Freeze(context.Background(), run)
	if err != nil || !result.Stopped || preparer.calls != 0 {
		t.Fatalf("deletion result: %#v err=%v calls=%d", result, err, preparer.calls)
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

	if _, err := mustIntentFreezer(t, kubeClient, &countingPreparer{prepared: testPreparedRun(t)}, testPolicyResolver(t), records).
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

type countingPolicyResolver struct {
	policy profileradmission.FrozenPolicy
	err    error
	calls  int
}

type countingExecutionPolicyResolver struct {
	policy profiler.FrozenExecutionPolicy
	err    error
	calls  int
}

func (r *countingExecutionPolicyResolver) Resolve(
	_ context.Context,
	_ profilingrun.Intent,
) (profiler.FrozenExecutionPolicy, error) {
	r.calls++
	return r.policy, r.err
}

func (r *countingPolicyResolver) Resolve(context.Context, string) (profileradmission.FrozenPolicy, error) {
	r.calls++
	return r.policy, r.err
}

func testPolicyResolver(t *testing.T) *countingPolicyResolver {
	t.Helper()
	return &countingPolicyResolver{policy: testFrozenRun(t).AdmissionPolicy}
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
	policies AdmissionPolicyResolver,
	records IntentRecordRepository,
	executionPolicies ...ExecutionPolicyResolver,
) *IntentFreezer {
	t.Helper()
	executionPolicy := ExecutionPolicyResolver(testExecutionPolicyResolver(t))
	if len(executionPolicies) > 0 {
		executionPolicy = executionPolicies[0]
	}
	freezer, err := NewIntentFreezer(kubeClient, preparer, policies, executionPolicy, records)
	if err != nil {
		t.Fatal(err)
	}
	return freezer
}

func testExecutionPolicyResolver(t *testing.T) *countingExecutionPolicyResolver {
	t.Helper()
	return &countingExecutionPolicyResolver{policy: testFrozenRun(t).ExecutionPolicy}
}
