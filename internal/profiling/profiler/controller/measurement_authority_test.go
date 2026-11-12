package controller

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
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

func authorityPlan(t *testing.T, config profilingrun.RunConfig) []byte {
	t.Helper()
	idle := config
	idle.LoadOriginNode, idle.LoadOriginGatewayPod = "", nil
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
		DurationSeconds: 30, DurationToleranceSeconds: .5, SettlingDurationSeconds: 5,
		StartupWarmupIterations: config.Measurement.StartupWarmupIterations, WarmupBatchSizes: []int{1},
	}
	spec, err := projection.ProjectCurvePlanSpec("frozen-plan", config, idle,
		derivation.CurveConversionContract{}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := derivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	body, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func authorityRoot(config profilingrun.RunConfig) (*edge.BatchEnergyOptimization, *core.Node) {
	selected := testRun()
	points := config.Measurement.OperatingPoints
	root := &edge.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: selected.Namespace, Name: "frozen-plan", UID: "plan-uid",
			Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}},
		Spec: edge.BatchEnergyOptimizationSpec{
			State: selected.Spec.MeasurementStateSpec, TrialsPerBatch: 3, RetryLimit: 1,
			ActiveProtocol: edge.ActiveProtocolSpec{Workload: config.Measurement.WorkloadName,
				Dataset: config.Measurement.Dataset.Name, WarmupIterations: 20, DurationSeconds: 30,
				OperatingPoints: edge.MeasurementOperatingPointsSpec{
					Utilizations: slices.Clone(points.Utilizations), SparseMeanGapMilliseconds: points.SparseMeanGapMilliseconds,
					PhaseDurationSeconds: int32(points.PhaseDurationSeconds), RequestLifetimeSeconds: int32(points.RequestLifetimeSeconds),
					DeliveryMarginMilliseconds:     int32(points.DeliveryMarginMilliseconds),
					FramedDecodedRequestUpperBytes: points.FramedDecodedRequestUpperBytes,
				},
				MetricObservation: selected.Spec.MetricObservation},
			IdleProtocol: edge.IdleProtocolSpec{SettlingDurationSeconds: 5, DurationSeconds: 30,
				MetricObservation: selected.Spec.MetricObservation},
		},
	}
	node := &core.Node{ObjectMeta: metav1.ObjectMeta{Name: config.TargetNode.Name, UID: types.UID(config.TargetNode.UID)},
		Spec: core.NodeSpec{Taints: []core.Taint{{Key: profilingrun.MeasurementTaintKey,
			Value: string(root.UID), Effect: core.TaintEffectNoSchedule}}}}
	return root, node
}

func authorityChild(root *edge.BatchEnergyOptimization, mode campaign.WorkloadMode, batch int) *edge.ProfilingRun {
	run := testRun()
	run.Name = campaign.RunName(root.Name, campaign.Measurement{WorkloadMode: mode, Batch: batch}, 1)
	run.Spec.CampaignTrial = &edge.ProfilingRunCampaignTrialSpec{Batch: int32(batch), Attempt: 1}
	run.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(root,
		edge.GroupVersion.WithKind("BatchEnergyOptimization"))}
	if mode == campaign.WorkloadModeIdle {
		run.Spec.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	}
	return run
}

func authorityResolver(t *testing.T, kube client.Reader, config profilingrun.RunConfig, plans map[types.UID][]byte) *RunConfigResolver {
	t.Helper()
	report, err := os.ReadFile("../testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	ports := &authorityPreparationPorts{config: config, report: report, catalog: controllerExecutionPolicyCatalog(t),
		workloadError: errors.New("live Workload is unavailable after plan freeze")}
	preparer, err := profiler.NewPreparer(ports, ports, ports, ports, ports, ports)
	if err != nil {
		t.Fatal(err)
	}
	preparer.ResolveLoadOrigin = func(context.Context, string) (string, profilingrun.LoadOriginGatewayPodContract, error) {
		return config.LoadOriginNode, *config.LoadOriginGatewayPod, nil
	}
	resolver, err := NewRunConfigResolver(preparer, ports, kube,
		func(_ context.Context, uid types.UID) (derivation.CurvePlan, error) {
			body, found := plans[uid]
			if !found {
				return derivation.CurvePlan{}, evidencestore.ErrNotFound
			}
			return derivation.DecodeCurvePlan(body)
		})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func TestRealAdmittedRootPlanSuppliesChildrenWithoutArchive(t *testing.T) {
	for _, mode := range []campaign.WorkloadMode{campaign.WorkloadModeActive, campaign.WorkloadModeIdle} {
		t.Run(string(mode), func(t *testing.T) {
			config := currentPreparedRun(t).RunConfig
			root, node := authorityRoot(config)
			child := authorityChild(root, mode, 1)
			child.Name = "opaque-child"
			kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(root, node).Build()
			resolver := authorityResolver(t, kube, config, map[types.UID][]byte{root.UID: authorityPlan(t, config)})
			resolved, err := resolver.Resolve(context.Background(), child)
			if err != nil {
				t.Fatal(err)
			}
			actual, frozen := resolved.Prepared.RunConfig.Measurement, config.Measurement
			if mode == campaign.WorkloadModeActive &&
				(actual.WorkloadUID != frozen.WorkloadUID || actual.WorkloadResourceVersion != frozen.WorkloadResourceVersion ||
					actual.SLOPercentile != frozen.SLOPercentile || actual.SLOTargetLatencyNanos != frozen.SLOTargetLatencyNanos ||
					actual.MinServiceSamples != frozen.MinServiceSamples) {
				t.Fatal("active child lost the recorded plan's actual Workload/SLO/sample stop")
			}
		})
	}
}

func TestCampaignAuthorizationRetainsFrozenStartupInsteadOfPlannedDomain(t *testing.T) {
	for _, mode := range []campaign.WorkloadMode{campaign.WorkloadModeActive, campaign.WorkloadModeIdle} {
		t.Run(string(mode), func(t *testing.T) {
			config := currentPreparedRun(t).RunConfig
			plan, err := derivation.DecodeCurvePlan(authorityPlan(t, config))
			if err != nil {
				t.Fatal(err)
			}
			spec := plan.Shared()
			spec.PlannedBatchSizes = []int{1, 2}
			plan, err = derivation.NewCurvePlan(spec)
			if err != nil {
				t.Fatal(err)
			}
			body, err := plan.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			root, node := authorityRoot(config)
			root.Spec.ActiveProtocol.WarmupIterations = 999
			kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(root, node).Build()
			resolver := authorityResolver(t, kube, config, map[types.UID][]byte{root.UID: body})
			resolved, err := resolver.Resolve(context.Background(), authorityChild(root, mode, 1))
			if err != nil {
				t.Fatal(err)
			}
			actor := resolved.Prepared.RunConfig.Actor
			if !slices.Equal(actor.WarmupBatchSizes, spec.Actor.WarmupBatchSizes) ||
				actor.WarmupIterations != spec.Actor.WarmupIterations {
				t.Fatalf("frozen startup changed: actual=%+v frozen=%+v", actor, spec.Actor)
			}
		})
	}
}

func TestForgedOwnerAndUnplannedChildrenNeverPublishJob(t *testing.T) {
	for _, defect := range []string{"missing-root", "not-admitted", "missing-plan", "unplanned-K", "wrong-root-UID", "no-node-grant"} {
		t.Run(defect, func(t *testing.T) {
			config := currentPreparedRun(t).RunConfig
			root, node := authorityRoot(config)
			child := authorityChild(root, campaign.WorkloadModeActive, 1)
			objects := []client.Object{child, root, node}
			plans := map[types.UID][]byte{root.UID: authorityPlan(t, config)}
			switch defect {
			case "missing-root":
				objects = []client.Object{child}
			case "not-admitted":
				root.Annotations = nil
			case "missing-plan":
				delete(plans, root.UID)
			case "unplanned-K":
				child.Spec.CampaignTrial.Batch = 2
				child.Name = campaign.RunName(root.Name, campaign.Measurement{WorkloadMode: campaign.WorkloadModeActive, Batch: 2}, 1)
			case "wrong-root-UID":
				child.OwnerReferences[0].UID = "caller-invented-uid"
			case "no-node-grant":
				node.Spec.Taints = nil
			}
			kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).
				WithStatusSubresource(child).WithObjects(objects...).Build()
			resolver := authorityResolver(t, kube, config, plans)
			jobs := &recordingExecutionJobs{}
			finalizer := &recordingRunEvidenceFinalizer{}
			reconciler, err := NewRunReconciler(kube, kube, resolver.Resolve, jobs.Find, jobs.Ensure,
				finalizer.Finalize, finalizer.RuntimeTermination)
			if err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(child)
			if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			stored := &edge.ProfilingRun{}
			if err := kube.Get(context.Background(), key, stored); err != nil {
				t.Fatal(err)
			}
			if stored.Status.Job != nil {
				t.Fatalf("unauthenticated child reached Job creation: %+v", stored.Status.Job)
			}
		})
	}
}

func TestRealAdmittedIdleBaselineRootRemainsExecutable(t *testing.T) {
	config := currentPreparedRun(t).RunConfig
	campaignRoot, node := authorityRoot(config)
	root := &edge.ModelLoadedIdleBaseline{
		ObjectMeta: metav1.ObjectMeta{Namespace: campaignRoot.Namespace, Name: "idle-baseline", UID: "idle-root-uid",
			Annotations: campaignRoot.Annotations},
		Spec: edge.ModelLoadedIdleBaselineSpec{
			State: campaignRoot.Spec.State, Batch: 1, Protocol: campaignRoot.Spec.IdleProtocol,
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
	resolved, err := authorityResolver(t, kube, config, nil).Resolve(context.Background(), child)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Prepared.RunConfig.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		t.Fatal("authenticated idle baseline child changed execution mode")
	}
}
