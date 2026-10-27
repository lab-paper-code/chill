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
	reconcile()    // A fresh same-mode request fences and applies again.
	host.mhz = 300 // Drift after the 900 MHz fence, then restore in this pass.
	reconcile()
	if host.applies != 2 {
		t.Fatalf("fresh matching-clock request applied %d times, want 2 total", host.applies)
	}
	host.now = host.now.Add(11 * time.Second)
	current = reconcile()
	if current.Labels[metadata.PowerModeState] != metadata.PowerModeSuccess {
		t.Fatalf("fresh request did not settle: %v", current.Labels)
	}
	settled, readsBack := metadata.HoldsPowerMode(current, "GPU_900", class.Spec.PowerModes[0].Actuator,
		time.Minute, host.now)
	generation, _, err := metadata.NodePowerModeGeneration(current)
	if err != nil || !settled || !readsBack || generation != 2 {
		t.Fatalf("restored mode lost cumulative read-back: settled=%v readsBack=%v generation=%d err=%v",
			settled, readsBack, generation, err)
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

func TestNvidiaUnrequestedClocksDoNotAdvanceGeneration(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{},
		Annotations: map[string]string{
			metadata.PowerModeGeneration: "87",
			metadata.PowerModeBoot:       "boot-1",
			metadata.PowerActuator:       `{"kind":"NvidiaLockedGPUClock","nvidiaLockedGPUClock":{"minMHz":300,"maxMHz":300}}`,
		},
	}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot-1"}}}
	for _, step := range []struct {
		name       string
		mode       string
		mhz        int32
		boot       string
		generation int64
	}{
		{name: "default boost", mhz: 900, boot: "boot-1", generation: 87},
		{name: "default idle", mhz: 300, boot: "boot-1", generation: 87},
		{name: "default restart", mhz: 300, boot: "boot-1", generation: 87},
		{name: "default reboot", mhz: 300, boot: "boot-2", generation: 88},
		{name: "requested lock", mode: "GPU_900", mhz: 900, boot: "boot-2", generation: 89},
		{name: "requested restart", mode: "GPU_900", mhz: 900, boot: "boot-2", generation: 89},
		{name: "requested drift", mode: "GPU_900", mhz: 300, boot: "boot-2", generation: 90},
	} {
		t.Run(step.name, func(t *testing.T) {
			node.Labels[metadata.PowerMode] = step.mode
			node.Status.NodeInfo.BootID = step.boot
			host := &gpuClockHost{t: t, mhz: step.mhz}
			observation, err := host.Observe(context.Background(), nodeactuator.Request{
				Target: nodeactuator.Target{Kind: nodeactuator.ActuatorKindNvidiaLockedGPUClock},
			})
			if err != nil {
				t.Fatal(err)
			}
			annotations := map[string]*string{}
			generation, err := publishReadBack(node, observation, nil, annotations)
			if err != nil || generation != step.generation {
				t.Fatalf("generation=%d err=%v, want %d", generation, err, step.generation)
			}
			var readBack edgev1alpha1.PowerModeActuator
			if err := metadata.NodeReadBack(node, &readBack); err != nil {
				t.Fatal(err)
			}
			if readBack.NvidiaLockedGPUClock.MinMHz != step.mhz {
				t.Fatalf("physical MHz telemetry lost: %+v", readBack)
			}
			if step.name == "default boost" && (annotations[metadata.PowerActuator] == nil ||
				*annotations[metadata.PowerActuator] != node.Annotations[metadata.PowerActuator]) {
				t.Fatal("default MHz change was not staged for publication")
			}
		})
	}
}
