package profilingrun

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGrantedNodeRequiresTheRootsMeasurementTaint(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, holder string
		tainted      bool
		granted      bool
	}{
		{name: "own grant", holder: "root-uid", tainted: true, granted: true},
		{name: "other root", holder: "other-uid", tainted: true},
		{name: "untainted", holder: "root-uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetsono", UID: "node-uid"}}
			if test.tainted {
				node.Spec.Taints = []corev1.Taint{{
					Key: MeasurementTaintKey, Value: test.holder, Effect: corev1.TaintEffectNoSchedule,
				}}
			}
			reader := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(node).Build()
			got, err := GrantedNode(context.Background(), reader, "root-uid", "jetsono", "node-uid")
			if (err == nil) != test.granted || (got != nil) != test.granted {
				t.Fatalf("GrantedNode() = %v, %v, want granted %t", got, err, test.granted)
			}
		})
	}
}
