package controller

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type authorityPreparationPorts struct {
	config          profilingrun.RunConfig
	report, catalog []byte
	workloadError   error
}

func (p *authorityPreparationPorts) ResolveCandidate(
	_ context.Context, selection profiler.CandidateSelection,
) (profiler.ResolvedCandidate, error) {
	c := p.config
	dataset := c.Measurement.Dataset
	if selection.Dataset == "" {
		dataset = nil
	}
	return profiler.ResolvedCandidate{
		ReportBytes: p.report, Inputs: c.Candidate.Inputs, Verdict: c.Candidate.Verdict,
		State: c.State, Dataset: dataset, DeviceArchitecture: c.TargetNode.Architecture,
		DeviceNodeSelector: map[string]string{"edge.dacs.io/device-class": c.State.DeviceClass},
		DevicePowerModes:   []string{c.State.PowerMode}, RuntimeImage: c.Images.Runtime,
		ArtifactImage: c.Images.Artifact, ArtifactArchitecture: c.TargetNode.Architecture,
		MetricObserverImage: c.Images.MetricObserver,
	}, nil
}

func (p *authorityPreparationPorts) ResolveNode(context.Context, profiler.NodeSelection) (profilingrun.NodeSnapshot, error) {
	return p.config.TargetNode, nil
}

func (p *authorityPreparationPorts) ResolveMetricSource(context.Context, string) (profiler.ResolvedMetricSource, error) {
	c := p.config.Metrics
	return profiler.ResolvedMetricSource{Kind: c.SourceKind, Endpoint: c.Endpoint,
		PowerMetric: c.PowerMetric, MetricLabels: c.MetricLabels,
		ThermalEndpoint: c.ThermalEndpoint, ThermalMetrics: c.ThermalMetrics}, nil
}

func (p *authorityPreparationPorts) ResolveWorkloadObservation(context.Context) (profiler.ResolvedWorkloadObservation, error) {
	return profiler.ResolvedWorkloadObservation{Image: p.config.Images.WorkloadObserver}, nil
}

func (p *authorityPreparationPorts) ResolveMeasurementWorkload(
	context.Context, string, profiler.CandidateSelection,
) (profilingrun.MeasurementContract, error) {
	return p.config.Measurement, p.workloadError
}

func (*authorityPreparationPorts) ResolveActuationPolicy(
	context.Context, profiler.ActuationSelection,
) (profiler.FrozenActuationPolicy, error) {
	return profiler.FrozenActuationPolicy{}, errors.New("unexpected fixed-node actuation")
}

func (p *authorityPreparationPorts) Resolve(
	_ context.Context, config profilingrun.RunConfig,
) (profiler.FrozenExecutionPolicy, error) {
	return profiler.FreezeConfiguredExecutionPolicy(p.catalog, config, profiler.ExecutionPolicySource{
		Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: "chill-system", Name: "chill-profiler-facts",
		UID: "facts-uid", ResourceVersion: "42", Key: "execution-policies.yaml",
	})
}

func authorityRoot(config profilingrun.RunConfig) (*edge.BatchEnergyOptimization, *core.Node) {
	selected := testRun()
	root := &edge.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: selected.Namespace, Name: "frozen-plan", UID: "plan-uid",
			Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}},
		Spec: edge.BatchEnergyOptimizationSpec{
			State: selected.Spec.MeasurementStateSpec,
			ActiveProtocol: edge.ActiveProtocolSpec{Workload: config.Measurement.WorkloadName,
				Dataset: config.Measurement.Dataset.Name, WarmupIterations: 20},
		},
	}
	node := &core.Node{ObjectMeta: metav1.ObjectMeta{Name: config.TargetNode.Name, UID: types.UID(config.TargetNode.UID)},
		Spec: core.NodeSpec{Taints: []core.Taint{{Key: profilingrun.MeasurementTaintKey,
			Value: string(root.UID), Effect: core.TaintEffectNoSchedule}}}}
	return root, node
}

func authorityResolver(t *testing.T, kube client.Reader, config profilingrun.RunConfig) *RunConfigResolver {
	t.Helper()
	report, err := os.ReadFile("../testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	ports := &authorityPreparationPorts{config: config, report: report, catalog: controllerExecutionPolicyCatalog(t),
		workloadError: errors.New("live Workload is unavailable after plan freeze")}
	preparer, err := profiler.NewPreparer(ports, ports, ports, ports, ports, ports,
		func(context.Context, string) (string, profilingrun.LoadOriginGatewayPodContract, error) {
			return config.LoadOriginNode, *config.LoadOriginGatewayPod, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewRunConfigResolver(preparer, ports, kube)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func TestBatchEnergyOptimizationOwnedRunIsUnsupportedRoot(t *testing.T) {
	config := currentPreparedRun(t).RunConfig
	root, node := authorityRoot(config)
	child := testRun()
	child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(root,
		edge.GroupVersion.WithKind("BatchEnergyOptimization"))}
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(root, node).Build()
	_, err := authorityResolver(t, kube, config).Resolve(context.Background(), child)
	if err == nil || !strings.Contains(err.Error(), "unsupported measurement root") {
		t.Fatalf("err = %v", err)
	}
}

func TestRealAdmittedIdleBaselineRootRemainsExecutable(t *testing.T) {
	config := currentPreparedRun(t).RunConfig
	campaignRoot, node := authorityRoot(config)
	root := &edge.ModelLoadedIdleBaseline{
		ObjectMeta: metav1.ObjectMeta{Namespace: campaignRoot.Namespace, Name: "idle-baseline", UID: "idle-root-uid",
			Annotations: campaignRoot.Annotations},
		Spec: edge.ModelLoadedIdleBaselineSpec{
			State: campaignRoot.Spec.State, Batch: 1,
			Protocol: edge.IdleProtocolSpec{SettlingDurationSeconds: 5, DurationSeconds: 30,
				MetricObservation: testRun().Spec.MetricObservation},
			PointPolicy: edge.ModelLoadedIdleBaselinePointPolicySpec{
				Version: "idle-point-v1", MinimumRuns: 3, ConfidenceLevel: "0.95"},
			AttemptLimit: 5,
		},
	}
	node.Spec.Taints[0].Value = string(root.UID)
	child := testRun()
	child.Name, child.Labels = root.Name+"-idle-a1", map[string]string{"edge.dacs.io/attempt": "1"}
	child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(root,
		edge.GroupVersion.WithKind("ModelLoadedIdleBaseline"))}
	child.Spec.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(root, node).Build()
	resolved, err := authorityResolver(t, kube, config).Resolve(context.Background(), child)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Prepared.RunConfig.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		t.Fatal("authenticated idle baseline child changed execution mode")
	}
}
