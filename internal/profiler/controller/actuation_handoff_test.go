package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testActuationUID = "actuation-uid"
	testDriftValue   = "other"
)

func TestBuildActuationHandoffFreezesExactRunNodeTargetPolicyAndDeadline(t *testing.T) {
	t.Parallel()
	run, frozen := testActuatedRunAndFrozen(t)
	now := time.Date(2026, 8, 6, 12, 30, 0, 123, time.UTC)
	handoff, err := buildActuationHandoff(run, frozen, actuationHandoffTestScheme(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(handoff, run) || handoff.Spec.Run.Name != run.Name ||
		handoff.Spec.Run.UID != string(run.UID) || handoff.Spec.Node.Name != frozen.Prepared.Intent.TargetNode.Name ||
		handoff.Spec.Node.UID != frozen.Prepared.Intent.TargetNode.UID ||
		handoff.Spec.DeviceClass != frozen.Prepared.Intent.State.DeviceClass ||
		handoff.Spec.PowerMode != frozen.Prepared.Intent.State.PowerMode ||
		handoff.Spec.Actuator.Kind != edgev1alpha1.PowerModeActuatorJetsonNVPModel ||
		handoff.Spec.Actuator.JetsonNVPModel == nil ||
		handoff.Spec.Actuator.JetsonNVPModel.ID != frozen.Prepared.Intent.Actuation.Actuator.JetsonNVPModel.ID {
		t.Fatalf("handoff identity = %#v", handoff)
	}
	policy := frozen.Prepared.ActuationPolicy.Policy
	if handoff.Spec.PolicyName != frozen.Prepared.Intent.Actuation.PolicyName ||
		handoff.Spec.SettlingDurationSeconds != int32(policy.SettlingDurationSeconds) ||
		handoff.Spec.MaxActuationDurationSeconds != int32(policy.MaxActuationDurationSeconds) ||
		handoff.Spec.DesiredStage != edgev1alpha1.NodeActuationDesiredStageApply ||
		!handoff.Spec.StageRequestedAt.Time.Equal(now) ||
		!handoff.Spec.Deadline.Time.Equal(now.Add(time.Duration(policy.MaxActuationDurationSeconds)*time.Second)) {
		t.Fatalf("handoff policy and timing = %#v", handoff.Spec)
	}
	handoff.UID = testActuationUID
	if err := verifyActuationHandoff(run, handoff, frozen); err != nil {
		t.Fatalf("verify built handoff: %v", err)
	}
}

func TestVerifyActuationHandoffRejectsFrozenIdentityAndTimingDrift(t *testing.T) {
	t.Parallel()
	run, frozen := testActuatedRunAndFrozen(t)
	base, err := buildActuationHandoff(run, frozen, actuationHandoffTestScheme(t), time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	base.UID = testActuationUID
	for _, test := range []struct {
		name   string
		mutate func(*edgev1alpha1.NodeActuation)
		match  string
	}{
		{name: "node UID", mutate: func(h *edgev1alpha1.NodeActuation) { h.Spec.Node.UID = testDriftValue }, match: "frozen Run input"},
		{name: "policy", mutate: func(h *edgev1alpha1.NodeActuation) { h.Spec.PolicyName = "other-policy" }, match: "frozen Run input"},
		{name: "deadline", mutate: func(h *edgev1alpha1.NodeActuation) {
			h.Spec.Deadline = metav1.NewTime(h.Spec.Deadline.Add(time.Second))
		}, match: "deadline"},
		{name: "owner", mutate: func(h *edgev1alpha1.NodeActuation) { h.OwnerReferences[0].UID = testDriftValue }, match: "ownership"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base.DeepCopy()
			test.mutate(candidate)
			if err := verifyActuationHandoff(run, candidate, frozen); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("drift error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestActuationHandoffsCreatesOnceAndReusesExactObject(t *testing.T) {
	t.Parallel()
	run, frozen := testActuatedRunAndFrozen(t)
	scheme := actuationHandoffTestScheme(t)
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	kubeClient := &actuationUIDClient{Client: baseClient}
	repository, err := NewActuationHandoffs(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	repository.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	first, created, err := repository.Ensure(context.Background(), run, frozen)
	if err != nil || !created || first.UID == "" {
		t.Fatalf("first Ensure = %#v, created=%t, error=%v", first, created, err)
	}
	second, created, err := repository.Ensure(context.Background(), run, frozen)
	if err != nil || created || second.UID != first.UID || second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("second Ensure = %#v, created=%t, error=%v", second, created, err)
	}
}

func TestActuationHandoffsAdvancesOnceToRestoreBeforeDeadline(t *testing.T) {
	t.Parallel()
	run, frozen := testActuatedRunAndFrozen(t)
	scheme := actuationHandoffTestScheme(t)
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	kubeClient := &actuationUIDClient{Client: baseClient}
	repository, err := NewActuationHandoffs(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	repository.now = func() time.Time { return createdAt }
	handoff, _, err := repository.Ensure(context.Background(), run, frozen)
	if err != nil {
		t.Fatal(err)
	}
	restoreAt := createdAt.Add(time.Second)
	repository.now = func() time.Time { return restoreAt }
	changed, err := repository.AdvanceToRestore(context.Background(), run, frozen, handoff)
	if err != nil || !changed {
		t.Fatalf("AdvanceToRestore() changed=%t, error=%v", changed, err)
	}
	actual := &edgev1alpha1.NodeActuation{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(handoff), actual); err != nil {
		t.Fatal(err)
	}
	if actual.Spec.DesiredStage != edgev1alpha1.NodeActuationDesiredStageRestore ||
		!actual.Spec.StageRequestedAt.Time.Equal(restoreAt) || actual.Spec.Deadline != handoff.Spec.Deadline {
		t.Fatalf("restored handoff spec = %#v", actual.Spec)
	}
	changed, err = repository.AdvanceToRestore(context.Background(), run, frozen, actual)
	if err != nil || changed {
		t.Fatalf("idempotent AdvanceToRestore() changed=%t, error=%v", changed, err)
	}
}

func TestActuationHandoffsLeavesElapsedDeadlineToFailSafeRestore(t *testing.T) {
	t.Parallel()
	run, frozen := testActuatedRunAndFrozen(t)
	scheme := actuationHandoffTestScheme(t)
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	kubeClient := &actuationUIDClient{Client: baseClient}
	repository, err := NewActuationHandoffs(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	repository.now = func() time.Time { return createdAt }
	handoff, _, err := repository.Ensure(context.Background(), run, frozen)
	if err != nil {
		t.Fatal(err)
	}
	repository.now = func() time.Time { return handoff.Spec.Deadline.Time }
	changed, err := repository.AdvanceToRestore(context.Background(), run, frozen, handoff)
	if err != nil || changed || handoff.Spec.DesiredStage != edgev1alpha1.NodeActuationDesiredStageApply {
		t.Fatalf("elapsed AdvanceToRestore() changed=%t spec=%#v error=%v", changed, handoff.Spec, err)
	}
}

type actuationUIDClient struct {
	client.Client
}

func (c *actuationUIDClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if actuation, ok := object.(*edgev1alpha1.NodeActuation); ok && actuation.UID == "" {
		actuation.UID = types.UID(testActuationUID)
	}
	return c.Client.Create(ctx, object, opts...)
}

func actuationHandoffTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
