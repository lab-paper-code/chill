package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func evaluationFixture(t *testing.T) (*evaluationService, httpapi.EvaluationRequest) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "ns"}}
	pod.Spec.Containers = []corev1.Container{{Name: "server", Image: "repo/server@sha256:live"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "server", Ready: true, ImageID: "repo/server@sha256:live",
	}}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	load := &api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "load", Namespace: "ns", UID: "load-uid"}}
	sequence := 0
	native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, load).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			sequence++
			if obj.GetUID() == "" {
				obj.SetUID(types.UID(fmt.Sprintf("created-%d", sequence)))
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	image := api.EvaluationImage{Image: "repo/server@sha256:live", SourceSHA: "source"}
	ref := api.EvaluationEvidenceReference{Store: "evidence", Key: "start", Size: 1}
	spec := api.EvaluationRunSpec{
		Question: "compare", EvidenceStore: "evidence", Harness: image,
		Target: api.TargetIdentity{Components: []api.EvaluationTargetComponent{{Name: "server", EvaluationImage: image,
			Resource: api.EvaluationResourceReference{APIVersion: "v1", Kind: "Pod", Namespace: "ns", Name: "server"}}}},
		LoadGenerator: api.Generator{EvaluationImage: image, Shards: map[string]int32{"workload": 1},
			Barrier: api.GeneratorBarrier{MinimumReadySamples: 1}},
		PowerMeasurement: api.PowerMeasurement{Meters: []api.EvaluationMeter{{ID: "meter", SourceIdentity: "source"}},
			Nodes: []api.EvaluationObjectReference{{Name: "node", UID: "node-uid"}}, Series: "power",
			Sampling: api.PowerSampling{IntervalNanos: 1}},
		Judgment: api.Judgment{EnergyCoverage: api.EnergyCoverage{MaxSingleGapSeconds: 1}},
		Stages: []api.StageTemplate{{ID: "stage", Row: "E5", Mode: api.EvaluationModeFixed,
			LoadScenarioRef: api.EvaluationObjectReference{Name: "load", UID: "load-uid"},
			StartState:      api.EvaluationStartState{EvidenceRef: &ref}}},
	}
	service := &evaluationService{reader: native, client: native, namespace: "ns"}
	return service, httpapi.EvaluationRequest{Name: "run", Spec: spec}
}
func TestEvaluationPreflightBoundaries(t *testing.T) {
	for _, fault := range []string{"none", "pool", "e5", "profiling", "digest"} {
		t.Run(fault, func(t *testing.T) {
			service, request := evaluationFixture(t)
			now := metav1.NewTime(time.Now())
			var object client.Object
			switch fault {
			case "pool":
				object = &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: "occupied", Namespace: "ns"},
					Status: api.EvaluationRunStatus{StartedAt: &now}}
			case "e5":
				object = &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
					Name: "legacy", Namespace: "ns", Labels: map[string]string{"chill.dacs.io/e5": "full"},
				}}
			case "profiling":
				object = &api.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Name: "profile", Namespace: "ns"}}
			case "digest":
				request.Spec.Target.Components[0].Image = "repo/server@sha256:other"
			}
			if object != nil {
				if err := service.client.Create(context.Background(), object); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.preflight(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for _, check := range result.Checks {
				if !check.Passed {
					failed = true
				}
			}
			if failed != (fault != "none") {
				t.Fatalf("checks=%+v", result.Checks)
			}
			if fault == "digest" && (result.LiveTarget[0].Match || len(result.LiveTarget[0].LiveImageIDs) != 1) {
				t.Fatal("live target diff lost")
			}
		})
	}
}
func TestEvaluationLifecycleMutationGuards(t *testing.T) {
	service, request := evaluationFixture(t)
	ctx := context.Background()
	created, err := service.create(ctx, request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.get(ctx, created.Name)
	if err != nil || view.Phase != evaluationPending {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if _, err := service.delete(ctx, created.Name, "wrong", "operator"); err == nil {
		t.Fatal("UID mismatch accepted")
	}
	replacement, err := service.replace(ctx, created.Name, created.UID, request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.UID == created.UID || replacement.UID == "" {
		t.Fatal("replacement did not receive a new UID")
	}
	var run api.EvaluationRun
	if err := service.reader.Get(ctx, client.ObjectKey{Namespace: "ns", Name: request.Name}, &run); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now())
	run.Status.StartedAt = &now
	run.Status.Conditions = []metav1.Condition{{Type: api.EvaluationConditionPoolAcquired, Status: metav1.ConditionTrue}}
	if err := service.client.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if _, err := service.replace(ctx, request.Name, replacement.UID, request, "operator"); err == nil {
		t.Fatal("Running replacement accepted")
	}
	release := httpapi.EvaluationReleaseRequest{UID: replacement.UID, Reason: "checked"}
	if _, err := service.release(ctx, request.Name, release, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.delete(ctx, request.Name, replacement.UID, "operator"); err != nil {
		t.Fatal(err)
	}
}
func TestEvaluationPhaseTable(t *testing.T) {
	now := metav1.NewTime(time.Now())
	for _, tc := range []struct {
		name                     string
		started                  bool
		pool, execution, cleanup metav1.ConditionStatus
		want                     string
	}{
		{"pending", false, "", "", "", evaluationPending},
		{"running", true, metav1.ConditionTrue, metav1.ConditionUnknown, metav1.ConditionUnknown, "Running"},
		{"finishing", true, metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionUnknown, "Finishing"},
		{"done", true, metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue, "Done"},
		{"blocked", true, metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionFalse, "Blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := api.EvaluationRun{}
			if tc.started {
				run.Status.StartedAt = &now
			}
			run.Status.Conditions = []metav1.Condition{
				{Type: api.EvaluationConditionPoolAcquired, Status: tc.pool},
				{Type: api.EvaluationConditionExecutionComplete, Status: tc.execution},
				{Type: api.EvaluationConditionCleanupComplete, Status: tc.cleanup},
			}
			if got := evaluationPhase(&run); got != tc.want {
				t.Fatalf("phase=%s want=%s", got, tc.want)
			}
		})
	}
}
