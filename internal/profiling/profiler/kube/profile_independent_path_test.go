package kube

import (
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These are the existing static compatibility/installation fixtures, not a
// serving profile or a reconstructed Evaluation authority. Public intent
// supplies no UID, historical profile, or frozen Workload identity.
func firstProfileInputs(t *testing.T) ([]client.Object, *api.BatchEnergyOptimization) {
	t.Helper()
	objects := validCandidateObjects()
	facts := objects[2].(*core.ConfigMap)
	for key, value := range validMetricSourceConfigMap().Data {
		facts.Data[key] = value
	}
	facts.Data["workload-observation.yaml"] = validWorkloadObservationFacts()
	facts.Data["execution-policies.yaml"] = string(controllerExecutionPolicyCatalog(t))
	node := validNode()
	node.Status.Allocatable[core.ResourceMemory] = resource.MustParse("8Gi")
	workload := &api.Workload{
		ObjectMeta: meta.ObjectMeta{Name: "first-profile", UID: "first-profile-workload", ResourceVersion: "1"},
		Spec: api.WorkloadSpec{
			ModelRef:        api.CatalogReference{Name: "mobilenet-v2-050"},
			PreprocessorRef: api.CatalogReference{Name: "mobilenet-imagenet"},
			DatasetRef:      &api.CatalogReference{Name: "vqav2-val-subset"},
			SLO:             api.WorkloadSLO{Percentile: 95, TargetLatency: meta.Duration{Duration: time.Second}},
		},
	}
	objects = append(objects, node, workload)
	objects = append(objects,
		&api.ChillSystem{ObjectMeta: meta.ObjectMeta{Name: "first-profile-system", Labels: map[string]string{"app.kubernetes.io/instance": "first-profile-installation"}}},
		&core.Pod{ObjectMeta: meta.ObjectMeta{Name: "first-profile-gateway", Namespace: testSystemNamespace, UID: "first-profile-gateway-uid",
			Labels: map[string]string{"app.kubernetes.io/instance": "first-profile-installation", "app.kubernetes.io/component": "serving-entrypoint"}},
			Spec: core.PodSpec{NodeName: "gateway-node"}, Status: core.PodStatus{Phase: core.PodRunning,
				Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}}},
	)
	observation := api.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}
	intent := &api.BatchEnergyOptimization{
		TypeMeta:   meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization"},
		ObjectMeta: meta.ObjectMeta{Namespace: testSystemNamespace, Name: "first-profile"},
		Spec: api.BatchEnergyOptimizationSpec{
			State: api.MeasurementStateSpec{
				Candidate: api.ProfilingRunCandidateSpec{ModelSpec: workload.Spec.ModelRef.Name,
					Preprocessor: workload.Spec.PreprocessorRef.Name, DeviceClass: "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu", PowerMode: "fixed"}, TargetNode: node.Name,
			},
			ActiveProtocol: api.ActiveProtocolSpec{Workload: workload.Name, Dataset: workload.Spec.DatasetRef.Name,
				WarmupIterations: 1, DurationSeconds: 30, MetricObservation: observation,
				OperatingPoints: api.MeasurementOperatingPointsSpec{Utilizations: []float64{.5}, SparseMeanGapMilliseconds: 1000,
					PhaseDurationSeconds: 30, PostLoadDurationSeconds: 30, RequestLifetimeSeconds: 30, DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024}},
			IdleProtocol:   api.IdleProtocolSpec{SettlingDurationSeconds: 1, DurationSeconds: 30, MetricObservation: observation},
			TrialsPerBatch: 3,
		},
	}
	return objects, intent
}
