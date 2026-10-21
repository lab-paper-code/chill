package nodeagent

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

func TestSafeToStopBlocksOnlyPendingPowerModes(t *testing.T) {
	t.Parallel()
	for state, want := range map[string]bool{
		chillmeta.PowerModePending: false,
		chillmeta.PowerModeSuccess: true,
		chillmeta.PowerModeFailed:  true,
		"":                         true,
	} {
		t.Run(state, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetson", Labels: map[string]string{}}}
			if state != "" {
				node.Labels[chillmeta.PowerModeState] = state
			}
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
			safe, err := SafeToStop(context.Background(), reader)
			if err != nil || safe != want {
				t.Fatalf("SafeToStop = %v, %v; want %v", safe, err, want)
			}
		})
	}
}
