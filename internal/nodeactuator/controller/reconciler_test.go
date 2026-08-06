package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
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
) (LeaseBinding, error) {
	l.observeCalls++
	state := nodeactuator.LeaseNotHeld
	retry := time.Duration(0)
	uid := ""
	if l.held {
		state, retry, uid = nodeactuator.LeaseHeld, 10*time.Second, "lease-uid"
	}
	return LeaseBinding{
		Name: "node-lease", UID: uid, HolderIdentity: "holder", State: state, RetryAfter: retry,
	}, nil
}

func (l *recordingLeaseController) AcquireOrRenew(
	context.Context,
	nodeactuator.Request,
	time.Time,
) (LeaseBinding, error) {
	l.acquireCalls++
	l.held = true
	return LeaseBinding{
		Name: "node-lease", UID: "lease-uid", HolderIdentity: "holder",
		State: nodeactuator.LeaseHeld, RetryAfter: 10 * time.Second,
	}, nil
}

func (l *recordingLeaseController) Release(context.Context, nodeactuator.Request) (bool, error) {
	l.releaseCalls++
	l.held = false
	return true, nil
}

type staticSafetyChecker struct {
	calls   int
	allowed bool
	now     time.Time
	err     error
}

func (s *staticSafetyChecker) Check(
	context.Context,
	nodeactuator.Request,
) (WorkloadSafetyResult, error) {
	s.calls++
	if s.err != nil {
		return WorkloadSafetyResult{}, s.err
	}
	return WorkloadSafetyResult{Allowed: s.allowed, ObservedAt: s.now}, nil
}

type memoryHostActuator struct {
	target       nodeactuator.Target
	applyCalls   []nodeactuator.Target
	observeCalls int
	ignoreApply  bool
	applyErr     error
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
	return nodeactuator.Observation{
		PowerMode: powerMode,
		Target:    a.target, ObservedAt: time.Date(2026, 8, 6, 12, 0, a.observeCalls, 0, time.UTC),
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
	if !a.ignoreApply {
		a.target = target
	}
	return nil
}

func TestHandoffReconcilerFencesApplyAndRestoresBeforeLeaseRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handoff := testNodeActuation()
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(handoff).
		Build()
	lease := &recordingLeaseController{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	safety := &staticSafetyChecker{allowed: true, now: now}
	host := &memoryHostActuator{target: nvpTarget(1)}
	reconciler := newTestHandoffReconciler(t, kubeClient, lease, safety, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(handoff)}

	reconcileTimes(t, ctx, reconciler, request, 5)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseApplying || len(host.applyCalls) != 0 ||
		current.Status.ApplyStartedAt == nil {
		t.Fatalf("pre-apply fence = phase:%s applyCalls:%d status:%#v", current.Status.Phase, len(host.applyCalls), current.Status)
	}
	reconcileTimes(t, ctx, reconciler, request, 3)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseSettling || len(host.applyCalls) != 1 ||
		current.Status.TargetObservation == nil || current.Status.VerifiedAt == nil {
		t.Fatalf("target verification = phase:%s applyCalls:%d status:%#v", current.Status.Phase, len(host.applyCalls), current.Status)
	}
	now = now.Add(62 * time.Second)
	reconcileTimes(t, ctx, reconciler, request, 1)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseReady {
		t.Fatalf("settled phase = %s, status=%#v", current.Status.Phase, current.Status)
	}

	current.Spec.DesiredStage = edgev1alpha1.NodeActuationDesiredStageRestore
	current.Spec.StageRequestedAt = metav1.NewTime(now)
	if err := kubeClient.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	reconcileTimes(t, ctx, reconciler, request, 4)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseRestored || len(host.applyCalls) != 2 ||
		current.Status.RestoredObservation == nil || lease.releaseCalls != 0 {
		t.Fatalf("restoration = phase:%s applyCalls:%d releases:%d status:%#v",
			current.Status.Phase, len(host.applyCalls), lease.releaseCalls, current.Status)
	}
	reconcileTimes(t, ctx, reconciler, request, 1)
	current = getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if lease.releaseCalls != 1 || current.Status.Lease != nil || host.target.JetsonNVPModel.ID != 1 {
		t.Fatalf("post-restore release = releases:%d target:%#v status:%#v", lease.releaseCalls, host.target, current.Status)
	}
}

func TestExecuteLeaseActionSchedulesLifecycleProgressAfterRenewal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handoff := testNodeActuation()
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(handoff).
		Build()
	lease := &recordingLeaseController{}
	reconciler := newTestHandoffReconciler(
		t, kubeClient, lease, &staticSafetyChecker{allowed: true},
		&memoryHostActuator{}, nil,
	)

	result, err := reconciler.executeLeaseAction(
		ctx, handoff, nodeactuator.Snapshot{},
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
	handoff := testNodeActuation()
	previous := &edgev1alpha1.NodeActuationObservation{
		PowerMode: "MAXN", Actuator: edgev1alpha1.PowerModeActuator{
			Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
			JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0},
		},
	}
	handoff.Status.Phase = edgev1alpha1.NodeActuationPhaseSettling
	handoff.Status.PreviousObservation = previous.DeepCopy()
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(handoff).
		Build()
	lease := &recordingLeaseController{}
	reconciler := newTestHandoffReconciler(
		t, kubeClient, lease, &staticSafetyChecker{allowed: true},
		&memoryHostActuator{}, nil,
	)

	if _, err := reconciler.executeLeaseAction(
		ctx, handoff, nodeactuator.Snapshot{},
		nodeactuator.Decision{Action: nodeactuator.ActionAcquireLease},
	); err != nil {
		t.Fatal(err)
	}
	current := getNodeActuation(t, ctx, kubeClient, client.ObjectKeyFromObject(handoff))
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseSettling ||
		!reflect.DeepEqual(current.Status.PreviousObservation, previous) {
		t.Fatalf("reacquired status discarded progress: %#v", current.Status)
	}
}

func TestHandoffReconcilerRetainsLeaseAndFinalizerOnRestoreReadBackMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 12, 2, 0, 0, time.UTC)
	handoff := readyNodeActuation(now)
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(handoff).
		Build()
	lease := &recordingLeaseController{held: true}
	safety := &staticSafetyChecker{allowed: true, now: now}
	host := &memoryHostActuator{target: nvpTarget(2), ignoreApply: true}
	reconciler := newTestHandoffReconciler(t, kubeClient, lease, safety, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(handoff)}

	reconcileTimes(t, ctx, reconciler, request, 4)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseFailed ||
		current.Status.TerminalFailure == nil ||
		current.Status.TerminalFailure.Stage != edgev1alpha1.NodeActuationFailureStageRestore ||
		lease.releaseCalls != 0 || !controllerutilContainsFinalizer(current, nodeActuationFinalizer) {
		t.Fatalf("restore failure = releases:%d status:%#v finalizers:%v", lease.releaseCalls, current.Status, current.Finalizers)
	}
	reconcileTimes(t, ctx, reconciler, request, 1)
	if lease.releaseCalls != 0 {
		t.Fatalf("restore failure released Lease %d times", lease.releaseCalls)
	}
}

func TestHandoffReconcilerIgnoresHandoffForAnotherNode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handoff := testNodeActuation()
	handoff.Spec.Node.Name = "jetsony"
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(handoff).Build()
	lease := &recordingLeaseController{}
	safety := &staticSafetyChecker{}
	host := &memoryHostActuator{target: nvpTarget(1)}
	now := time.Now()
	reconciler := newTestHandoffReconciler(t, kubeClient, lease, safety, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(handoff)}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if lease.acquireCalls != 0 || safety.calls != 0 || host.observeCalls != 0 || len(host.applyCalls) != 0 {
		t.Fatal("foreign Node handoff crossed a controller port")
	}
}

func TestHandoffReconcilerRejectsUnconfiguredPolicyBeforeEveryPort(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handoff := testNodeActuation()
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(handoff).
		Build()
	lease := &recordingLeaseController{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	safety := &staticSafetyChecker{allowed: true, now: now}
	host := &memoryHostActuator{target: nvpTarget(1)}
	reconciler := newTestHandoffReconciler(t, kubeClient, lease, safety, host, &now)
	reconciler.options.AllowedPolicyDigests = []string{"sha256:" + strings.Repeat("b", 64)}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(handoff)}

	reconcileTimes(t, ctx, reconciler, request, 2)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseFailed ||
		current.Status.TerminalFailure == nil ||
		current.Status.TerminalFailure.Stage != edgev1alpha1.NodeActuationFailureStagePolicy ||
		lease.observeCalls != 0 || lease.acquireCalls != 0 || safety.calls != 0 ||
		host.observeCalls != 0 || len(host.applyCalls) != 0 {
		t.Fatalf("policy rejection crossed a port: lease=%#v safety=%d host=%#v status=%#v",
			lease, safety.calls, host, current.Status)
	}
}

func TestHandoffReconcilerRestoresRetainedStateAfterPolicyDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 12, 2, 0, 0, time.UTC)
	handoff := readyNodeActuation(now)
	handoff.Spec.DesiredStage = edgev1alpha1.NodeActuationDesiredStageApply
	handoff.Spec.StageRequestedAt = metav1.NewTime(now.Add(-2 * time.Minute))
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(handoff).
		Build()
	lease := &recordingLeaseController{held: true}
	safety := &staticSafetyChecker{allowed: true, now: now}
	host := &memoryHostActuator{target: nvpTarget(2)}
	reconciler := newTestHandoffReconciler(t, kubeClient, lease, safety, host, &now)
	reconciler.options.AllowedPolicyDigests = []string{"sha256:" + strings.Repeat("b", 64)}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(handoff)}

	reconcileTimes(t, ctx, reconciler, request, 1)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseRestoring ||
		current.Status.TerminalFailure == nil ||
		current.Status.TerminalFailure.Stage != edgev1alpha1.NodeActuationFailureStagePolicy ||
		lease.observeCalls != 0 || host.observeCalls != 0 || len(host.applyCalls) != 0 {
		t.Fatalf("policy drift did not enter fenced restoration: lease=%#v host=%#v status=%#v",
			lease, host, current.Status)
	}
}

func TestHandoffReconcilerRestoresAfterPartialApplyError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handoff := testNodeActuation()
	scheme := handoffReconcilerScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.NodeActuation{}).
		WithObjects(handoff).
		Build()
	lease := &recordingLeaseController{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	safety := &staticSafetyChecker{allowed: true, now: now}
	host := &memoryHostActuator{target: nvpTarget(1), applyErr: errors.New("command interrupted")}
	reconciler := newTestHandoffReconciler(t, kubeClient, lease, safety, host, &now)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(handoff)}
	reconcileTimes(t, ctx, reconciler, request, 6)
	current := getNodeActuation(t, ctx, kubeClient, request.NamespacedName)
	if current.Status.Phase != edgev1alpha1.NodeActuationPhaseRestoring ||
		current.Status.TerminalFailure == nil ||
		current.Status.TerminalFailure.Stage != edgev1alpha1.NodeActuationFailureStageApply ||
		current.Status.RestoreStartedAt == nil {
		t.Fatalf("partial apply failure did not enter restoration: %#v", current.Status)
	}
}

func TestHandoffReconcilerOptionsRequireExplicitPolicyAuthorization(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("a", 64)
	base := HandoffReconcilerOptions{
		AssignedNode:         nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
		AllowedPolicyDigests: []string{digest},
		SafetyValidFor:       time.Minute,
	}
	for name, mutate := range map[string]func(*HandoffReconcilerOptions){
		"missing policy": func(options *HandoffReconcilerOptions) {
			options.AllowedPolicyDigests = nil
		},
		"invalid policy": func(options *HandoffReconcilerOptions) {
			options.AllowedPolicyDigests = []string{"sha256:bad"}
		},
		"duplicate policy": func(options *HandoffReconcilerOptions) {
			options.AllowedPolicyDigests = []string{digest, digest}
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

func newTestHandoffReconciler(
	t *testing.T,
	kubeClient client.Client,
	lease LeaseController,
	safety WorkloadSafetyChecker,
	host HostActuator,
	now *time.Time,
) *HandoffReconciler {
	t.Helper()
	reconciler, err := NewHandoffReconciler(
		kubeClient, lease, safety, host,
		HandoffReconcilerOptions{
			AssignedNode: nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
			AllowedPolicyDigests: []string{
				"sha256:" + strings.Repeat("a", 64),
			},
			SafetyValidFor: 5 * time.Minute,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time { return *now }
	return reconciler
}

func reconcileTimes(
	t *testing.T,
	ctx context.Context,
	reconciler *HandoffReconciler,
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
	handoff := &edgev1alpha1.NodeActuation{}
	if err := kubeClient.Get(ctx, key, handoff); err != nil {
		t.Fatal(err)
	}
	return handoff
}

func readyNodeActuation(now time.Time) *edgev1alpha1.NodeActuation {
	handoff := testNodeActuation()
	handoff.Finalizers = []string{nodeActuationFinalizer}
	handoff.Spec.DesiredStage = edgev1alpha1.NodeActuationDesiredStageRestore
	handoff.Spec.StageRequestedAt = metav1.NewTime(now)
	timestamp := metav1.NewTime(now.Add(-time.Minute))
	handoff.Status = edgev1alpha1.NodeActuationStatus{
		ObservedGeneration: handoff.Generation,
		Phase:              edgev1alpha1.NodeActuationPhaseReady,
		Lease:              &edgev1alpha1.ProfilingRunObjectReference{Name: "node-lease", UID: "lease-uid"},
		Safety: &edgev1alpha1.NodeActuationSafetyObservation{
			Verdict: edgev1alpha1.NodeActuationSafetyAllowed, ObservedAt: timestamp,
		},
		PreviousObservation: &edgev1alpha1.NodeActuationObservation{
			PowerMode: "mode-1", Actuator: projectAPITarget(nvpTarget(1)), ObservedAt: timestamp,
		},
		TargetObservation: &edgev1alpha1.NodeActuationObservation{
			PowerMode: "mode-2", Actuator: projectAPITarget(nvpTarget(2)), ObservedAt: timestamp,
		},
		ApplyStartedAt: &timestamp, VerifiedAt: &timestamp,
		SettlingStartedAt: &timestamp, ReadyAt: &timestamp,
	}
	return handoff
}

func nvpTarget(id int32) nodeactuator.Target {
	return nodeactuator.Target{
		Kind:           nodeactuator.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{ID: id},
	}
}

func handoffReconcilerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func controllerutilContainsFinalizer(handoff *edgev1alpha1.NodeActuation, finalizer string) bool {
	for _, candidate := range handoff.Finalizers {
		if candidate == finalizer {
			return true
		}
	}
	return false
}
