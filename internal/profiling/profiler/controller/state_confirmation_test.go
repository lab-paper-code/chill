package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// settledNode is the config's target Node after its agent settled powerMode
// at generation and reads back the config's actuator.
func settledNode(config profilingrun.RunConfig, powerMode string, readyAt time.Time, generation int64) *corev1.Node {
	verifiedAt := readyAt.Add(-30 * time.Second)
	applyStartedAt := verifiedAt.Add(-time.Minute)
	actuator, err := json.Marshal(config.Actuation.Actuator)
	if err != nil {
		panic(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: config.TargetNode.Name, UID: types.UID(config.TargetNode.UID),
		Annotations: map[string]string{metadata.PowerActuator: string(actuator)},
	}}
	metadata.SetPowerModeTransition(node, metadata.PowerModeSuccess, generation, metadata.PowerModeTransitionRecord{
		PowerMode: powerMode, RequestedAt: applyStartedAt.Format(time.RFC3339Nano), Generation: generation,
		ApplyStartedAt: &applyStartedAt, VerifiedAt: &verifiedAt, SettlingStartedAt: &verifiedAt, ReadyAt: &readyAt,
	})
	return node
}

// Every unconfirmed Node state is a permanent reason, never a retryable error:
// the Run is admitted onto a Node its root already settled, so retrying would
// reconcile without bound and stall the campaign that owns the Run.
func TestConfirmRequiredStateReadsTheTargetNode(t *testing.T) {
	t.Parallel()
	_, resolved := testActuatedRunAndResolved(t)
	config := resolved.Prepared.RunConfig
	readyAt := time.Now().Add(-time.Minute)
	for _, test := range []struct {
		name      string
		node      func() *corev1.Node
		confirmed bool
	}{
		{name: "settled", confirmed: true, node: func() *corev1.Node {
			return settledNode(config, config.Actuation.PowerMode, readyAt, 3)
		}},
		{name: "deleted", node: func() *corev1.Node { return nil }},
		{name: "replaced", node: func() *corev1.Node {
			node := settledNode(config, config.Actuation.PowerMode, readyAt, 3)
			node.UID = "replacement-uid"
			return node
		}},
		{name: "different mode", node: func() *corev1.Node {
			return settledNode(config, "MAXN", readyAt, 3)
		}},
		{name: "pending", node: func() *corev1.Node {
			node := settledNode(config, config.Actuation.PowerMode, readyAt, 3)
			node.Labels[metadata.PowerModeState] = metadata.PowerModePending
			return node
		}},
		{name: "host changed since apply", node: func() *corev1.Node {
			node := settledNode(config, config.Actuation.PowerMode, readyAt, 3)
			node.Annotations[metadata.PowerModeGeneration] = "4"
			return node
		}},
		{name: "read-back differs", node: func() *corev1.Node {
			node := settledNode(config, config.Actuation.PowerMode, readyAt, 3)
			node.Annotations[metadata.PowerActuator] = `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":7}}`
			return node
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			builder := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t))
			if node := test.node(); node != nil {
				builder = builder.WithObjects(node)
			}
			reconciler := &RunReconciler{Client: builder.Build()}
			unconfirmed, err := reconciler.confirmRequiredState(context.Background(), resolved.Prepared)
			if err != nil {
				t.Fatalf("unconfirmed state returned a retryable error: %v", err)
			}
			if (unconfirmed == "") != test.confirmed {
				t.Fatalf("unconfirmed = %q, want confirmed %t", unconfirmed, test.confirmed)
			}
		})
	}
}
