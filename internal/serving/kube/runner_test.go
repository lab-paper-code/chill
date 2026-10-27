package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const listVerb = "list"

func transitionRoot() *edge.PowerModeTransitionCost {
	o := &edge.PowerModeTransitionCost{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "transition", UID: types.UID("transition-uid"), CreationTimestamp: meta.Now()}}
	o.Spec.TargetNode, o.Spec.ModelSpec, o.Spec.ExecutionPath = "node", "measured-model", "cpu"
	o.Spec.ServingContainer, o.Spec.ServingService = firstfloorContainerName, "runner"
	o.Spec.Modes = []edge.PowerModeTransitionCostModeSpec{{PowerMode: "fixed"}, {PowerMode: "eco"}}
	return o
}

// runnerHarness offers two prepared profiles of the Node; the newer one is the runner.
func runnerHarness(t *testing.T) (*admissionHarness, string) {
	h, _, _ := sequenceHarness(t)
	_, _, _, class := observerSourceFixture()
	h.create(&class)
	older, newer := measuredCPUPreparationFixture(), measuredCPUPreparationFixture()
	for i, spec := range []*PrepareSpec{&older, &newer} {
		profile := *spec.Instance.Profile
		profile.ID = []string{"older", "newer"}[i]
		profile.MeasuredAt = time.Now().Add(time.Duration(i) * time.Hour)
		profile.Condition.LogicalModel, profile.Condition.Artifact.ExecutionPath = "measured-model", "cpu"
		spec.Instance.Profile = &profile
	}
	h.controller.observer.SetProfiles([]serving.Profile{*older.Instance.Profile, *newer.Instance.Profile})
	h.controller.mu.Lock()
	h.controller.config.Preparations = map[string]PrepareSpec{"older": older, "newer": newer}
	h.controller.mu.Unlock()
	return h, "newer"
}

func TestRunnerRefusalsComeBeforeTheTaint(t *testing.T) {
	for reason, mutate := range map[string]func(*edge.PowerModeTransitionCost){
		refusedRunnerContainerUnsupported: func(o *edge.PowerModeTransitionCost) { o.Spec.ServingContainer = "runtime" },
		refusedRunnerNamespace:            func(o *edge.PowerModeTransitionCost) { o.Namespace = "elsewhere" },
		refusedRunnerProfileMissing:       func(o *edge.PowerModeTransitionCost) { o.Spec.ModelSpec = "unmeasured" },
	} {
		h, _ := runnerHarness(t)
		h.controller.config.MeasurementNamespaces = []string{"chill-system", "elsewhere"}
		root := transitionRoot()
		mutate(root)
		h.create(root)
		h.admit()
		if got := h.admission(root); got != "Refused: "+reason || h.holder() != "" {
			t.Fatalf("%s: admission %q holder %q", reason, got, h.holder())
		}
	}
}

func TestRunnerRefusesReadBackOfSecondModeBeforeTaint(t *testing.T) {
	h, _ := runnerHarness(t)
	_, _, _, class := observerSourceFixture()
	if err := h.controller.store.client.Get(context.Background(), types.NamespacedName{Name: class.Name}, &class); err != nil {
		t.Fatal(err)
	}
	root := transitionRoot()
	class.Spec.PowerModes = []edge.PowerMode{
		{Name: root.Spec.Modes[0].PowerMode, Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 2}}},
		{Name: "eco", Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 0}}},
	}
	if err := h.controller.store.client.Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	h.setNode(func(n *core.Node) {
		n.Labels[chillmeta.PowerMode] = root.Spec.Modes[0].PowerMode
		n.Annotations[chillmeta.PowerActuator] = nvpModel0
	})
	h.create(root)
	h.kube.ClearActions()
	h.admit()
	if got := h.admission(root); got != "Refused: "+refusedRunnerSourceStateMismatch || h.holder() != "" {
		t.Fatalf("admission %q holder %q", got, h.holder())
	}
	for _, action := range h.kube.Actions() {
		resource := action.GetResource().Resource
		if (resource == "nodes" || resource == "pods") && action.GetVerb() != getVerb && action.GetVerb() != listVerb {
			t.Fatalf("refused runner wrote %s: %s", resource, action.GetVerb())
		}
	}
}

func TestRunnerPreparesNewestProfileAndHandsBackOnFailure(t *testing.T) {
	h, newest := runnerHarness(t)
	h.controller.mu.Lock()
	config := h.controller.config
	h.controller.mu.Unlock()
	_, _, _, class := observerSourceFixture()
	spec, reason := h.controller.runnerPreparation(transitionRoot(), h.node, []edge.DeviceClass{class}, config)
	if reason != "" || spec.Instance.Profile.ID != newest || !strings.HasPrefix(spec.Name, "runner-") {
		t.Fatalf("runner %+v %q", spec.Instance.Profile, reason)
	}
	root := transitionRoot()
	h.create(root)
	h.admit() // the harness stops a preparation after its Pod is created
	got := h.admission(root)
	if !strings.HasPrefix(got, "Refused: "+refusedRunnerPrepareFailed) || h.holder() != "" {
		t.Fatalf("admission %q holder %q", got, h.holder())
	}
	if pods, _ := h.kube.CoreV1().Pods("chill-system").List(context.Background(), meta.ListOptions{}); len(pods.Items) != 0 {
		t.Fatalf("runner Pod left after hand-back: %d", len(pods.Items))
	}
}
