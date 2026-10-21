package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func transitionRoot(container string) *edge.PowerModeTransitionCost {
	o := &edge.PowerModeTransitionCost{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "transition", UID: types.UID("transition-uid"), CreationTimestamp: meta.Now()}}
	o.Spec.TargetNode, o.Spec.ModelSpec, o.Spec.ExecutionPath = "node", "measured-model", "cpu"
	o.Spec.ServingContainer, o.Spec.ServingService = container, "runner"
	o.Spec.Modes = []edge.PowerModeTransitionCostModeSpec{{PowerMode: "fixed"}, {PowerMode: "eco"}}
	return o
}

// runnerHarness offers two prepared profiles of the Node; the newer one is the runner.
func runnerHarness(t *testing.T) (*admissionHarness, string) {
	h, _, _ := sequenceHarness(t)
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
		root := transitionRoot(firstfloorContainerName)
		mutate(root)
		h.create(root)
		h.admit()
		if got := h.admission(root); got != "Refused: "+reason || h.holder() != "" {
			t.Fatalf("%s: admission %q holder %q", reason, got, h.holder())
		}
	}
}

func TestRunnerPreparesNewestProfileAndHandsBackOnFailure(t *testing.T) {
	h, newest := runnerHarness(t)
	h.controller.mu.Lock()
	config := h.controller.config
	h.controller.mu.Unlock()
	spec, reason := h.controller.runnerPreparation(transitionRoot(firstfloorContainerName), h.node, config)
	if reason != "" || spec.Instance.Profile.ID != newest || !strings.HasPrefix(spec.Name, "runner-") {
		t.Fatalf("runner %+v %q", spec.Instance.Profile, reason)
	}
	root := transitionRoot(firstfloorContainerName)
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
