package kube

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	resetHighMode   = "high"
	resetFixedMode  = "fixed"
	resetLowerStep  = "lower-power"
	resetLowestNoOp = "already lowest without policy"
)

func newMixedResetPowerHarness(t *testing.T) (*resetHarness, *edge.DeviceClass) {
	t.Helper()
	h := newResetHarness(t)
	class := h.powerReturnCandidate(t)
	class.Spec.PowerModes = append(class.Spec.PowerModes, edge.PowerMode{Name: resetHighMode,
		Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 2}}})
	ctx := context.Background()
	if err := h.api.Update(ctx, class); err != nil {
		t.Fatal(err)
	}
	profiles := h.controller.observer.Profiles()
	profiles[0].Condition.PowerMode = resetHighMode
	h.controller.observer.SetProfiles(profiles)
	h.controller.mu.Lock()
	preparation := h.controller.config.Preparations[profiles[0].ID]
	preparation.Instance.Profile = &profiles[0]
	h.controller.config.Preparations[profiles[0].ID] = preparation
	baseline := h.controller.config.Baselines[0]
	fixed := baseline
	fixed.ID, fixed.PowerMode, fixed.Watts = "fixed-idle", resetFixedMode, 1
	fixed.Reference.Key = fixed.ID
	high := baseline
	high.ID, high.PowerMode, high.Watts = "high-idle", resetHighMode, 10
	high.Reference.Key = high.ID
	h.controller.config.Baselines = []serving.NodeBaseline{fixed, baseline, high}
	h.controller.mu.Unlock()
	node, err := h.kube.CoreV1().Nodes().Get(ctx, testNode.Name, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels[chillmeta.PowerMode] = resetHighMode
	settleAgent(class)(node)
	if _, err := h.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	return h, class
}

// The public reset runs the real removal and power operations against API-backed
// Nodes. The fixture agent supplies host readback; it does not replace Execute.
func TestResetMixedPowerModesRetriesEmptyNode(t *testing.T) {
	h, class := newMixedResetPowerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, beforeDocument := h.configuration(t)
	foreignBefore, err := h.kube.CoreV1().Pods(h.foreign.Namespace).Get(ctx, h.foreign.Name, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	failPower := true
	var requested []string
	h.api.Client.(*nodeAPI).agent = func(node *core.Node) {
		requested = append(requested, node.Labels[chillmeta.PowerMode])
		if failPower {
			generation, _, _ := chillmeta.NodePowerModeGeneration(node)
			chillmeta.SetPowerModeTransition(node, chillmeta.PowerModeFailed, generation, chillmeta.PowerModeTransitionRecord{
				PowerMode: node.Labels[chillmeta.PowerMode], RequestedAt: node.Annotations[chillmeta.PowerModeRequestedAt],
				Generation: generation, Failure: "host retained higher mode"})
			return
		}
		settleAgent(class)(node)
	}
	for range 2 {
		result, err := h.controller.ExecuteManual(ctx, h.request())
		assertResetPowerFailure(t, ctx, h, result, err)
	}
	failPower = false
	completed, err := h.controller.ExecuteManual(ctx, h.request())
	assertResetPowerReadback(t, ctx, h, class, completed, err, requested)
	assertResetPowerUnrelated(t, ctx, h, beforeDocument, foreignBefore)
}

func assertResetPowerFailure(t *testing.T, ctx context.Context, h *resetHarness, result servingstate.OperationResult, err error) {
	t.Helper()
	if err != nil || result.Status != servingstate.NativeOperationPartial || result.Error == "" {
		t.Fatalf("power failure lost partial reset evidence: %+v err=%v", result, err)
	}
	assertResetSteps(t, result, "disable-autonomy", "unroute", "drain", "remove", "release", resetLowerStep)
	for _, step := range result.Steps[:len(result.Steps)-1] {
		if step.Status != servingstate.NativeOperationComplete || step.Error != "" {
			t.Fatalf("power failure changed completed cleanup: %+v", step)
		}
	}
	if result.Steps[len(result.Steps)-1].Status == servingstate.NativeOperationComplete ||
		result.Before.Node != testNode || result.Actual.PowerMode != resetHighMode {
		t.Fatalf("reset forgot empty Node or claimed unapplied power: %+v", result)
	}
	for _, get := range []func() error{
		func() error {
			_, err := h.kube.CoreV1().Pods(h.owned.Namespace).Get(ctx, h.owned.Name, meta.GetOptions{})
			return err
		},
		func() error { return h.api.Get(ctx, client.ObjectKeyFromObject(h.owned), &core.Pod{}) },
	} {
		if err := get(); !apierrors.IsNotFound(err) {
			t.Fatalf("owned Pod survives power failure: %v", err)
		}
	}
}

func assertResetPowerReadback(t *testing.T, ctx context.Context, h *resetHarness, class *edge.DeviceClass, completed servingstate.OperationResult, err error, requested []string) {
	t.Helper()
	assertResetComplete(t, completed, err)
	if completed.Error != "" || completed.Actual.Node != testNode || completed.Actual.PowerMode != lowMode {
		t.Fatalf("reset did not complete from actual lower-mode readback: %+v", completed)
	}
	node, err := h.kube.CoreV1().Nodes().Get(ctx, testNode.Name, meta.GetOptions{})
	if err != nil || servingstate.ReadBackMode(node, class) != lowMode || !slices.ContainsFunc(node.Status.Conditions, func(condition core.NodeCondition) bool {
		return condition.Type == core.NodeReady && condition.Status == core.ConditionTrue
	}) {
		t.Fatalf("reset lacks actual lower-mode Ready Node: %+v err=%v", node, err)
	}
	if len(requested) == 0 || slices.ContainsFunc(requested, func(mode string) bool { return mode != lowMode }) {
		t.Fatalf("reset requested an unactuatable or higher measured mode: %v", requested)
	}
}

func assertResetPowerUnrelated(t *testing.T, ctx context.Context, h *resetHarness, beforeDocument map[string]json.RawMessage, foreignBefore *core.Pod) {
	t.Helper()
	foreignAfter, err := h.kube.CoreV1().Pods(h.foreign.Namespace).Get(ctx, h.foreign.Name, meta.GetOptions{})
	if err != nil || !reflect.DeepEqual(foreignBefore, foreignAfter) {
		t.Fatalf("reset changed unrelated Pod: %+v err=%v", foreignAfter, err)
	}
	var beforeSettings, settings serving.SchedulerSettings
	var beforeRouting, routing map[string]map[string]int
	if err := json.Unmarshal(beforeDocument["settings"], &beforeSettings); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(beforeDocument["staticRouting"], &beforeRouting); err != nil {
		t.Fatal(err)
	}
	cm, document := h.configuration(t)
	if err := json.Unmarshal(document["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(document["staticRouting"], &routing); err != nil {
		t.Fatal(err)
	}
	beforeSettings.AutonomousWorkloads = slices.DeleteFunc(beforeSettings.AutonomousWorkloads, func(uid string) bool { return uid == h.selectedUID })
	_, selectedDeclared := routing[h.selectedUID]
	effective, _ := h.controller.runtime.SchedulerRouting()
	if !reflect.DeepEqual(settings, beforeSettings) || !reflect.DeepEqual(h.controller.Settings(), beforeSettings) ||
		!reflect.DeepEqual(routing[h.foreignUID], beforeRouting[h.foreignUID]) || selectedDeclared || len(effective[h.selectedUID]) != 0 {
		t.Fatalf("reset changed unrelated settings/routing or retained selected routing: settings=%+v routing=%v effective=%v", settings, routing, effective)
	}
	h.assertAlreadyClean(t, ctx, cm)
}

func TestResetMixedPowerModesEvidenceBoundary(t *testing.T) {
	for _, boundary := range []string{"no eligible measured mode", "unknown actual readback", resetLowestNoOp} {
		t.Run(boundary, func(t *testing.T) {
			h, class := newMixedResetPowerHarness(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			switch boundary {
			case "no eligible measured mode", resetLowestNoOp:
				h.controller.mu.Lock()
				h.controller.config.PowerPolicies = nil
				if boundary == resetLowestNoOp {
					h.controller.config.Baselines = slices.DeleteFunc(h.controller.config.Baselines, func(baseline serving.NodeBaseline) bool {
						return baseline.PowerMode != resetHighMode
					})
				}
				h.controller.mu.Unlock()
			case "unknown actual readback":
				node, err := h.kube.CoreV1().Nodes().Get(ctx, testNode.Name, meta.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				actuator, err := json.Marshal(edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 99}})
				if err != nil {
					t.Fatal(err)
				}
				node.Annotations[chillmeta.PowerActuator] = string(actuator)
				if _, err := h.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			h.api.Client.(*nodeAPI).agent = settleAgent(class)
			actionStart := len(h.kube.Actions())
			result, err := h.controller.ExecuteManual(ctx, h.request())
			if boundary == resetLowestNoOp {
				assertResetComplete(t, result, err)
				if result.Actual.Node != testNode || result.Actual.PowerMode != resetHighMode {
					t.Fatalf("already-lowest reset lost actual mode: %+v", result)
				}
			} else if err != nil || result.Status != servingstate.NativeOperationPartial || result.Error == "" ||
				len(result.Steps) == 0 || result.Steps[len(result.Steps)-1].Name != resetLowerStep {
				t.Fatalf("reset accepted missing power evidence: %+v err=%v", result, err)
			}
			for _, action := range h.kube.Actions()[actionStart:] {
				if action.GetResource().Resource == "nodes" && action.GetVerb() != getVerb && action.GetVerb() != listVerb {
					t.Fatalf("reset issued power effect without evidence: %s", action.GetVerb())
				}
			}
		})
	}
}
