package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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
	spec := api.EvaluationRunSpec{
		Question: "compare", EvidenceStore: "evidence", Harness: image,
		Target: api.TargetIdentity{Components: []api.EvaluationTargetComponent{{Name: "server",
			Resource: api.EvaluationResourceReference{APIVersion: "v1", Kind: "Pod", Namespace: "ns", Name: "server"}}}},
		LoadGenerator: api.Generator{EvaluationImage: image, NodeName: "genesis1", Shards: map[string]int32{"workload": 1},
			Barrier: api.GeneratorBarrier{MinimumReadySamples: 1}},
		PowerMeasurement: api.PowerMeasurement{Meters: []api.EvaluationMeter{{ID: "meter", SourceIdentity: "source"}},
			Nodes: []api.EvaluationObjectReference{{Name: "node", UID: "node-uid"}}, Series: "power",
			Sampling: api.PowerSampling{IntervalNanos: 1}},
		Judgment: api.Judgment{EnergyCoverage: api.EnergyCoverage{MaxSingleGapSeconds: 1}},
		Stages: []api.StageTemplate{{ID: "stage", Row: "evaluation",
			LoadScenarioRef: api.EvaluationObjectReference{Name: "load", UID: "load-uid"}}},
	}
	service := &evaluationService{reader: native, client: native, namespace: "ns"}
	return service, httpapi.EvaluationRequest{Name: "run", Spec: spec}
}
func TestEvaluationPreflightBoundaries(t *testing.T) {
	for _, fault := range []string{"none", "pool", "load-generator", "profiling", "unready"} {
		t.Run(fault, func(t *testing.T) {
			service, request := evaluationFixture(t)
			var object client.Object
			switch fault {
			case "pool":
				object = &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: "occupied", Namespace: "ns"},
					Status: api.EvaluationRunStatus{Conditions: []metav1.Condition{{
						Type: api.EvaluationConditionPoolAcquired, Status: metav1.ConditionTrue,
					}}}}
			case "load-generator":
				object = &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
					Name: "load-generator", Namespace: "ns", Labels: map[string]string{"edge.dacs.io/workload-role": "load-generator"},
				}}
			case "profiling":
				object = &api.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Name: "profile", Namespace: "ns"}}
			case "unready":
				pod := &corev1.Pod{}
				key := client.ObjectKey{Namespace: "ns", Name: "server"}
				if err := service.client.Get(context.Background(), key, pod); err != nil {
					t.Fatal(err)
				}
				pod.Status.Conditions[0].Status = corev1.ConditionFalse
				if err := service.client.Status().Update(context.Background(), pod); err != nil {
					t.Fatal(err)
				}
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
			if fault == "unready" && (result.LiveTarget[0].Ready || len(result.LiveTarget[0].LiveImageIDs) != 1) {
				t.Fatal("live target readiness lost")
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
	replaceRequest := request
	replaceRequest.Name = ""
	replacement, err := service.replace(ctx, created.Name, created.UID, replaceRequest, "operator")
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
	if _, err := service.release(ctx, request.Name, release, "operator"); err == nil {
		t.Fatal("running release accepted")
	}
	run.Status.Conditions = append(run.Status.Conditions,
		metav1.Condition{Type: api.EvaluationConditionExecutionComplete, Status: metav1.ConditionTrue},
		metav1.Condition{Type: api.EvaluationConditionCleanupComplete, Status: metav1.ConditionFalse},
	)
	if err := service.client.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
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

func TestEvaluationViewFlattensStatus(t *testing.T) {
	service, request := evaluationFixture(t)
	now := metav1.NewTime(time.Now().UTC())
	run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: request.Name, Namespace: "ns"}}
	run.Status = api.EvaluationRunStatus{
		CurrentStage: "stage", StartedAt: &now, FinishedAt: &now,
		Conditions: []metav1.Condition{{Type: api.EvaluationConditionPublished, Status: metav1.ConditionTrue}},
	}
	if err := service.client.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	detail, err := service.get(context.Background(), run.Name)
	if err != nil {
		t.Fatal(err)
	}
	list, err := service.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []httpapi.EvaluationView{detail, list.Items[0]} {
		body, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"conditions", "currentStage", "startedAt", "finishedAt", "liveTarget"} {
			if len(fields[key]) == 0 {
				t.Fatalf("missing top-level %s: %s", key, body)
			}
		}
		if _, exists := fields["status"]; exists {
			t.Fatal("nested status leaked into projection")
		}
	}
}

func TestEvaluationPreflightValidatesCanonicalSettingsPatch(t *testing.T) {
	for _, test := range []struct {
		patch string
		valid bool
	}{
		{`{"controls":{"power":false}}`, true},
		{`{"maxReplicas":3}`, true},
		{`{"unknown":true}`, false},
		{`{"unknown":null}`, false},
		{`{"controls":{"unknown":false}}`, false},
		{`{"maxReplicas":0}`, false},
	} {
		t.Run(test.patch, func(t *testing.T) {
			service, request := evaluationFixture(t)
			service.settings = func() (serving.SchedulerSettings, error) {
				return serving.SchedulerSettings{
					ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
					MaxObservationAge: time.Second, MaxReplicas: 1, MaxInstances: 1,
					CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
					CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
					CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
				}, nil
			}
			request.Spec.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(test.patch)}
			result, err := service.preflight(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, check := range result.Checks {
				if check.ID == "settingsPatch/run" {
					found = true
					if check.Passed != test.valid {
						t.Fatalf("check=%+v", check)
					}
				}
			}
			if !found {
				t.Fatal("settings patch check missing")
			}
			_, err = service.create(context.Background(), request, "operator")
			if (err == nil) != test.valid {
				t.Fatalf("create=%v", err)
			}
		})
	}
}

func TestEvaluationRecordsObservedTargetWithoutImageGate(t *testing.T) {
	service, request := evaluationFixture(t)
	pod := &corev1.Pod{}
	key := client.ObjectKey{Namespace: "ns", Name: "server"}
	if err := service.client.Get(context.Background(), key, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses[0].ImageID = "another/runtime@sha256:observed"
	pod.Status.ContainerStatuses[0].RestartCount = 9
	if err := service.client.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	id, err := service.create(context.Background(), request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	var run api.EvaluationRun
	key.Name = id.Name
	if err := service.reader.Get(context.Background(), key, &run); err != nil {
		t.Fatal(err)
	}
	var recorded []httpapi.EvaluationLiveTarget
	if err := json.Unmarshal([]byte(run.Annotations["edge.dacs.io/evaluation-live-target"]), &recorded); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || !recorded[0].Ready || recorded[0].LiveImageIDs[0] != "another/runtime@sha256:observed" {
		t.Fatalf("record=%+v", recorded)
	}
}

func TestEvaluationViewProjectsOnlyRecordedLiveTarget(t *testing.T) {
	recorded := `[{"component":"server","resource":` +
		`{"apiVersion":"apps/v1","kind":"Deployment","namespace":"ns","name":"server"},` +
		`"liveImageIDs":["recorded-image"],"ready":false}]`
	for _, test := range []struct{ name, annotation, want string }{
		{"absent", "", "null"},
		{"invalid JSON", "[", "null"},
		{"wrong shape", `{"component":"server"}`, "null"},
		{"partial decode", `[{"component":"server"},{"ready":42}]`, "null"},
		{"recorded null", "null", "null"},
		{"recorded empty", "[]", "[]"},
		{"recorded identity", recorded, recorded},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{"edge.dacs.io/evaluation-live-target": test.annotation},
			}}
			raw, err := json.Marshal(evaluationView(run))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["liveTarget"]) != test.want {
				t.Fatalf("liveTarget=%s want=%s", fields["liveTarget"], test.want)
			}
		})
	}
}
