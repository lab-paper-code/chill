package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/test/fixtures"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// These ports retain the reviewer's real Resolver -> ProjectRequest -> Preparer
// reproduction. Only external cluster/report sources are test boundaries.
type cadenceAuthorityPorts struct {
	config          profilingrun.RunConfig
	report, catalog []byte
	workloadError   error
}

func (p *cadenceAuthorityPorts) ResolveCandidate(context.Context, profiler.CandidateSelection) (profiler.ResolvedCandidate, error) {
	c := p.config
	return profiler.ResolvedCandidate{
		ReportBytes: p.report, Inputs: c.Candidate.Inputs, Verdict: c.Candidate.Verdict,
		State: c.State, Dataset: c.Measurement.Dataset, DeviceArchitecture: c.TargetNode.Architecture,
		DeviceNodeSelector: map[string]string{"edge.dacs.io/device-class": c.State.DeviceClass},
		DevicePowerModes:   []string{c.State.PowerMode}, RuntimeImage: c.Images.Runtime,
		ArtifactImage: c.Images.Artifact, ArtifactArchitecture: c.TargetNode.Architecture,
		MetricObserverImage: c.Images.MetricObserver,
	}, nil
}

func (p *cadenceAuthorityPorts) ResolveNode(context.Context, profiler.NodeSelection) (profilingrun.NodeSnapshot, error) {
	return p.config.TargetNode, nil
}

func (p *cadenceAuthorityPorts) ResolveMetricSource(context.Context, string) (profiler.ResolvedMetricSource, error) {
	c := p.config.Metrics
	return profiler.ResolvedMetricSource{
		Kind: c.SourceKind, Endpoint: c.Endpoint, PowerMetric: c.PowerMetric, MetricLabels: c.MetricLabels,
		ThermalEndpoint: c.ThermalEndpoint, ThermalMetrics: c.ThermalMetrics,
	}, nil
}

func (p *cadenceAuthorityPorts) ResolveWorkloadObservation(context.Context) (profiler.ResolvedWorkloadObservation, error) {
	return profiler.ResolvedWorkloadObservation{Image: p.config.Images.WorkloadObserver}, nil
}

func (p *cadenceAuthorityPorts) ResolveMeasurementWorkload(
	context.Context, string, profiler.CandidateSelection,
) (profilingrun.MeasurementContract, error) {
	return p.config.Measurement, p.workloadError
}

func (*cadenceAuthorityPorts) ResolveActuationPolicy(
	context.Context, profiler.ActuationSelection,
) (profiler.FrozenActuationPolicy, error) {
	return profiler.FrozenActuationPolicy{}, errors.New("unexpected fixed-node actuation")
}

func (p *cadenceAuthorityPorts) Resolve(
	_ context.Context, config profilingrun.RunConfig,
) (profiler.FrozenExecutionPolicy, error) {
	return profiler.FreezeConfiguredExecutionPolicy(p.catalog, config, profiler.ExecutionPolicySource{
		Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: "chill-system", Name: "chill-profiler-facts",
		UID: "facts-uid", ResourceVersion: "42", Key: "execution-policies.yaml",
	})
}

func TestCadenceAuthorityRejectsStandaloneBodyAndPreservesFrozenChild(t *testing.T) {
	config := currentPreparedRun(t).RunConfig
	report, err := os.ReadFile("../testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	ports := &cadenceAuthorityPorts{config: config, report: report, catalog: controllerExecutionPolicyCatalog(t)}
	noTrace := profiler.MissingServingTrace("no actual serving trace exists")
	preparer, err := profiler.NewPreparer(ports, ports, ports, ports, ports, ports,
		func(context.Context, profilingrun.RunConfig) (map[int]*firstfloor.CadenceContract, error) {
			return nil, noTrace
		})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewRunConfigResolver(preparer, ports)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), testRun()); !errors.Is(err, noTrace) {
		t.Fatalf("standalone must resolve the actual serving source: %v", err)
	}

	fake := fixtures.Cadence()
	fake.Target.LoadContext = "fabricated-context"
	for i := range fake.Provenance.Sources {
		fake.Provenance.Sources[i].LoadContext = fake.Target.LoadContext
		fake.Provenance.Sources[i].Object.Key = "never-created-trace-object"
	}
	for i := range fake.Provenance.Phases {
		for j := range fake.Provenance.Phases[i].Elements {
			fake.Provenance.Phases[i].Elements[j].GapNanos = 0
		}
	}
	for _, claimedUID := range []string{"", config.Measurement.WorkloadUID} {
		run := testRun()
		run.Spec.Measurement.Cadence = cadenceAuthorityJSON(t, fake)
		if claimedUID != "" {
			freezeAuthorityWorkload(run, config.Measurement)
		}
		if _, err := resolver.Resolve(context.Background(), run); err == nil ||
			!strings.Contains(err.Error(), "standalone ProfilingRun cannot supply cadence") {
			t.Fatalf("unowned supplied cadence must be rejected even with a claimed frozen UID: %v", err)
		}
	}

	child := testRun()
	child.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: edge.GroupVersion.String(), Kind: "BatchEnergyOptimization", Name: "frozen-plan",
		UID: "plan-uid", Controller: ptr.To(true),
	}}
	freezeAuthorityWorkload(child, config.Measurement)
	child.Spec.Measurement.Cadence = cadenceAuthorityJSON(t, config.Measurement.Cadence)
	ports.workloadError = errors.New("live Workload was deleted after plan freeze")
	if _, err := resolver.Resolve(context.Background(), child); err != nil {
		t.Fatalf("owned frozen child must remain executable without live trace or Workload resolution: %v", err)
	}
}

func cadenceAuthorityJSON(t *testing.T, cadence *firstfloor.CadenceContract) *apiextensions.JSON {
	t.Helper()
	body, err := json.Marshal(cadence)
	if err != nil {
		t.Fatal(err)
	}
	return &apiextensions.JSON{Raw: body}
}

func freezeAuthorityWorkload(run *edge.ProfilingRun, measurement profilingrun.MeasurementContract) {
	run.Spec.Measurement.WorkloadUID = measurement.WorkloadUID
	run.Spec.Measurement.WorkloadResourceVersion = measurement.WorkloadResourceVersion
	run.Spec.Measurement.SLOPercentile = measurement.SLOPercentile
	run.Spec.Measurement.SLOTargetLatencyNanos = measurement.SLOTargetLatencyNanos
	run.Spec.Measurement.MinServiceSamples = int32(measurement.MinServiceSamples)
}
