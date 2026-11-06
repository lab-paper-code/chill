package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func evaluationFixture(t *testing.T) (*Service, EvaluationRequest) {
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
	load := &api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "load", Namespace: "ns", UID: "load-uid"},
		Spec: api.LoadScenarioSpec{Workloads: []api.EvaluationWorkloadReference{{Name: "workload"}}}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: api.EvaluationConfigName, Namespace: "ns"},
		Data: map[string]string{"operational": `{"workloads":{"workload":{}}}`}}
	sequence := 0
	native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, load, config).WithInterceptorFuncs(interceptor.Funcs{
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
	service := &Service{reader: native, client: native, namespace: "ns", store: "evidence"}
	return service, EvaluationRequest{Name: "run", Spec: spec}
}

// preflightFault applies one boundary fault and returns an object to create, if any.
func preflightFault(t *testing.T, service *Service, request *EvaluationRequest, fault string) client.Object {
	t.Helper()
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
	case "idle-baseline":
		// Defect 23: a measuring baseline launches its next run soon; it holds the pool like a BEO.
		object = &api.ModelLoadedIdleBaseline{ObjectMeta: metav1.ObjectMeta{Name: "baseline", Namespace: "ns"},
			Status: api.ModelLoadedIdleBaselineStatus{Phase: api.ModelLoadedIdleBaselinePhaseMeasuring}}
	case "finished-parent-runs":
		// A finished campaign's runs are judged through the campaign, not one by one.
		parent := &api.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Name: "campaign", Namespace: "ns"},
			Status: api.BatchEnergyOptimizationStatus{Phase: string(api.MeasurementPhaseComplete)}}
		if err := service.client.Create(context.Background(), parent); err != nil {
			t.Fatal(err)
		}
		object = &api.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "ns",
			Labels: map[string]string{"edge.dacs.io/batch-energy-optimization": "campaign"}}}
	case "other-jobs":
		// Other running Jobs remain environment observations.
		object = &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "ns"}}
	case "foreign-store":
		// Defect 18: a Run naming another store would publish refs this server cannot read.
		request.Spec.EvidenceStore = "chill-evidence-store"
	case "shard-coverage":
		// Defect 21: preflight applies the run's own coverage rule, not a weaker copy.
		request.Spec.LoadGenerator.Shards["other"] = 1
	case "operational-coverage":
		config := &corev1.ConfigMap{}
		key := client.ObjectKey{Namespace: "ns", Name: api.EvaluationConfigName}
		if err := service.client.Get(context.Background(), key, config); err != nil {
			t.Fatal(err)
		}
		config.Data["operational"] = `{"workloads":{"workload":{},"other":{}}}`
		if err := service.client.Update(context.Background(), config); err != nil {
			t.Fatal(err)
		}
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
	return object
}
func TestEvaluationPreflightBoundaries(t *testing.T) {
	for _, fault := range []string{"none", "pool", "load-generator", "profiling", "unready", "foreign-store", "shard-coverage", "operational-coverage",
		"idle-baseline", "finished-parent-runs", "other-jobs"} {
		t.Run(fault, func(t *testing.T) {
			service, request := evaluationFixture(t)
			object := preflightFault(t, service, &request, fault)
			if object != nil {
				if err := service.client.Create(context.Background(), object); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.Preflight(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for _, check := range result.Checks {
				if !check.Passed {
					failed = true
				}
			}
			if failed != (fault != "none" && fault != "finished-parent-runs" && fault != "other-jobs" && fault != "load-generator" && fault != "unready") {
				t.Fatalf("checks=%+v", result.Checks)
			}
			if fault == "foreign-store" {
				if _, err := service.Create(context.Background(), request, "operator"); err == nil || !strings.Contains(err.Error(), "evidenceStore") {
					t.Fatalf("Run naming a foreign evidence store must be refused, got %v", err)
				}
			}
			if strings.HasSuffix(fault, "-coverage") {
				if _, err := service.Create(context.Background(), request, "operator"); err == nil || !strings.Contains(err.Error(), "coverage") {
					t.Fatalf("coverage mismatch must be refused at creation, got %v", err)
				}
			}
			if fault == "unready" && (result.LiveTarget[0].Ready || len(result.LiveTarget[0].LiveImageIDs) != 1) {
				t.Fatal("live target readiness lost")
			}
			if fault == "unready" || fault == "load-generator" {
				request.Spec.Harness = api.EvaluationImage{Image: "harness:current"}
				request.Spec.LoadGenerator.EvaluationImage = api.EvaluationImage{Image: "generator:current"}
				if _, err := service.Create(context.Background(), request, "operator"); err != nil {
					t.Fatalf("observed unavailable/shared supply must not block experiment creation: %v", err)
				}
			}
		})
	}
}

func TestEvaluationTargetIgnoresTerminatedDeploymentPods(t *testing.T) {
	for _, tc := range []struct {
		name       string
		phase      corev1.PodPhase
		live       bool
		wantReady  bool
		wantImages int
	}{
		{"completed with serving replica", corev1.PodSucceeded, true, true, 1},
		{"failed with serving replica", corev1.PodFailed, true, true, 1},
		{"completed only", corev1.PodSucceeded, false, false, 0},
		{"failed only", corev1.PodFailed, false, false, 0},
		{"pending replica", corev1.PodPending, true, false, 2},
		{"unready running replica", corev1.PodRunning, true, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, request := evaluationFixture(t)
			ctx := context.Background()
			selector := map[string]string{"app": "server"}
			deployment := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]interface{}{"name": "server", "namespace": "ns"},
				"spec": map[string]interface{}{"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"app": "server"},
				}},
			}}
			if err := service.client.Create(ctx, deployment); err != nil {
				t.Fatal(err)
			}
			request.Spec.Target.Components[0].Resource.APIVersion = "apps/v1"
			request.Spec.Target.Components[0].Resource.Kind = "Deployment"
			pod := &corev1.Pod{}
			if err := service.client.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "server"}, pod); err != nil {
				t.Fatal(err)
			}
			if tc.live {
				pod.Labels = selector
				if err := service.client.Update(ctx, pod); err != nil {
					t.Fatal(err)
				}
				pod.Status.Phase = corev1.PodRunning
				if err := service.client.Status().Update(ctx, pod); err != nil {
					t.Fatal(err)
				}
			}
			stale := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "previous-replica", Namespace: "ns", Labels: selector,
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "previous", UID: "previous"}}},
				Status: corev1.PodStatus{Phase: tc.phase,
					Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
					ContainerStatuses: []corev1.ContainerStatus{{Name: "server", ImageID: "repo/server@sha256:previous"}},
				},
			}
			if err := service.client.Create(ctx, stale); err != nil {
				t.Fatal(err)
			}
			result, err := service.Preflight(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			target := result.LiveTarget[0]
			if target.Ready != tc.wantReady || len(target.LiveImageIDs) != tc.wantImages {
				t.Fatalf("target=%+v, want ready=%v images=%d", target, tc.wantReady, tc.wantImages)
			}
			if _, err := service.Create(ctx, request, "operator"); err != nil {
				t.Fatalf("target readiness is observational: %v", err)
			}
		})
	}
}
func TestEvaluationLifecycleMutationGuards(t *testing.T) {
	service, request := evaluationFixture(t)
	ctx := context.Background()
	created, err := service.Create(ctx, request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Get(ctx, created.Name)
	if err != nil || view.Phase != evaluationPending {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if _, err := service.Delete(ctx, created.Name, "wrong", "operator"); err == nil {
		t.Fatal("UID mismatch accepted")
	}
	replaceRequest := request
	replaceRequest.Name = ""
	replacement, err := service.Replace(ctx, created.Name, created.UID, replaceRequest, "operator")
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
	if _, err := service.Replace(ctx, request.Name, replacement.UID, request, "operator"); err == nil {
		t.Fatal("Running replacement accepted")
	}
	release := EvaluationReleaseRequest{UID: replacement.UID, Reason: "checked"}
	if _, err := service.Release(ctx, request.Name, release, "operator"); err == nil {
		t.Fatal("running release accepted")
	}
	run.Status.Conditions = append(run.Status.Conditions,
		metav1.Condition{Type: api.EvaluationConditionExecutionComplete, Status: metav1.ConditionTrue},
		metav1.Condition{Type: api.EvaluationConditionCleanupComplete, Status: metav1.ConditionFalse},
	)
	if err := service.client.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Release(ctx, request.Name, release, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, request.Name, replacement.UID, "operator"); err != nil {
		t.Fatal(err)
	}
}

// Defect 20: a stop is visible as a fact only once deletion was actually requested; the
// annotation alone (patched before a failed delete) does not claim a stop, and phase stays.
func TestEvaluationViewStopRequested(t *testing.T) {
	service, request := evaluationFixture(t)
	ctx := context.Background()
	created, err := service.Create(ctx, request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	var run api.EvaluationRun
	if err := service.reader.Get(ctx, client.ObjectKey{Namespace: "ns", Name: created.Name}, &run); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now())
	run.Finalizers = []string{api.EvaluationRunFinalizer}
	run.Annotations["edge.dacs.io/stop-requested-by"] = "operator"
	run.Status.StartedAt = &now
	run.Status.Conditions = []metav1.Condition{{Type: api.EvaluationConditionPoolAcquired, Status: metav1.ConditionTrue}}
	if err := service.client.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	view, err := service.Get(ctx, created.Name)
	if err != nil || view.StopRequested != nil {
		t.Fatalf("annotation without deletion must not report a stop: %+v err=%v", view.StopRequested, err)
	}
	phase := view.Phase
	if _, err := service.Delete(ctx, created.Name, created.UID, "dashboard-user"); err != nil {
		t.Fatal(err)
	}
	view, err = service.Get(ctx, created.Name)
	if err != nil || view.StopRequested == nil || view.StopRequested.At.IsZero() || view.StopRequested.By != "dashboard-user" || view.Phase != phase {
		t.Fatalf("stop request not projected as a fact: %+v phase=%q err=%v", view.StopRequested, view.Phase, err)
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
					DefaultOperationCost: map[string]serving.OperationCostValue{
						"prepare": {Seconds: 852, Joules: 36371},
						"remove":  {Seconds: 13, Joules: 1014},
						"power":   {Seconds: 166, Joules: 2229},
					},
					DefaultIdlePower:  &serving.IdlePowerDefault{NodeWatts: 54, PerInstanceWatts: 8},
					ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
					MaxObservationAge: time.Second, MaxReplicas: 1, MaxInstances: 1,
					CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
					CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
					CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
				}, nil
			}
			request.Spec.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(test.patch)}
			result, err := service.Preflight(context.Background(), request)
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
			_, err = service.Create(context.Background(), request, "operator")
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
	id, err := service.Create(context.Background(), request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	var run api.EvaluationRun
	key.Name = id.Name
	if err := service.reader.Get(context.Background(), key, &run); err != nil {
		t.Fatal(err)
	}
	var recorded []EvaluationLiveTarget
	if err := json.Unmarshal([]byte(run.Annotations["edge.dacs.io/evaluation-live-target"]), &recorded); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || !recorded[0].Ready || recorded[0].LiveImageIDs[0] != "another/runtime@sha256:observed" {
		t.Fatalf("record=%+v", recorded)
	}
}
