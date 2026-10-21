package controller

import (
	"context"
	"encoding/json"
	"strconv"
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
	transition, err := json.Marshal(metadata.PowerModeTransitionRecord{
		PowerMode: powerMode, Generation: generation, ApplyStartedAt: &applyStartedAt,
		VerifiedAt: &verifiedAt, SettlingStartedAt: &verifiedAt, ReadyAt: &readyAt,
	})
	if err != nil {
		panic(err)
	}
	actuator, err := json.Marshal(config.Actuation.Actuator)
	if err != nil {
		panic(err)
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: config.TargetNode.Name, UID: types.UID(config.TargetNode.UID),
		Labels: map[string]string{metadata.PowerMode: powerMode, metadata.PowerModeState: metadata.PowerModeSuccess},
		Annotations: map[string]string{
			metadata.PowerModeGeneration: strconv.FormatInt(generation, 10),
			metadata.PowerModeTransition: string(transition),
			metadata.PowerActuator:       string(actuator),
		},
	}}
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
					Key: profilingrun.MeasurementTaintKey, Value: test.holder, Effect: corev1.TaintEffectNoSchedule,
				}}
			}
			reader := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(node).Build()
			got, err := GrantedNode(context.Background(), reader, "root-uid", "jetsono", "node-uid")
			if (err == nil) != test.granted || (got != nil) != test.granted {
				t.Fatalf("GrantedNode() = %v, %v, want granted %t", got, err, test.granted)
			}
		})
	}
}
