package kube

import (
	"context"
	"slices"
	"strings"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	corev1 "k8s.io/api/core/v1"
)

func TestSessionFreezesSelectedMaterialAndNodeCappedEnvelope(t *testing.T) {
	objects, intent := firstProfileInputs(t)
	model := objects[0].(*edge.ModelSpec)
	model.Spec.Artifacts = append(model.Spec.Artifacts, edge.ModelArtifact{Name: "unused"})
	model.Spec.ExecutionPaths = append(model.Spec.ExecutionPaths, edge.ModelExecutionPath{Name: "unused", Artifact: "absent"})
	device := objects[1].(*edge.DeviceClass)
	device.Spec.PowerModes = append(device.Spec.PowerModes, edge.PowerMode{Name: "unused"})
	facts := objects[2].(*corev1.ConfigMap)
	facts.Data = map[string]string{
		"execution-facts.yaml": strings.Replace(validExecutionFacts(), "metricObserverImage: "+testMetricObserverImage+"\n", "", 1) +
			"  - deviceClass: unused\n    runtime: {payload: broken}\nfutureField: true\n",
		"execution-policies.yaml": `policies:
  - deviceClass: unused
    executionPath: unused
    workloadMode: unused
  - deviceClass: "*"
    executionPath: ort-cpu
    workloadMode: empirical-cadence-fixed-batch
    maxInputBytes: 1
    runtimeMemoryRequestBytes: 1
    runtimeMemoryLimitBytes: 1
  - deviceClass: lattepanda-3-delta-8g
    executionPath: ort-cpu
    workloadMode: empirical-cadence-fixed-batch
    maxInputBytes: 67108864
    runtimeMemoryRequestBytes: 134217728
    runtimeMemoryLimitBytes: 17179869184
futureField: true
`,
	}
	reader := candidateFakeReader(t, objects...)
	candidates, err := NewKubernetesSessionCandidateResolver(reader, candidateResolverOptions())
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := NewKubernetesNodeResolver(reader)
	if err != nil {
		t.Fatal(err)
	}
	workloads, err := NewKubernetesSessionWorkloadResolver(reader)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := NewKubernetesSessionExecutionPolicyResolver(reader, ExecutionPolicyResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: facts.Name, ConfigMapKey: "execution-policies.yaml", MeasurementMemoryFraction: .5,
	})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := profiler.NewSessionPreparer(candidates, nodes, workloads, execution)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := preparer.PrepareSession(context.Background(), profiler.SessionRequest{
		Candidate: validCandidateSelection(), Workload: intent.Spec.ActiveProtocol.Workload, WarmupIterations: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := prepared.RunConfig
	if config.Actor.MemoryLimitBytes != 4<<30 || config.Actor.MemoryRequestBytes != 128<<20 || config.Actor.MaxQueueBytes != 64<<20 ||
		config.Actor.WarmupIterations != 3 || config.Images.Runtime != testRuntimeImage ||
		config.Measurement.Dataset.Name != "vqav2-val-subset" || config.Measurement.SLOPercentile != 95 ||
		!slices.Equal(prepared.PlannedBatchSizes, []int{1, 2, 4}) {
		t.Fatalf("session froze wrong executable measurement: %+v", prepared)
	}
}
