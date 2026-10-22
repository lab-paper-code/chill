package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type powerModeHost struct {
	id       int32
	readErr  error
	applyErr error
	applied  []int32
	clock    func() time.Time
}

// Observe stamps the read with the shared clock, as the Jetson adapter does
// with its own wall clock while the agent waits on the command.
func (h *powerModeHost) Observe(context.Context, nodeactuator.Request) (nodeactuator.Observation, error) {
	if h.readErr != nil {
		return nodeactuator.Observation{}, h.readErr
	}
	return nodeactuator.Observation{Target: nodeactuator.Target{
		Kind: nodeactuator.ActuatorKindJetsonNVPModel, JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{ID: h.id},
	}, ObservedAt: h.clock()}, nil
}

func (h *powerModeHost) Apply(_ context.Context, _ nodeactuator.Request, target nodeactuator.Target) error {
	if h.applyErr != nil {
		return h.applyErr
	}
	h.applied = append(h.applied, target.JetsonNVPModel.ID)
	h.id = target.JetsonNVPModel.ID
	return nil
}

type powerModeFixture struct {
	t        *testing.T
	client   client.Client
	host     *powerModeHost
	now      time.Time
	classErr error
}

// tick reads the clock, which every read advances by a millisecond.
func (f *powerModeFixture) tick() time.Time {
	now := f.now
	f.now = f.now.Add(time.Millisecond)
	return now
}

func newPowerModeFixture(t *testing.T, labels map[string]string) *powerModeFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	labels[metadata.DeviceClass] = "jetson-orin-nano"
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "jetson", Labels: labels},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot-1"}},
	}
	mode := func(name string, id int32) edgev1alpha1.PowerMode {
		return edgev1alpha1.PowerMode{Name: name, Actuator: edgev1alpha1.PowerModeActuator{
			Kind: edgev1alpha1.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: id},
		}}
	}
	class := &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "jetson-orin-nano"},
		Spec: edgev1alpha1.DeviceClassSpec{PowerModes: []edgev1alpha1.PowerMode{
			mode("15W", 0), mode("7W", 1), mode("25W", 2),
		}},
	}
	f := &powerModeFixture{t: t, host: &powerModeHost{}, now: time.Unix(1_000, 0).UTC()}
	f.client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, class).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*edgev1alpha1.DeviceClass); ok && f.classErr != nil {
				return f.classErr
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	f.host.clock = f.tick
	return f
}

// reconcile runs one pass of a fresh agent process, as a restart would.
func (f *powerModeFixture) reconcile() {
	f.t.Helper()
	if err := f.reconcileErr(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *powerModeFixture) reconcileErr() error {
	policy := func(mode string) nodeactuator.ActuationPolicySpec {
		return nodeactuator.ActuationPolicySpec{
			Name: "p", DeviceClass: "jetson-orin-nano", PowerMode: mode, ActuatorKind: nodeactuator.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		}
	}
	r := &PowerModeReconciler{
		Client: f.client, Reader: f.client, Host: f.host, Node: "jetson",
		Policies: []nodeactuator.ActuationPolicySpec{policy("15W"), policy("7W")},
		Interval: 10 * time.Second, Timeout: time.Second,
		Now: f.tick,
	}
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	return err
}

func (f *powerModeFixture) node() *corev1.Node {
	f.t.Helper()
	node := &corev1.Node{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Name: "jetson"}, node); err != nil {
		f.t.Fatal(err)
	}
	return node
}

func (f *powerModeFixture) update(mutate func(*corev1.Node)) {
	f.t.Helper()
	node := f.node()
	mutate(node)
	if err := f.client.Update(context.Background(), node); err != nil {
		f.t.Fatal(err)
	}
}

func (f *powerModeFixture) reboot() {
	f.t.Helper()
	node := f.node()
	node.Status.NodeInfo.BootID = "boot-2"
	if err := f.client.Status().Update(context.Background(), node); err != nil {
		f.t.Fatal(err)
	}
}

func (f *powerModeFixture) expect(state string, generation int64) metadata.PowerModeTransitionRecord {
	f.t.Helper()
	node := f.node()
	got, ok, err := metadata.NodePowerModeGeneration(node)
	if err != nil || !ok || got != generation || node.Labels[metadata.PowerModeState] != state {
		f.t.Fatalf("state=%q generation=%d (%v, %v), want %q %d",
			node.Labels[metadata.PowerModeState], got, ok, err, state, generation)
	}
	transition, _, err := metadata.NodePowerModeTransition(node)
	if err != nil {
		f.t.Fatal(err)
	}
	// ADR 0020 order: fence, read-back, settling start, Ready.
	var last time.Time
	for _, at := range []*time.Time{transition.ApplyStartedAt, transition.VerifiedAt, transition.SettlingStartedAt, transition.ReadyAt} {
		if at == nil {
			continue
		}
		if at.Before(last) {
			f.t.Fatalf("transition times out of order: %+v", transition)
		}
		last = *at
	}
	return transition
}

func TestPowerModeAppliesFencesSettlesAndSucceeds(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "7W"})
	f.reconcile()
	fence := f.expect(metadata.PowerModePending, 0)
	if fence.PowerMode != "7W" || fence.ApplyStartedAt == nil || len(f.host.applied) != 0 {
		t.Fatalf("fence = %+v, applied %v", fence, f.host.applied)
	}
	f.reconcile()
	f.reconcile()
	verified := f.expect(metadata.PowerModePending, 1)
	if verified.Generation != 1 || verified.VerifiedAt == nil || verified.SettlingStartedAt == nil ||
		len(f.host.applied) != 1 {
		t.Fatalf("verified = %+v, applied %v", verified, f.host.applied)
	}
	if f.node().Annotations[metadata.PowerActuator] != `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":1}}` {
		t.Fatalf("read-back = %q", f.node().Annotations[metadata.PowerActuator])
	}
	f.now = f.now.Add(time.Minute)
	f.reconcile()
	if ready := f.expect(metadata.PowerModeSuccess, 1); ready.ReadyAt == nil || ready.Failure != "" {
		t.Fatalf("ready = %+v", ready)
	}
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 1)
	if len(f.host.applied) != 1 {
		t.Fatalf("applied %v", f.host.applied)
	}
}

func TestPowerModeAlreadyHeldWritesSuccessOnly(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "15W"})
	f.reconcile()
	if transition := f.expect(metadata.PowerModeSuccess, 0); transition.PowerMode != "15W" ||
		transition.ApplyStartedAt != nil || transition.ReadyAt == nil || len(f.host.applied) != 0 {
		t.Fatalf("transition = %+v, applied %v", transition, f.host.applied)
	}
}

func TestPowerModeApplyFailureIsTerminalUntilRequestedAgain(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "7W"})
	f.host.applyErr = errors.New("nvpmodel refused")
	f.reconcile()
	f.reconcile()
	if failed := f.expect(metadata.PowerModeFailed, 0); failed.Failure != "nvpmodel refused" {
		t.Fatalf("failure = %+v", failed)
	}
	f.host.applyErr = nil
	f.reconcile()
	f.expect(metadata.PowerModeFailed, 0)

	// The writer retries with a new request time and the same label, in one
	// patch the agent may see at once.
	f.update(func(node *corev1.Node) { node.Annotations[metadata.PowerModeRequestedAt] = "later" })
	f.reconcile()
	f.reconcile()
	f.reconcile()
	if retried := f.expect(metadata.PowerModePending, 1); retried.RequestedAt != "later" {
		t.Fatalf("retried = %+v", retried)
	}

	f.update(func(node *corev1.Node) { delete(node.Labels, metadata.PowerMode) })
	f.reconcile()
	if _, ok := f.node().Labels[metadata.PowerModeState]; ok {
		t.Fatal("state survived the deleted desired label")
	}
}

func TestPowerModeUnauthorizedModeFails(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "25W"})
	f.reconcile()
	if failed := f.expect(metadata.PowerModeFailed, 0); failed.Failure == "" || len(f.host.applied) != 0 {
		t.Fatalf("failure = %+v, applied %v", failed, f.host.applied)
	}
}

// A mode changed by hand and a reboot each count once; an agent restart and
// a repeated read count none; the agent never re-applies the desired mode.
func TestPowerModeGenerationCountsForeignChangesAndBoots(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "15W"})
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 0)
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 0)

	f.host.id = 1 // changed by hand
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 1)
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 1)
	if len(f.host.applied) != 0 {
		t.Fatalf("agent re-applied %v", f.host.applied)
	}

	f.reboot()
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 2)
	if f.node().Annotations[metadata.PowerModeBoot] != "boot-2" {
		t.Fatalf("boot = %q", f.node().Annotations[metadata.PowerModeBoot])
	}
}

// A boot-established mode restarts the Node during Apply. The state label is
// on the Node object, so the restarted agent finds it pending, counts the
// boot once, and finishes the transition.
func TestPowerModeRebootModeStaysPendingAcrossReboot(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "7W"})
	f.reconcile()
	f.expect(metadata.PowerModePending, 0)
	// Apply recorded the mode and restarted the Node before returning.
	f.host.id = 1
	f.reboot()
	f.reconcile()
	verified := f.expect(metadata.PowerModePending, 1)
	if verified.VerifiedAt == nil || len(f.host.applied) != 0 {
		t.Fatalf("verified = %+v, applied %v", verified, f.host.applied)
	}
	f.now = f.now.Add(time.Minute)
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 1)
}

// A boot that does not establish the target, which the host never reads back,
// fails once the policy bound since the fence passes, instead of restarting
// the Node again.
func TestPowerModeRebootWithoutTargetFails(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "7W"})
	f.reconcile()
	f.host.readErr = errors.New("mode recorded, not established")
	f.reboot()
	f.reconcile()
	f.expect(metadata.PowerModePending, 0)
	f.now = f.now.Add(15 * time.Minute)
	f.reconcile()
	if failed := f.expect(metadata.PowerModeFailed, 1); failed.Failure == "" || len(f.host.applied) != 0 {
		t.Fatalf("failure = %+v, applied %v", failed, f.host.applied)
	}
	if _, ok := f.node().Annotations[metadata.PowerActuator]; ok {
		t.Fatal("unreadable host kept a read-back")
	}
}

// A first read that fails after a reboot-mode reboot keeps the request
// pending and counts nothing; the next good read counts the boot once and
// settles the target.
func TestPowerModeUnreadRebootStaysPending(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "7W"})
	f.reconcile()
	readBack := f.node().Annotations[metadata.PowerActuator]
	f.host.id = 1
	f.host.readErr = errors.New("nvpmodel timed out")
	f.reboot()
	f.reconcile()
	f.expect(metadata.PowerModePending, 0)
	if got := f.node().Annotations[metadata.PowerActuator]; got != readBack || len(f.host.applied) != 0 {
		t.Fatalf("read-back = %q, want %q; applied %v", got, readBack, f.host.applied)
	}
	f.host.readErr = nil
	f.reconcile()
	if verified := f.expect(metadata.PowerModePending, 1); verified.VerifiedAt == nil {
		t.Fatalf("verified = %+v", verified)
	}
	f.now = f.now.Add(time.Minute)
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 1)
}

// A DeviceClass read that fails for a moment requeues; it neither fails the
// request nor disturbs a settled one.
func TestPowerModeTransientDeviceClassReadRequeues(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "15W"})
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 0)
	f.classErr = errors.New("apiserver unavailable")
	if err := f.reconcileErr(); err == nil {
		t.Fatal("transient DeviceClass read did not requeue")
	}
	f.expect(metadata.PowerModeSuccess, 0)
	f.update(func(node *corev1.Node) { node.Labels[metadata.PowerMode] = "7W" })
	if err := f.reconcileErr(); err == nil {
		t.Fatal("transient DeviceClass read did not requeue")
	}
	if state := f.node().Labels[metadata.PowerModeState]; state == metadata.PowerModeFailed {
		t.Fatal("transient DeviceClass read failed the request")
	}
}

// One failed host read keeps the read-back and counts nothing; a boot seen
// while the host cannot be read counts once.
func TestPowerModeFailedReadCountsOnlyABoot(t *testing.T) {
	f := newPowerModeFixture(t, map[string]string{metadata.PowerMode: "15W"})
	f.reconcile()
	readBack := f.node().Annotations[metadata.PowerActuator]
	f.host.readErr = errors.New("nvpmodel timed out")
	f.reconcile()
	f.host.readErr = nil
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 0)
	if got := f.node().Annotations[metadata.PowerActuator]; got != readBack {
		t.Fatalf("read-back = %q, want %q", got, readBack)
	}
	f.host.readErr = errors.New("nvpmodel timed out")
	f.reboot()
	f.reconcile()
	f.expect(metadata.PowerModeSuccess, 1)
}
