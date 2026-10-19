package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordingLeaseController struct {
	held         bool
	observeCalls int
	acquireCalls int
	releaseCalls int
}

func (l *recordingLeaseController) Observe(
	context.Context,
	nodeactuator.Request,
	time.Time,
) (LeaseRecord, error) {
	l.observeCalls++
	state := nodeactuator.LockNotHeld
	retry := time.Duration(0)
	uid := ""
	if l.held {
		state, retry, uid = nodeactuator.LockHeld, 10*time.Second, "lease-uid"
	}
	return LeaseRecord{
		Name: "node-lease", UID: uid, HolderIdentity: "holder", State: state, RetryAfter: retry,
	}, nil
}

func (l *recordingLeaseController) AcquireOrRenew(
	context.Context,
	nodeactuator.Request,
	time.Time,
) (LeaseRecord, error) {
	l.acquireCalls++
	l.held = true
	return LeaseRecord{
		Name: "node-lease", UID: "lease-uid", HolderIdentity: "holder",
		State: nodeactuator.LockHeld, RetryAfter: 10 * time.Second,
	}, nil
}

func (l *recordingLeaseController) Release(context.Context, nodeactuator.Request) (bool, error) {
	l.releaseCalls++
	l.held = false
	return true, nil
}

type memoryHostActuator struct {
	target       nodeactuator.Target
	applyCalls   []nodeactuator.Target
	observeCalls int
	applyErr     error
	now          func() time.Time
}

func (a *memoryHostActuator) Observe(
	_ context.Context,
	request nodeactuator.Request,
) (nodeactuator.Observation, error) {
	a.observeCalls++
	powerMode := fmt.Sprintf("mode-%d", a.target.JetsonNVPModel.ID)
	if reflect.DeepEqual(a.target, request.Target) {
		powerMode = request.PowerMode
	}
	observedAt := time.Date(2026, 8, 6, 12, 0, a.observeCalls, 0, time.UTC)
	if a.now != nil {
		observedAt = a.now()
	}
	return nodeactuator.Observation{
		PowerMode: powerMode,
		Target:    a.target, ObservedAt: observedAt,
	}, nil
}

func (a *memoryHostActuator) Apply(
	_ context.Context,
	_ nodeactuator.Request,
	target nodeactuator.Target,
) error {
	a.applyCalls = append(a.applyCalls, target)
	if a.applyErr != nil {
		return a.applyErr
	}
	a.target = target
	return nil
}

func TestTransitionReconcilerFencesApplyAndReleasesLeaseAtReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transition := testNodeActuation()
	scheme := transitionReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(transition, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "external", UID: "external-uid"},
			Spec:       corev1.PodSpec{NodeName: "jetsonx"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}).
		Build()
	lease := &recordingLeaseController{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	host := &memoryHostActuator{target: nvpTarget(1)}
	reconciler := newTestTransitionReconciler(t, kubeClient, lease, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(transition)}

	reconcileTimes(t, ctx, reconciler, request, 3)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseApplying || len(host.applyCalls) != 0 ||
		current.Status.ApplyStartedAt == nil {
		t.Fatalf("pre-apply fence = phase:%s applyCalls:%d status:%#v", current.Status.Phase, len(host.applyCalls), current.Status)
	}
	reconcileTimes(t, ctx, reconciler, request, 3)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseSettling || len(host.applyCalls) != 1 ||
		current.Status.PreviousObservation == nil || current.Status.TargetObservation == nil ||
		current.Status.VerifiedAt == nil {
		t.Fatalf("target verification = phase:%s applyCalls:%d status:%#v", current.Status.Phase, len(host.applyCalls), current.Status)
	}
	now = now.Add(62 * time.Second)
	reconcileTimes(t, ctx, reconciler, request, 1)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseReady {
		t.Fatalf("settled phase = %s, status=%#v", current.Status.Phase, current.Status)
	}

	reconcileTimes(t, ctx, reconciler, request, 1)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if lease.releaseCalls != 1 || current.Status.Lease != nil || host.target.JetsonNVPModel.ID != 2 ||
		len(host.applyCalls) != 1 {
		t.Fatalf("ready release = releases:%d target:%#v applyCalls:%d status:%#v",
			lease.releaseCalls, host.target, len(host.applyCalls), current.Status)
	}
}

func TestTransitionReconcilerSkipsApplyWhenTargetIsAlreadyActive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transition := testNodeActuation()
	scheme := transitionReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(transition).
		Build()
	lease := &recordingLeaseController{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	host := &memoryHostActuator{target: nvpTarget(2)}
	reconciler := newTestTransitionReconciler(t, kubeClient, lease, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(transition)}

	reconcileTimes(t, ctx, reconciler, request, 4)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseSettling || len(host.applyCalls) != 0 ||
		current.Status.PreviousObservation == nil || current.Status.TargetObservation == nil ||
		current.Status.VerifiedAt == nil || current.Status.SettlingStartedAt == nil ||
		current.Status.ApplyStartedAt != nil ||
		!reflect.DeepEqual(current.Status.PreviousObservation, current.Status.TargetObservation) {
		t.Fatalf("already-active target = phase:%s applyCalls:%d status:%#v",
			current.Status.Phase, len(host.applyCalls), current.Status)
	}

	now = now.Add(61 * time.Second)
	reconcileTimes(t, ctx, reconciler, request, 1)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseReady {
		t.Fatalf("settled phase = %s, status=%#v", current.Status.Phase, current.Status)
	}

	reconcileTimes(t, ctx, reconciler, request, 1)
	if lease.releaseCalls != 1 || len(host.applyCalls) != 0 {
		t.Fatalf("ready release = releases:%d applyCalls:%d", lease.releaseCalls, len(host.applyCalls))
	}
}

func TestTransitionReconcilerRecordsTransitionCostOnlyAtReady(t *testing.T) {
	t.Parallel()

	t.Run("segments sum to end-to-end duration", func(t *testing.T) {
		ctx := context.Background()
		transition := testNodeActuation()
		scheme := transitionReconcilerScheme(t)
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).
			WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
			WithObjects(transition).
			Build()
		lease := &recordingLeaseController{}
		now := time.Date(2026, 8, 6, 12, 0, 0, 125_000_000, time.UTC)
		host := &memoryHostActuator{target: nvpTarget(1), now: func() time.Time { return now }}
		reconciler := newTestTransitionReconciler(t, kubeClient, lease, host, &now)
		request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(transition)}

		reconcileTimes(t, ctx, reconciler, request, 3)
		now = now.Add(1375 * time.Millisecond)
		reconcileTimes(t, ctx, reconciler, request, 3)
		current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
		if current.Status.Phase != edgev1alpha1.NodeActuationPhaseSettling ||
			current.Status.TransitionCost != nil {
			t.Fatalf("pre-Ready cost = phase:%s cost:%#v", current.Status.Phase, current.Status.TransitionCost)
		}

		now = now.Add(60625 * time.Millisecond)
		reconcileTimes(t, ctx, reconciler, request, 1)
		current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
		cost := current.Status.TransitionCost
		if current.Status.Phase != edgev1alpha1.NodeActuationPhaseReady || cost == nil {
			t.Fatalf("Ready cost = phase:%s cost:%#v", current.Status.Phase, cost)
		}
		if cost.ApplyAndReadBackMilliseconds != 1375 || cost.SettlingMilliseconds != 60625 ||
			cost.EndToEndMilliseconds != 62000 ||
			cost.EndToEndMilliseconds != cost.ApplyAndReadBackMilliseconds+cost.SettlingMilliseconds {
			t.Fatalf("transition cost = %#v", cost)
		}
	})

	t.Run("failure records no cost", func(t *testing.T) {
		ctx := context.Background()
		transition := testNodeActuation()
		scheme := transitionReconcilerScheme(t)
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).
			WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
			WithObjects(transition).
			Build()
		lease := &recordingLeaseController{}
		now := time.Date(2026, 8, 6, 12, 0, 0, 125_000_000, time.UTC)
		host := &memoryHostActuator{target: nvpTarget(1), applyErr: errors.New("command interrupted")}
		reconciler := newTestTransitionReconciler(t, kubeClient, lease, host, &now)
		request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(transition)}

		reconcileTimes(t, ctx, reconciler, request, 3)
		now = now.Add(375 * time.Millisecond)
		reconcileTimes(t, ctx, reconciler, request, 1)
		current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
		if current.Status.Phase != edgev1alpha1.NodeActuationPhaseFailed ||
			current.Status.TransitionCost != nil {
			t.Fatalf("failed transition cost = phase:%s cost:%#v", current.Status.Phase, current.Status.TransitionCost)
		}
	})
}

func TestExecuteLeaseActionSchedulesLifecycleProgressAfterRenewal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transition := testNodeActuation()
	scheme := transitionReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(transition).
		Build()
	lease := &recordingLeaseController{}
	reconciler := newTestTransitionReconciler(
		t, kubeClient, lease,
		&memoryHostActuator{}, nil,
	)

	result, err := reconciler.executeLeaseAction(
		ctx, transition, nodeactuator.Snapshot{},
		nodeactuator.Decision{Action: nodeactuator.ActionRenewLease},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Requeue || result.RequeueAfter != 0 || lease.acquireCalls != 1 {
		t.Fatalf("renew result = %#v, calls = %d", result, lease.acquireCalls)
	}
}

func TestExecuteLeaseActionReacquiresWithoutDiscardingDurableProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transition := testNodeActuation()
	previous := &edgev1alpha1.NodeActuationObservation{
		PowerMode: "MAXN", Actuator: edgev1alpha1.PowerModeActuator{
			Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
			JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0},
		},
	}
	transition.Status.Phase = edgev1alpha1.NodeActuationPhaseSettling
	transition.Status.PreviousObservation = previous.DeepCopy()
	scheme := transitionReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(transition).
		Build()
	lease := &recordingLeaseController{}
	reconciler := newTestTransitionReconciler(
		t, kubeClient, lease,
		&memoryHostActuator{}, nil,
	)

	if _, err := reconciler.executeLeaseAction(
		ctx, transition, nodeactuator.Snapshot{},
		nodeactuator.Decision{Action: nodeactuator.ActionAcquireLease},
	); err != nil {
		t.Fatal(err)
	}
	current := getNodeActuation(t, ctx, kubeClient, client.ObjectKeyFromObject(transition))
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseSettling ||
		!reflect.DeepEqual(current.Status.PreviousObservation, previous) {
		t.Fatalf("reacquired status discarded progress: %#v", current.Status)
	}
}

func TestTransitionReconcilerIgnoresATransitionForAnotherNode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transition := testNodeActuation()
	transition.Spec.Node.Name = "jetsony"
	scheme := transitionReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(transition).Build()
	lease := &recordingLeaseController{}
	host := &memoryHostActuator{target: nvpTarget(1)}
	now := time.Now()
	reconciler := newTestTransitionReconciler(t, kubeClient, lease, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(transition)}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if lease.acquireCalls != 0 || host.observeCalls != 0 || len(host.applyCalls) != 0 {
		t.Fatal("foreign Node transition crossed a controller port")
	}
}

func TestTransitionReconcilerRejectsUnconfiguredPolicyBeforeEveryPort(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transition := testNodeActuation()
	scheme := transitionReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(transition).
		Build()
	lease := &recordingLeaseController{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	host := &memoryHostActuator{target: nvpTarget(1)}
	reconciler := newTestTransitionReconciler(t, kubeClient, lease, host, &now)
	disallowed := testAllowedPolicy()
	disallowed.PowerMode = "MAXN"
	reconciler.options.AllowedPolicies = []nodeactuator.ActuationPolicySpec{disallowed}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(transition)}

	reconcileTimes(t, ctx, reconciler, request, 1)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseFailed ||
		current.Status.TerminalFailure == nil ||
		current.Status.TerminalFailure.Stage != edgev1alpha1.NodeActuationFailureStagePolicy ||
		lease.observeCalls != 0 || lease.acquireCalls != 0 ||
		host.observeCalls != 0 || len(host.applyCalls) != 0 {
		t.Fatalf("policy rejection crossed a port: lease=%#v host=%#v status=%#v",
			lease, host, current.Status)
	}
}

func TestTransitionReconcilerTerminatesApplyFailureAndReleasesLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transition := testNodeActuation()
	scheme := transitionReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(transition).
		Build()
	lease := &recordingLeaseController{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	host := &memoryHostActuator{target: nvpTarget(1), applyErr: errors.New("command interrupted")}
	reconciler := newTestTransitionReconciler(t, kubeClient, lease, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(transition)}

	reconcileTimes(t, ctx, reconciler, request, 4)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseFailed ||
		current.Status.TerminalFailure == nil ||
		current.Status.TerminalFailure.Stage != edgev1alpha1.NodeActuationFailureStageApply ||
		lease.releaseCalls != 0 {
		t.Fatalf("apply failure = releases:%d status:%#v", lease.releaseCalls, current.Status)
	}
	reconcileTimes(t, ctx, reconciler, request, 1)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if lease.releaseCalls != 1 || current.Status.Lease != nil {
		t.Fatalf("failed release = releases:%d status:%#v", lease.releaseCalls, current.Status)
	}
}

func TestTransitionReconcilerOptionsRequireExplicitPolicyAuthorization(t *testing.T) {
	t.Parallel()
	base := TransitionReconcilerOptions{
		AssignedNode:    nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
		AllowedPolicies: []nodeactuator.ActuationPolicySpec{testAllowedPolicy()},
	}
	for name, mutate := range map[string]func(*TransitionReconcilerOptions){
		"missing policy": func(options *TransitionReconcilerOptions) {
			options.AllowedPolicies = nil
		},
		"invalid policy": func(options *TransitionReconcilerOptions) {
			invalid := testAllowedPolicy()
			invalid.PowerMode = "fixed"
			options.AllowedPolicies = []nodeactuator.ActuationPolicySpec{invalid}
		},
		"duplicate policy": func(options *TransitionReconcilerOptions) {
			options.AllowedPolicies = []nodeactuator.ActuationPolicySpec{testAllowedPolicy(), testAllowedPolicy()}
		},
	} {
		t.Run(name, func(t *testing.T) {
			options := base
			mutate(&options)
			if err := options.Validate(); err == nil {
				t.Fatalf("invalid options accepted: %#v", options)
			}
		})
	}
}

func newTestTransitionReconciler(
	t *testing.T,
	kubeClient client.Client,
	lease LeaseController,
	host HostActuator,
	now *time.Time,
) *TransitionReconciler {
	t.Helper()
	reconciler, err := NewTransitionReconciler(
		kubeClient, lease, host,
		TransitionReconcilerOptions{
			AssignedNode:    nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
			AllowedPolicies: []nodeactuator.ActuationPolicySpec{testAllowedPolicy()},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time { return *now }
	return reconciler
}

func testAllowedPolicy() nodeactuator.ActuationPolicySpec {
	return nodeactuator.ActuationPolicySpec{
		Name: "xavier-mode", DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
		ActuatorKind:            nodeactuator.ActuatorKindJetsonNVPModel,
		SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
	}
}

func reconcileTimes(
	t *testing.T,
	ctx context.Context,
	reconciler *TransitionReconciler,
	request ctrl.Request,
	count int,
) {
	t.Helper()
	for i := 0; i < count; i++ {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("reconcile %d/%d: %v", i+1, count, err)
		}
	}
}

func getNodeActuation(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	key client.ObjectKey,
) *edgev1alpha1.NodeActuation {
	t.Helper()
	transition := &edgev1alpha1.NodeActuation{}
	if err := kubeClient.Get(ctx, key, transition); err != nil {
		t.Fatal(err)
	}
	return transition
}

func nvpTarget(id int32) nodeactuator.Target {
	return nodeactuator.Target{
		Kind:           nodeactuator.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{ID: id},
	}
}

func transitionReconcilerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
