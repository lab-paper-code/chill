package nodeagent

import (
	"context"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Stopping removes the DaemonSets the node agent replaced (the legacy actuator
// only once no transition depends on it) and the power read-back on Nodes.
func TestStopMigratesLegacyDaemonSetsAndStripsReadBack(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	const namespace = "chill-system"
	discovery, actuator := component.LegacyNodeDaemonSetNames("chill")
	daemonSet := func(name string) *appsv1.DaemonSet {
		return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetson", Labels: map[string]string{
		chillmeta.PowerModeState: chillmeta.PowerModePending,
	}, Annotations: map[string]string{
		chillmeta.PowerActuator: `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":2}}`, "manual": "kept",
	}}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(daemonSet(discovery), daemonSet(actuator), node).Build()
	reconciler := &Reconciler{Client: c, Scheme: scheme}
	options := Options{SystemName: "chill", Namespace: namespace}.Defaulted()
	present := func(name string) bool {
		err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &appsv1.DaemonSet{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}
	readBack := func() bool {
		current := &corev1.Node{}
		if err := c.Get(ctx, client.ObjectKey{Name: "jetson"}, current); err != nil {
			t.Fatal(err)
		}
		if current.Annotations["manual"] != "kept" {
			t.Fatalf("unrelated annotation lost: %v", current.Annotations)
		}
		_, ok := current.Annotations[chillmeta.PowerActuator]
		return ok
	}
	if _, err := reconciler.stop(ctx, options); err != nil {
		t.Fatal(err)
	}
	if present(discovery) || !present(actuator) || !readBack() {
		t.Fatalf("while a switch runs: discovery=%v actuator=%v readBack=%v", present(discovery), present(actuator), readBack())
	}
	settled := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "jetson"}, settled); err != nil {
		t.Fatal(err)
	}
	settled.Labels[chillmeta.PowerModeState] = chillmeta.PowerModeFailed
	if err := c.Update(ctx, settled); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.stop(ctx, options); err != nil {
		t.Fatal(err)
	}
	if present(actuator) || readBack() {
		t.Fatalf("after the switch ended: actuator=%v readBack=%v", present(actuator), readBack())
	}
}
