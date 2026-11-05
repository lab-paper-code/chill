package controller

import (
	"context"
	"os"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	projection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	run "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Exercise child creation, API projection, real preparation, Kubernetes Workload
// resolution and curve evidence admission together, not a copied stop-policy path.
func TestCampaignChildrenRetainFrozenWorkloadAfterIdle(t *testing.T) {
	for _, mutateSLO := range []bool{false, true} {
		name := "status-only-resource-version"
		if mutateSLO {
			name = "changed-slo-percentile"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			config := curveFixtureRunConfig(t)
			payload, err := os.ReadFile("../../profiler/testdata/candidate-report.json")
			if err != nil {
				t.Fatal(err)
			}
			parent := testParent()
			parent.Spec.State = api.MeasurementStateSpec{
				TargetNode: config.TargetNode.Name,
				Candidate: api.ProfilingRunCandidateSpec{
					ModelSpec: config.State.Model, DeviceClass: config.State.DeviceClass,
					ExecutionPath: config.Candidate.ExecutionPath, PowerMode: run.FixedPowerMode,
					Preprocessor: config.State.Preprocessor.Name,
				},
			}
			metric := api.ProfilingRunMetricObservationSpec{
				IntervalMilliseconds: int32(config.Metrics.IntervalMilliseconds),
				TimeoutMilliseconds:  int32(config.Metrics.TimeoutMilliseconds),
				DurationSeconds:      int32(config.Metrics.DurationSeconds),
			}
			parent.Spec.ActiveProtocol = api.ActiveProtocolSpec{
				WarmupIterations: 20, DurationSeconds: 30, DurationToleranceSeconds: ptr.To(.5),
				Workload: "mobilenet", Dataset: config.Measurement.Dataset.Name, MetricObservation: metric,
			}
			parent.Spec.IdleProtocol = api.IdleProtocolSpec{
				SettlingDurationSeconds: 60, DurationSeconds: 30,
				DurationToleranceSeconds: ptr.To(.5), MetricObservation: metric,
			}
			workload := &api.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "mobilenet", UID: "actual-workload-uid", ResourceVersion: "7"},
				Spec: api.WorkloadSpec{
					ModelRef:        api.CatalogReference{Name: config.State.Model},
					PreprocessorRef: api.CatalogReference{Name: config.State.Preprocessor.Name},
					DatasetRef:      &api.CatalogReference{Name: config.Measurement.Dataset.Name},
					SLO:             api.WorkloadSLO{Percentile: 95, TargetLatency: metav1.Duration{Duration: time.Second}},
				},
			}
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.ProfilingRun{}, &api.Workload{}).WithObjects(parent, workload).Build()
			workloads, err := profilerkube.NewKubernetesWorkloadObservationResolver(native, profilerkube.WorkloadObservationResolverOptions{
				Namespace: "chill-system", ConfigMapName: "unused-image-source", ConfigMapKey: "facts",
			})
			if err != nil {
				t.Fatal(err)
			}
			ports := &frozenWorkloadPreparationPorts{config: config, report: payload, workloads: workloads}
			preparer, err := profiler.NewPreparer(ports, ports, ports, ports, ports, ports)
			if err != nil {
				t.Fatal(err)
			}
			var frozenProtocol *derivation.CurveProtocolContract
			prepare := func(child *api.ProfilingRun) run.RunConfig {
				return prepareFrozenWorkloadChild(t, ctx, preparer, child, frozenProtocol)
			}
			activeMeasurement := campaign.Measurement{WorkloadMode: campaign.WorkloadModeActive, Batch: 1}
			idleMeasurement := campaign.Measurement{WorkloadMode: campaign.WorkloadModeIdle, Batch: 1}
			initialActive, err := BuildRun(parent, activeMeasurement, 1, config.State.Backend)
			if err != nil {
				t.Fatal(err)
			}
			initialIdle, err := BuildRun(parent, idleMeasurement, 1, config.State.Backend)
			if err != nil {
				t.Fatal(err)
			}
			activeConfig, idleConfig := prepare(initialActive), prepare(initialIdle)
			spec, err := projection.ProjectCurvePlanSpec("frozen-workload", activeConfig, idleConfig, derivation.CurveConversionContract{}, []int{1})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := derivation.NewCurvePlan(spec)
			if err != nil {
				t.Fatal(err)
			}
			protocol := plan.Shared().ActiveProtocol
			frozenProtocol = &protocol
			reconciler := &BatchEnergyOptimizationReconciler{client: native}
			var idleEvidence profilerevidence.VerifiedIdleEvidence
			for attempt := 1; attempt <= 3; attempt++ {
				if _, err := reconciler.createMeasurementRun(ctx, parent, idleMeasurement, attempt, plan); err != nil {
					t.Fatal(err)
				}
				child, err := BuildRun(parent, idleMeasurement, attempt, config.State.Backend)
				if err != nil {
					t.Fatal(err)
				}
				if err := native.Get(ctx, client.ObjectKeyFromObject(child), child); err != nil {
					t.Fatal(err)
				}
				idleEvidence = curveFixtureIdleEvidence(t, prepare(child), "idle-pod")
				child.Status.Conditions = []metav1.Condition{
					{Type: api.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionTrue, Reason: "JobCompleted"},
					{Type: api.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionTrue, Reason: "Verified"},
				}
				if err := native.Status().Update(ctx, child); err != nil {
					t.Fatal(err)
				}
			}
			mutation := "status-only"
			if mutateSLO {
				mutation = "slo"
			}
			live := mutateFrozenWorkload(t, native, workload, mutation)
			if live.ResourceVersion == workload.ResourceVersion {
				t.Fatal("Workload mutation did not advance ResourceVersion")
			}
			if _, err := reconciler.createMeasurementRun(ctx, parent, activeMeasurement, 1, plan); err != nil {
				t.Fatal(err)
			}
			child := &api.ProfilingRun{}
			if err := native.Get(ctx, client.ObjectKeyFromObject(initialActive), child); err != nil {
				t.Fatal(err)
			}
			measured := prepare(child)
			if measured.Measurement.WorkloadUID != "actual-workload-uid" || measured.Measurement.SLOPercentile != 95 ||
				measured.Measurement.MinServiceSamples != 59 || measured.Measurement.SLOTargetLatencyNanos != int64(time.Second) ||
				measured.Measurement.WorkloadResourceVersion != "7" || ports.workloadResolutions != 1 {
				t.Fatalf("frozen measurement=%+v, live resolutions=%d", measured.Measurement, ports.workloadResolutions)
			}
			activeEvidence := curveFixtureActiveEvidence(t, measured, "active-pod", 8)
			if _, err := projection.ProjectVerifiedCurveState(plan, activeEvidence, idleEvidence); err != nil {
				t.Fatalf("frozen child rejected after live Workload mutation: %v", err)
			}
		})
	}
}

func mutateFrozenWorkload(t *testing.T, native client.Client, workload *api.Workload, mutation string) *api.Workload {
	t.Helper()
	ctx := context.Background()
	live := &api.Workload{}
	if err := native.Get(ctx, client.ObjectKeyFromObject(workload), live); err != nil {
		t.Fatal(err)
	}
	var err error
	switch mutation {
	case "status-only":
		live.Status.Admission = api.WorkloadAdmission{Allowed: true, Reason: "Observed", ObservedAt: metav1.Now()}
		err = native.Status().Update(ctx, live)
	case "slo", "p100":
		live.Spec.SLO.Percentile = 99
		if mutation == "p100" {
			live.Spec.SLO.Percentile = 100
		}
		live.Spec.SLO.TargetLatency.Duration = 2 * time.Second
		err = native.Update(ctx, live)
	case "tuple":
		live.Spec.ModelRef.Name = "replacement-model"
		live.Spec.PreprocessorRef.Name = "replacement-preprocessor"
		live.Spec.DatasetRef.Name = "replacement-dataset"
		err = native.Update(ctx, live)
	case "deleted", "recreated":
		err = native.Delete(ctx, live)
		if err == nil && mutation == "recreated" {
			live.UID, live.ResourceVersion = "replacement-workload-uid", ""
			live.Spec.SLO.Percentile = 99
			err = native.Create(ctx, live)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return live
}

type frozenWorkloadPreparationPorts struct {
	config              run.RunConfig
	report              []byte
	workloads           profiler.WorkloadObservationResolver
	workloadResolutions int
}

func (p *frozenWorkloadPreparationPorts) ResolveCandidate(_ context.Context, selection profiler.CandidateSelection) (profiler.ResolvedCandidate, error) {
	config := p.config
	config.State.PowerMode = run.FixedPowerMode
	candidate := profiler.ResolvedCandidate{
		ReportBytes: p.report, Inputs: config.Candidate.Inputs, Verdict: config.Candidate.Verdict,
		State: config.State, DeviceArchitecture: config.TargetNode.Architecture,
		DeviceNodeSelector: map[string]string{"edge.dacs.io/device-class": config.State.DeviceClass},
		DevicePowerModes:   []string{run.FixedPowerMode}, RuntimeImage: config.Images.Runtime,
		ArtifactImage: config.Images.Artifact, ArtifactArchitecture: config.TargetNode.Architecture,
		MetricObserverImage: config.Images.MetricObserver,
	}
	if selection.Dataset != "" {
		candidate.Dataset = config.Measurement.Dataset
	}
	return candidate, nil
}

func (p *frozenWorkloadPreparationPorts) ResolveNode(context.Context, profiler.NodeSelection) (run.NodeSnapshot, error) {
	return p.config.TargetNode, nil
}

func (p *frozenWorkloadPreparationPorts) ResolveMetricSource(context.Context, string) (profiler.ResolvedMetricSource, error) {
	metric := p.config.Metrics
	return profiler.ResolvedMetricSource{Kind: metric.SourceKind, Endpoint: metric.Endpoint, PowerMetric: metric.PowerMetric,
		MetricLabels: metric.MetricLabels, ThermalEndpoint: metric.ThermalEndpoint, ThermalMetrics: metric.ThermalMetrics}, nil
}

func (p *frozenWorkloadPreparationPorts) ResolveWorkloadObservation(context.Context) (profiler.ResolvedWorkloadObservation, error) {
	return profiler.ResolvedWorkloadObservation{Image: p.config.Images.WorkloadObserver}, nil
}

func (p *frozenWorkloadPreparationPorts) ResolveMeasurementWorkload(ctx context.Context, name string, selection profiler.CandidateSelection) (run.MeasurementContract, error) {
	p.workloadResolutions++
	return p.workloads.ResolveMeasurementWorkload(ctx, name, selection)
}

func (p *frozenWorkloadPreparationPorts) ResolveActuationPolicy(context.Context, profiler.ActuationSelection) (profiler.FrozenActuationPolicy, error) {
	return profiler.FrozenActuationPolicy{}, nil
}

func (p *frozenWorkloadPreparationPorts) Resolve(_ context.Context, config run.RunConfig) (profiler.FrozenExecutionPolicy, error) {
	return profiler.FrozenExecutionPolicy{
		SchemaVersion: profiler.FrozenExecutionPolicySchemaVersion,
		Source: profiler.ExecutionPolicySource{
			Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: "chill-system",
			Name: "facts", UID: "facts-uid", ResourceVersion: "1", Key: "execution-policies.yaml",
		},
		Policy: profiler.ExecutionPolicy{
			SchemaVersion: profiler.ExecutionPolicySchemaVersion,
			ExecutionPolicySpec: profiler.ExecutionPolicySpec{
				Name: "test-policy", DeviceClass: "*", ExecutionPath: config.Candidate.ExecutionPath,
				WorkloadMode:                  config.Measurement.WorkloadMode,
				ActiveDeadlineOverheadSeconds: 180, AttemptTimeoutOverheadSeconds: 150,
				ObserverReadyTimeoutSeconds: 30, ObserverStartOverheadSeconds: 120,
				MetricResultOverheadSeconds: 30, WorkloadEndFenceTimeoutSeconds: 15,
				WorkloadResultTimeoutSeconds: 30,
				MaxLatencySamples:            1_000_000, MaxInputBytes: 64 << 20,
				MaxRuntimeResultBytes: 32 << 20, MaxMetricResultBytes: 8 << 20,
				MaxWorkloadEvents: 10_000, MaxWorkloadResultBytes: 8 << 20,
				RuntimeMemoryRequestBytes:  p.config.Actor.MemoryRequestBytes,
				RuntimeMemoryLimitBytes:    p.config.Actor.MemoryLimitBytes,
				ObserverMemoryRequestBytes: 16 << 20, ObserverMemoryLimitBytes: 64 << 20,
				WorkloadObserverMemoryRequestBytes: 32 << 20, WorkloadObserverMemoryLimitBytes: 128 << 20,
				CoordinationReserveBytes: 1 << 20, WorkspaceBytes: 128 << 20,
			},
		},
	}, nil
}

func prepareFrozenWorkloadChild(
	t *testing.T, ctx context.Context, preparer *profiler.Preparer, child *api.ProfilingRun,
	protocol *derivation.CurveProtocolContract,
) run.RunConfig {
	t.Helper()
	request, err := profilercontroller.ProjectRequest(child.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if protocol != nil && child.Spec.Measurement.WorkloadMode != run.IdleModelLoadedWorkloadMode {
		projection.ApplyFrozenActiveProtocol(&request.Preparation.Measurement, *protocol)
		request.Preparation.Workload = request.Preparation.Measurement.WorkloadName
	}
	prepared, err := preparer.Prepare(ctx, request.Preparation)
	if err != nil {
		t.Fatal(err)
	}
	return prepared.RunConfig
}
