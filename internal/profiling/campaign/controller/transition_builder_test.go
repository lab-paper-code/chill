package controller

import (
	"context"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProjectTransitionMaterialKeepsTheFixedPathDirect(t *testing.T) {
	material, err := projectTransitionMaterial(profiler.PreparedRun{})
	if err != nil {
		t.Fatal(err)
	}
	if material != nil {
		t.Fatalf("fixed prepared Run projected transition material: %+v", material)
	}
}

// campaignNode is the campaign's target Node, its agent at state for powerMode
// with an apply fenced at applyStartedAt.
func campaignNode(powerMode, state string, applyStartedAt time.Time, generation int64) *corev1.Node {
	record := metadata.PowerModeTransitionRecord{
		PowerMode: powerMode, RequestedAt: applyStartedAt.Format(time.RFC3339Nano),
		Generation: generation, ApplyStartedAt: &applyStartedAt,
	}
	if state == metadata.PowerModeSuccess {
		readyAt := applyStartedAt.Add(time.Minute)
		record.VerifiedAt, record.ReadyAt = &applyStartedAt, &readyAt
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetsono", UID: "node-uid"}}
	metadata.SetPowerModeTransition(node, state, generation, record)
	return node
}

func TestProjectNodeTransitionFollowsTheAgent(t *testing.T) {
	parent := testParent()
	material, err := projectTransitionMaterial(actuatedPreparedRun(parent))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 14, 1, 2, 3, 0, time.UTC)
	mode := material.PowerMode
	for _, test := range []struct {
		name    string
		node    *corev1.Node
		outcome batchenergyoptimization.TransitionOutcome
	}{
		{name: "not requested", node: campaignNode("MODE_15W", metadata.PowerModeSuccess, now.Add(-time.Hour), 2)},
		{name: "applying", node: campaignNode(mode, metadata.PowerModePending, now.Add(-time.Minute), 2),
			outcome: batchenergyoptimization.TransitionInFlight},
		{name: "apply failed", node: campaignNode(mode, metadata.PowerModeFailed, now.Add(-time.Minute), 2),
			outcome: batchenergyoptimization.TransitionFailed},
		{name: "settled", node: campaignNode(mode, metadata.PowerModeSuccess, now.Add(-2*time.Minute), 3),
			outcome: batchenergyoptimization.TransitionEstablished},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := projectNodeTransition(test.node, material, now)
			if test.outcome == "" {
				if got != nil {
					t.Fatalf("transition = %#v, want none", got)
				}
				return
			}
			if got == nil || got.Outcome != test.outcome {
				t.Fatalf("transition = %#v, want %s", got, test.outcome)
			}
			want := batchenergyoptimization.State{
				TargetNode: parent.Spec.State.TargetNode, DeviceClass: parent.Spec.State.Candidate.DeviceClass,
				PowerMode: parent.Spec.State.Candidate.PowerMode,
			}
			if (got.EstablishedState != nil) != (test.outcome == batchenergyoptimization.TransitionEstablished) ||
				got.EstablishedState != nil && *got.EstablishedState != want {
				t.Fatalf("established state = %#v, want %#v", got.EstablishedState, want)
			}
		})
	}
}

type nodeTransitionPlan struct {
	*terminalReplayDependencies
	plan     profilederivation.CurvePlan
	material *TransitionMaterial
}

func (d nodeTransitionPlan) Resolve(
	context.Context, *edgev1alpha1.BatchEnergyOptimization,
) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error) {
	return d.plan, batchenergyoptimization.StoredObjectReference{}, d.material, nil
}

// The campaign writes its power mode on a Node its measurement taint grants it.
func TestCampaignRequestsPowerModeOnItsGrantedNode(t *testing.T) {
	parent := testParent()
	parent.Generation = 1
	parent.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}
	reportBytes, report := curvePlanCandidateReport(t, parent)
	plan, material, err := (&CurvePlanProjector{
		reader:   &fakeCurvePlanModelReader{model: curvePlanModel(report)},
		resolver: &fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer: &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"},
	}).Project(context.Background(), parent)
	if err != nil || material == nil {
		t.Fatalf("project plan: material=%v err=%v", material, err)
	}
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: material.Node.Name, UID: types.UID(material.Node.UID)},
		Spec: corev1.NodeSpec{Taints: []corev1.Taint{{
			Key: profilingrun.MeasurementTaintKey, Value: string(parent.UID), Effect: corev1.TaintEffectNoSchedule,
		}}},
	}
	native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).
		WithObjects(parent.DeepCopy(), node).Build()
	dependencies := nodeTransitionPlan{&terminalReplayDependencies{}, plan, material}
	reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies, dependencies, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: parent.Namespace, Name: parent.Name},
	})
	if err != nil || result.RequeueAfter != admissionWait {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	updated := &corev1.Node{}
	if err := native.Get(context.Background(), types.NamespacedName{Name: node.Name}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Labels[metadata.PowerMode] != material.PowerMode {
		t.Fatalf("power-mode label = %q, want %q", updated.Labels[metadata.PowerMode], material.PowerMode)
	}
}

func actuatedPreparedRun(parent *edgev1alpha1.BatchEnergyOptimization) profiler.PreparedRun {
	target := profilingrun.ActuatorTarget{
		Kind:           profilingrun.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 4},
	}
	policy := profiler.FrozenActuationPolicy{
		SchemaVersion: profiler.FrozenActuationPolicySchemaVersion,
		Source: profiler.ActuationPolicySource{
			Kind: profiler.ConfigMapActuationPolicySourceKind, Namespace: parent.Namespace,
			Name: "facts", UID: "facts-uid", ResourceVersion: "1", Key: "actuation-policies.yaml",
		},
		Policy: profiler.ActuationPolicy{
			SchemaVersion: profiler.ActuationPolicySchemaVersion,
			ActuationPolicySpec: profiler.ActuationPolicySpec{
				Name: "jetson-mode-v1", DeviceClass: parent.Spec.State.Candidate.DeviceClass,
				PowerMode: parent.Spec.State.Candidate.PowerMode, ActuatorKind: target.Kind,
				SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
			},
		},
	}
	return profiler.PreparedRun{
		RunConfig: profilingrun.RunConfig{
			State: profilingrun.ExecutionState{
				DeviceClass: parent.Spec.State.Candidate.DeviceClass,
				PowerMode:   parent.Spec.State.Candidate.PowerMode,
			},
			TargetNode: profilingrun.NodeSnapshot{Name: parent.Spec.State.TargetNode, UID: "node-uid"},
			Actuation: &profilingrun.ActuationContract{
				PowerMode: parent.Spec.State.Candidate.PowerMode,
				Actuator:  target, PolicyName: policy.Policy.Name,
			},
		},
		ActuationPolicy: &policy,
	}
}
