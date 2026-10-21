package controller

import (
	"context"
	"errors"
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

type readBackHost struct {
	id  int32
	err error
}

func (h *readBackHost) Observe(context.Context, nodeactuator.Request) (nodeactuator.Observation, error) {
	if h.err != nil {
		return nodeactuator.Observation{}, h.err
	}
	return nodeactuator.Observation{Target: nodeactuator.Target{
		Kind: nodeactuator.ActuatorKindJetsonNVPModel, JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{ID: h.id},
	}}, nil
}

func (h *readBackHost) Apply(context.Context, nodeactuator.Request, nodeactuator.Target) error {
	return nil
}

// The annotation follows the host and is removed while the host cannot be
// read, including a value a previous process left behind.
func TestReadBackPublisherPatchesOnlyChangesAndClearsOnFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stale := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetson", Annotations: map[string]string{
		metadata.PowerActuator: `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":2}}`,
	}}}
	patches := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).
		WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			patches++
			return c.Patch(ctx, o, p, opts...)
		}}).Build()
	host := &readBackHost{}
	publisher := ReadBackPublisher{Client: c, Host: host, Node: "jetson", Interval: time.Second, Timeout: time.Second}
	annotation := func() (string, bool) {
		node := &corev1.Node{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: "jetson"}, node); err != nil {
			t.Fatal(err)
		}
		value, ok := node.Annotations[metadata.PowerActuator]
		return value, ok
	}
	last := unpublished
	unreadable := errors.New("nvpmodel unreadable")
	steps := []struct {
		id      int32
		err     error
		patches int
		want    string
	}{
		{err: unreadable, patches: 1},
		{id: 2, patches: 2, want: `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":2}}`},
		{id: 2, patches: 2, want: `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":2}}`},
		{id: 0, patches: 3, want: `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":0}}`},
		{err: unreadable, patches: 4},
		{err: unreadable, patches: 4},
	}
	for i, step := range steps {
		host.id, host.err = step.id, step.err
		if err := publisher.publish(context.Background(), &last); !errors.Is(err, step.err) {
			t.Fatalf("step %d: %v", i, err)
		}
		value, ok := annotation()
		if patches != step.patches || value != step.want || ok != (step.want != "") {
			t.Fatalf("step %d: patches=%d annotation=%q present=%v", i, patches, value, ok)
		}
	}
}
