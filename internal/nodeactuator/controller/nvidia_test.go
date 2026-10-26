package controller

import (
	"context"
	"strings"
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
)

type gpuClockHost struct {
	t       *testing.T
	mhz     int32
	applies int
	now     time.Time
}

func (h *gpuClockHost) Observe(_ context.Context, r nodeactuator.Request) (nodeactuator.Observation, error) {
	if r.Target.Kind != nodeactuator.ActuatorKindNvidiaLockedGPUClock {
		h.t.Fatalf("NVIDIA Node queried through %s", r.Target.Kind)
	}
	return nodeactuator.Observation{Target: nodeactuator.Target{
		Kind:                 nodeactuator.ActuatorKindNvidiaLockedGPUClock,
		NvidiaLockedGPUClock: &nodeactuator.NvidiaLockedGPUClockTarget{MinMHz: h.mhz, MaxMHz: h.mhz},
	}, ObservedAt: h.now}, nil
}

func (h *gpuClockHost) Apply(_ context.Context, _ nodeactuator.Request, target nodeactuator.Target) error {
	h.applies++
	h.mhz = target.NvidiaLockedGPUClock.MinMHz
	return nil
}

func TestNvidiaMatchingClockStillAppliesAndSettlesNewRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	const className = "edge-server-rtx-2080-ti-64g"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "genesis2", Labels: map[string]string{
		metadata.DeviceClass: className, metadata.PowerMode: "GPU_900",
	}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot-1"}}}
	class := &edgev1alpha1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: className}, Spec: edgev1alpha1.DeviceClassSpec{
		PowerModes: []edgev1alpha1.PowerMode{{Name: "GPU_900", Actuator: edgev1alpha1.PowerModeActuator{
			Kind:                 edgev1alpha1.PowerModeActuatorNvidiaLockedGPUClock,
			NvidiaLockedGPUClock: &edgev1alpha1.NvidiaLockedGPUClockTarget{MinMHz: 900, MaxMHz: 900},
		}}},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node, class).Build()
	host := &gpuClockHost{t: t, mhz: 900, now: time.Unix(1000, 0)}
	reconcile := func() *corev1.Node {
		t.Helper()
		// Reconstruct the controller each pass, including while settling.
		r := &PowerModeReconciler{Client: c, Reader: c, Host: host, Node: "genesis2", Interval: time.Second,
			Timeout: time.Second, Now: func() time.Time { return host.now }, Policies: []nodeactuator.ActuationPolicySpec{{
				Name: "gpu-900", DeviceClass: className, PowerMode: "GPU_900", ActuatorKind: nodeactuator.ActuatorKindNvidiaLockedGPUClock,
				SettlingDurationSeconds: 10, MaxActuationDurationSeconds: 60,
			}}}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
			t.Fatal(err)
		}
		current := &corev1.Node{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: "genesis2"}, current); err != nil {
			t.Fatal(err)
		}
		return current
	}
	current := reconcile() // Even a matching instantaneous clock needs a fence.
	if current.Labels[metadata.PowerModeState] != metadata.PowerModePending || host.applies != 0 {
		t.Fatalf("matching clock bypassed lock request: %v applies=%d", current.Labels, host.applies)
	}
	reconcile()           // Apply the lock, independently read and start settling.
	current = reconcile() // Restart while settling must not reapply.
	if current.Labels[metadata.PowerModeState] != metadata.PowerModePending || host.applies != 1 {
		t.Fatalf("premature success or duplicate apply: %v applies=%d", current.Labels, host.applies)
	}
	host.now = host.now.Add(11 * time.Second)
	current = reconcile()
	if current.Labels[metadata.PowerModeState] != metadata.PowerModeSuccess || host.applies != 1 {
		t.Fatalf("state=%v applies=%d", current.Labels, host.applies)
	}
	current = reconcile() // An existing successful request remains unchanged.
	if host.applies != 1 {
		t.Fatal("successful request reapplied after restart")
	}
	current.Annotations[metadata.PowerModeRequestedAt] = host.now.Format(time.RFC3339Nano)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	reconcile() // A fresh same-mode request fences and applies again.
	reconcile()
	if host.applies != 2 {
		t.Fatalf("fresh matching-clock request applied %d times, want 2 total", host.applies)
	}
	host.now = host.now.Add(11 * time.Second)
	current = reconcile()
	if current.Labels[metadata.PowerModeState] != metadata.PowerModeSuccess {
		t.Fatalf("fresh request did not settle: %v", current.Labels)
	}
	host.mhz = 300
	const nextBoot = "gpu-boot-2"
	current.Status.NodeInfo.BootID = nextBoot
	if err := c.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	current = reconcile()
	if host.applies != 2 || current.Annotations[metadata.PowerModeBoot] != nextBoot ||
		!strings.Contains(current.Annotations[metadata.PowerActuator], "300") {
		t.Fatalf("reboot implicitly reapplied or stale readback: %v applies=%d", current.Annotations, host.applies)
	}
}
