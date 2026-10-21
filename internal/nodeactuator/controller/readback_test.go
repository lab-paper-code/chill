package controller

import (
	"context"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type readBackHost struct{ id int32 }

func (h *readBackHost) Observe(context.Context, nodeactuator.Request) (nodeactuator.Observation, error) {
	return nodeactuator.Observation{Target: nodeactuator.Target{
		Kind: nodeactuator.ActuatorKindJetsonNVPModel, JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{ID: h.id},
	}}, nil
}

func (h *readBackHost) Apply(context.Context, nodeactuator.Request, nodeactuator.Target) error {
	return nil
}

func TestReadBackPublisherPatchesOnlyChanges(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	patches := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetson"}}).
		WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			patches++
			return c.Patch(ctx, o, p, opts...)
		}}).Build()
	host := &readBackHost{id: 2}
	publisher := ReadBackPublisher{Client: c, Host: host, Node: "jetson", Interval: time.Second, Timeout: time.Second}
	last := ""
	for _, id := range []int32{2, 2, 0} {
		host.id = id
		if err := publisher.publish(context.Background(), &last); err != nil {
			t.Fatal(err)
		}
	}
	node := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "jetson"}, node); err != nil {
		t.Fatal(err)
	}
	if patches != 2 || node.Annotations[metadata.PowerActuator] != `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":0}}` {
		t.Fatalf("patches=%d annotation=%q", patches, node.Annotations[metadata.PowerActuator])
	}
}
