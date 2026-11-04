package main

import (
	"context"
	"fmt"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	evaluation "github.com/lab-paper-code/chill/internal/evaluation/service"
	"github.com/lab-paper-code/chill/internal/serving"
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

func evaluationFixture(
	t *testing.T, settings func() (serving.SchedulerSettings, error),
) (*evaluation.Service, httpapi.EvaluationRequest) {
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
	native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, load, config).
		WithInterceptorFuncs(interceptor.Funcs{
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
	service := evaluation.New(native, native, "ns", "evidence", settings)
	return service, httpapi.EvaluationRequest{Name: "run", Spec: spec}
}
