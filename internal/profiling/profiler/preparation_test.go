package profiler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestPreparerUpgradesProvenFirstPathIntent(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	resolvers := fixtureResolvers(expected, candidateBytes)
	preparer := newFixturePreparer(t, resolvers)

	prepared, err := preparer.Prepare(context.Background(), fixtureRequest(expected))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.Intent, expected) {
		t.Fatalf("prepared intent drifted from proven first path\nwant: %#v\n got: %#v", expected, prepared.Intent)
	}
	if err := profilingrun.ValidateCandidateReport(prepared.CandidateReportBytes, prepared.Intent); err != nil {
		t.Fatalf("prepared Run lost candidate-report semantics: %v", err)
	}
	if resolvers.metrics.nodeName != expected.TargetNode.Name {
		t.Fatal("metric source was not resolved from the selected Node")
	}
	if resolvers.node.selection.Name != expected.TargetNode.Name ||
		resolvers.node.selection.DeviceArchitecture != expected.TargetNode.Architecture ||
		resolvers.node.selection.DeviceClassSelector["edge.dacs.io/device-class"] != expected.State.DeviceClass {
		t.Fatalf("Node selection lost resolved DeviceClass facts: %#v", resolvers.node.selection)
	}
}

func TestPreparerFreezesExplicitRKNNLitePathWithoutORTSettings(t *testing.T) {
	expected, candidateBytes := rknnPreparationFixture(t)
	resolvers := fixtureResolvers(expected, candidateBytes)
	preparer := newFixturePreparer(t, resolvers)

	prepared, err := preparer.Prepare(context.Background(), fixtureRequest(expected))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.Intent, expected) {
		t.Fatalf("prepared RKNNLite intent drifted\nwant: %#v\n got: %#v", expected, prepared.Intent)
	}
	if prepared.Intent.CPU.ORTIntraOpThreads != 0 || prepared.Intent.CPU.ORTInterOpThreads != 0 ||
		!profilingrun.IsSupportedRKNNLiteIntent(prepared.Intent) {
		t.Fatalf("RKNNLite worker CPU contract drifted: %#v", prepared.Intent.CPU)
	}
	if prepared.Intent.Candidate.ExecutionPath != "rknnlite-bs2" || prepared.Intent.Measurement.Batch != 2 {
		t.Fatalf("artifact-specific path or batch drifted: %#v", prepared.Intent)
	}
}

func TestPreparerFreezesSelectedActuatorAndConfiguredPolicy(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	target := profilingrun.ActuatorTarget{
		Kind:           profilingrun.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 1},
	}
	expected.State.PowerMode = "15W"
	report, err := profilingrun.DecodeCandidateReport(candidateBytes)
	if err != nil {
		t.Fatal(err)
	}
	report.SchemaVersion = profilingrun.CandidateReportSchemaVersion
	report.Selection.PowerMode = expected.State.PowerMode
	report.Selection.Actuator = &target
	report.DeviceClassFacts.PowerModes = []string{profilingrun.FixedPowerMode, expected.State.PowerMode}
	candidateBytes, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}

	resolvers := fixtureResolvers(expected, candidateBytes)
	resolvers.candidate.result.SelectedActuator = &target
	resolvers.candidate.result.DevicePowerModes = append([]string(nil), report.DeviceClassFacts.PowerModes...)
	selection := ActuationSelection{
		DeviceClass: expected.State.DeviceClass, PowerMode: expected.State.PowerMode, Actuator: target,
	}
	policySpec := ActuationPolicySpec{
		Name: "lattepanda-15w-v1", DeviceClass: selection.DeviceClass,
		PowerMode: selection.PowerMode, ActuatorKind: selection.Actuator.Kind,
		SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
	}
	resolvers.actuation.result, err = FreezeConfiguredActuationPolicy(
		testActuationPolicyCatalog(t, policySpec),
		selection,
		testActuationPolicySource("1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := newFixturePreparer(t, resolvers).Prepare(
		context.Background(),
		fixtureRequest(expected),
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Intent.Actuation == nil || prepared.ActuationPolicy == nil ||
		prepared.Intent.Actuation.PolicyName != resolvers.actuation.result.Policy.Name ||
		prepared.Intent.Actuation.Actuator.JetsonNVPModel.ID != 1 ||
		resolvers.actuation.calls != 1 || !reflect.DeepEqual(resolvers.actuation.selection, selection) {
		t.Fatalf("prepared actuation binding = %#v policy=%#v", prepared.Intent.Actuation, prepared.ActuationPolicy)
	}
	if err := prepared.ValidateActuationBinding(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparerIsDeterministicAndOwnsResolvedBytes(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	resolvers := fixtureResolvers(expected, candidateBytes)
	preparer := newFixturePreparer(t, resolvers)
	request := fixtureRequest(expected)

	first, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("identical resolved facts produced different frozen Runs")
	}
	resolvers.candidate.result.ReportBytes[0] = 'x'
	if first.CandidateReportBytes[0] == 'x' {
		t.Fatal("prepared Run retained resolver-owned candidate bytes")
	}
	resolvers.workloads.result.Image = "changed"
	if first.Intent.Images.WorkloadObserver == "changed" {
		t.Fatal("prepared Run retained resolver-owned workload facts")
	}
}

func TestPreparerRejectsResolverDrift(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	tests := []struct {
		name   string
		mutate func(*fakePreparationResolvers)
		match  string
	}{
		{
			name: "candidate catalog mismatch",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.State.Model = "other-model"
			},
			match: "does not match requested catalog objects",
		},
		{
			name: "candidate power mode mismatch",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.State.PowerMode = "other-mode"
			},
			match: "power mode does not match request",
		},
		{
			name: "power mode absent from DeviceClass",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.DevicePowerModes = []string{"other-mode"}
			},
			match: "does not retain the selected power mode",
		},
		{
			name: "DeviceClass power mode report drift",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.DevicePowerModes = []string{"fixed", "15W"}
			},
			match: "candidate report DeviceClass powerModes mismatch",
		},
		{
			name: "missing DeviceClass selector",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.DeviceNodeSelector = nil
			},
			match: "architecture and Node selector are required",
		},
		{
			name: "candidate report mismatch",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.RuntimeImage = "registry.example/runtime@sha256:" + strings.Repeat("f", 64)
			},
			match: "candidate runtime image mismatch",
		},
		{
			name: "Node identity mismatch",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.node.result.Name = "other-node"
			},
			match: "does not match requested target",
		},
		{
			name: "invalid metric source",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.metrics.result.Endpoint = ""
			},
			match: "metrics.endpoint is required",
		},
		{
			name: "unsupported runtime path",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.State.Backend = "CUDAExecutionProvider"
			},
			match: "runtime/backend contract is not supported",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvers := fixtureResolvers(expected, candidateBytes)
			test.mutate(resolvers)
			preparer := newFixturePreparer(t, resolvers)
			_, err := preparer.Prepare(context.Background(), fixtureRequest(expected))
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestPreparerRejectsInternalReportMaterialDriftBeforeNodeResolution(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	tests := []struct {
		name   string
		mutate func(*ResolvedCandidate)
		match  string
	}{
		{
			name: "DeviceClass selector drift",
			mutate: func(candidate *ResolvedCandidate) {
				candidate.DeviceNodeSelector = map[string]string{"edge.dacs.io/device-class": "other"}
			},
			match: "DeviceClass nodeSelector mismatch",
		},
		{
			name: "artifact image drift",
			mutate: func(candidate *ResolvedCandidate) {
				candidate.ArtifactImage = "registry.example/artifact@sha256:" + strings.Repeat("f", 64)
			},
			match: "artifact image mismatch",
		},
		{
			name: "artifact path drift",
			mutate: func(candidate *ResolvedCandidate) {
				candidate.ArtifactPath = "/artifact/other.onnx"
			},
			match: "does not match execution state",
		},
		{
			name: "artifact path report drift",
			mutate: func(candidate *ResolvedCandidate) {
				candidate.State.ArtifactPath = "/artifact/other.onnx"
				candidate.ArtifactPath = candidate.State.ArtifactPath
			},
			match: "artifact path mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvers := fixtureResolvers(expected, candidateBytes)
			promoteFixtureCandidateReport(t, resolvers.candidate.result.ReportBytes, &resolvers.candidate.result)
			test.mutate(&resolvers.candidate.result)
			preparer := newFixturePreparer(t, resolvers)
			_, err := preparer.Prepare(context.Background(), fixtureRequest(expected))
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
			if resolvers.node.calls != 0 {
				t.Fatal("material drift reached Node resolution")
			}
		})
	}
}

func promoteFixtureCandidateReport(t *testing.T, payload []byte, candidate *ResolvedCandidate) {
	t.Helper()
	report, err := profilingrun.DecodeCandidateReport(payload)
	if err != nil {
		t.Fatal(err)
	}
	report.SchemaVersion = profilingrun.CandidateReportSchemaVersion
	report.Inputs.ModelSpec.UID, report.Inputs.ModelSpec.ResourceVersion = "model-uid", "1"
	report.Inputs.DeviceClass.UID, report.Inputs.DeviceClass.ResourceVersion = "device-uid", "1"
	report.Inputs.RuntimeDeclaration.Namespace = "chill-system"
	report.Inputs.RuntimeDeclaration.Name = "chill-profiler-facts"
	report.Inputs.RuntimeDeclaration.UID = "facts-uid"
	report.Inputs.RuntimeDeclaration.ResourceVersion = "1"
	report.Inputs.RuntimeDeclaration.Key = "execution-facts.yaml"
	report.Selection.PowerMode = candidate.State.PowerMode
	report.DeviceClassFacts = profilingrun.CandidateReportDeviceClassFacts{
		Architecture: candidate.DeviceArchitecture,
		NodeSelector: map[string]string{"edge.dacs.io/device-class": candidate.State.DeviceClass},
		PowerModes:   []string{candidate.State.PowerMode},
	}
	candidate.ArtifactArchitecture = candidate.DeviceArchitecture
	candidate.ArtifactPath = "/artifact/model.onnx"
	candidate.State.ArtifactPath = candidate.ArtifactPath
	report.ArtifactSupply = profilingrun.CandidateReportArtifactSupply{
		Image: candidate.ArtifactImage, Architecture: candidate.ArtifactArchitecture,
		Path: candidate.ArtifactPath,
	}
	report.MetricObserverImage = candidate.MetricObserverImage
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	candidate.ReportBytes = encoded
}

func TestPreparerPreservesResolverFailureAndStops(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	tests := []struct {
		name      string
		fail      func(*fakePreparationResolvers, error)
		wantCalls [4]int
	}{
		{
			name: "candidate",
			fail: func(resolvers *fakePreparationResolvers, err error) {
				resolvers.candidate.err = err
			},
			wantCalls: [4]int{1, 0, 0, 0},
		},
		{
			name: "Node",
			fail: func(resolvers *fakePreparationResolvers, err error) {
				resolvers.node.err = err
			},
			wantCalls: [4]int{1, 1, 0, 0},
		},
		{
			name: "metric source",
			fail: func(resolvers *fakePreparationResolvers, err error) {
				resolvers.metrics.err = err
			},
			wantCalls: [4]int{1, 1, 1, 0},
		},
		{
			name: "workload observation",
			fail: func(resolvers *fakePreparationResolvers, err error) {
				resolvers.workloads.err = err
			},
			wantCalls: [4]int{1, 1, 1, 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := errors.New("resolver unavailable")
			resolvers := fixtureResolvers(expected, candidateBytes)
			test.fail(resolvers, want)
			preparer := newFixturePreparer(t, resolvers)
			_, err := preparer.Prepare(context.Background(), fixtureRequest(expected))
			if !errors.Is(err, want) {
				t.Fatalf("expected resolver cause, got %v", err)
			}
			gotCalls := [4]int{
				resolvers.candidate.calls,
				resolvers.node.calls,
				resolvers.metrics.calls,
				resolvers.workloads.calls,
			}
			if gotCalls != test.wantCalls {
				t.Fatalf("resolver calls after failure: want %v, got %v", test.wantCalls, gotCalls)
			}
		})
	}
}

func TestPreparerRejectsCPURequestAboveNodeCapacityBeforeMetricResolution(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	resolvers := fixtureResolvers(expected, candidateBytes)
	preparer := newFixturePreparer(t, resolvers)
	request := fixtureRequest(expected)
	request.CPURequest = "5"

	_, err := preparer.Prepare(context.Background(), request)
	if !errors.Is(err, ErrCPURequestExceedsAllocatable) {
		t.Fatalf("expected CPU capacity rejection, got %v", err)
	}
	if resolvers.candidate.calls != 1 || resolvers.node.calls != 1 || resolvers.metrics.calls != 0 ||
		resolvers.workloads.calls != 0 {
		t.Fatalf("unexpected resolver calls: candidate=%d node=%d metrics=%d workloads=%d",
			resolvers.candidate.calls, resolvers.node.calls, resolvers.metrics.calls, resolvers.workloads.calls)
	}
}

func TestPreparerRejectsInvalidRequestBeforeResolution(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(*PreparationRequest)
	}{
		{name: "zero batch", mutate: func(request *PreparationRequest) { request.Measurement.Batch = 0 }},
		{name: "invalid CPU request", mutate: func(request *PreparationRequest) {
			request.CPURequest = "not-a-quantity"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolvers := fixtureResolvers(expected, candidateBytes)
			preparer := newFixturePreparer(t, resolvers)
			request := fixtureRequest(expected)
			test.mutate(&request)
			if _, err := preparer.Prepare(context.Background(), request); err == nil {
				t.Fatal("expected invalid measurement rejection")
			}
			if resolvers.candidate.calls != 0 || resolvers.node.calls != 0 || resolvers.metrics.calls != 0 ||
				resolvers.workloads.calls != 0 || resolvers.actuation.calls != 0 {
				t.Fatal("invalid caller input reached resolver ports")
			}
		})
	}
}

func newFixturePreparer(t *testing.T, resolvers *fakePreparationResolvers) *Preparer {
	t.Helper()
	preparer, err := NewPreparer(
		resolvers.candidate,
		resolvers.node,
		resolvers.metrics,
		resolvers.workloads,
		resolvers.actuation,
	)
	if err != nil {
		t.Fatal(err)
	}
	return preparer
}

func fixtureRequest(intent profilingrun.Intent) PreparationRequest {
	return PreparationRequest{
		Candidate: CandidateSelection{
			ModelSpec:     intent.State.Model,
			DeviceClass:   intent.State.DeviceClass,
			ExecutionPath: intent.Candidate.ExecutionPath,
			PowerMode:     intent.State.PowerMode,
		},
		TargetNode:  intent.TargetNode.Name,
		CPURequest:  intent.CPU.Request,
		Measurement: intent.Measurement,
		Metrics: MetricObservationPolicy{
			IntervalMilliseconds: intent.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  intent.Metrics.TimeoutMilliseconds,
			DurationSeconds:      intent.Metrics.DurationSeconds,
		},
	}
}

func preparationFixture(t *testing.T) (profilingrun.Intent, []byte) {
	t.Helper()
	intentBytes, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(intentBytes, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	intent.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	intent.Workload = profilingrun.CurrentWorkloadContract()
	candidateBytes, err := os.ReadFile("testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	return intent, candidateBytes
}

func rknnPreparationFixture(t *testing.T) (profilingrun.Intent, []byte) {
	t.Helper()
	intent, candidateBytes := preparationFixture(t)
	report, err := profilingrun.DecodeCandidateReport(candidateBytes)
	if err != nil {
		t.Fatal(err)
	}

	intent.Candidate.ExecutionPath = "rknnlite-bs2"
	intent.State.DeviceClass = "orangepi-5-plus-16g"
	intent.State.Artifact = "mobilenet-v2-050-rk3588-bs2"
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050-rk3588-bs2.rknn"
	intent.State.RuntimeFamily = profilingrun.RKNNLiteRuntimeFamily
	intent.State.Backend = profilingrun.RKNNLiteBackend
	intent.TargetNode.Name = "orangepi5plus"
	intent.TargetNode.Architecture = "arm64"
	intent.TargetNode.AllocatableCPU = "8"
	intent.CPU.Limit = "8"
	intent.CPU.ORTIntraOpThreads = 0
	intent.CPU.ORTInterOpThreads = 0
	intent.Metrics.NodeName = intent.TargetNode.Name
	intent.Measurement.Batch = 2

	host := profilingrun.CandidateReportHostContract{
		Libraries: []profilingrun.CandidateReportHostLibrary{{
			Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
		}},
		Driver: profilingrun.CandidateReportHostDriver{
			Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6",
		},
		Devices: []profilingrun.CandidateReportHostDevice{
			{Path: "/dev/dri/card0"}, {Path: "/dev/dri/card1"},
			{Path: "/dev/dri/renderD128"}, {Path: "/dev/dri/renderD129"},
		},
		Platform: &profilingrun.CandidateReportHostPlatform{
			CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588",
		},
	}
	intent.State.HostContract = &host
	report.SchemaVersion = profilingrun.CandidateReportSchemaVersion
	report.Inputs.ModelSpec.Name = intent.State.Model
	report.Inputs.DeviceClass.Name = intent.State.DeviceClass
	report.Inputs.RuntimeDeclaration.Image = intent.Images.Runtime
	report.Selection = profilingrun.CandidateReportSelection{
		ExecutionPath: intent.Candidate.ExecutionPath, Artifact: intent.State.Artifact,
		PowerMode: intent.State.PowerMode,
	}
	report.DeviceClassFacts = profilingrun.CandidateReportDeviceClassFacts{
		Architecture: intent.TargetNode.Architecture,
		NodeSelector: map[string]string{"edge.dacs.io/device-class": intent.State.DeviceClass},
		PowerModes:   []string{intent.State.PowerMode},
	}
	report.ArtifactSupply = profilingrun.CandidateReportArtifactSupply{
		Image: intent.Images.Artifact, Architecture: intent.TargetNode.Architecture,
		Path: intent.State.ArtifactPath,
	}
	report.ArtifactCapability = &profilingrun.CandidateArtifactCapability{
		TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{2}, InputSampleShape: []int32{224, 224, 3},
		InputLayout: profilingrun.RKNNLiteInputLayout, InputDataType: profilingrun.RKNNLiteInputDataType,
	}
	report.RuntimePackages = []profilingrun.CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}}
	report.HostContract = &host
	report.RuntimeAccess = &profilingrun.CandidateReportRuntimeAccess{
		Privileged: true, SupplementalGroups: []int64{44, 110},
	}
	report.MetricObserverImage = intent.Images.MetricObserver
	candidateBytes, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	intent.Candidate.Inputs = report.Inputs
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent, candidateBytes
}

type fakePreparationResolvers struct {
	candidate *fakeCandidateResolver
	node      *fakeNodeResolver
	metrics   *fakeMetricResolver
	workloads *fakeWorkloadObservationResolver
	actuation *fakeActuationPolicyResolver
}

func fixtureResolvers(intent profilingrun.Intent, report []byte) *fakePreparationResolvers {
	return &fakePreparationResolvers{
		candidate: &fakeCandidateResolver{result: ResolvedCandidate{
			ReportBytes:        append([]byte(nil), report...),
			Inputs:             intent.Candidate.Inputs,
			Verdict:            intent.Candidate.Verdict,
			State:              intent.State,
			DeviceArchitecture: intent.TargetNode.Architecture,
			DeviceNodeSelector: map[string]string{
				"edge.dacs.io/device-class": intent.State.DeviceClass,
			},
			DevicePowerModes:     []string{intent.State.PowerMode},
			RuntimeImage:         intent.Images.Runtime,
			ArtifactImage:        intent.Images.Artifact,
			ArtifactArchitecture: intent.TargetNode.Architecture,
			ArtifactPath:         intent.State.ArtifactPath,
			MetricObserverImage:  intent.Images.MetricObserver,
		}},
		node: &fakeNodeResolver{result: intent.TargetNode},
		metrics: &fakeMetricResolver{result: ResolvedMetricSource{
			Kind: intent.Metrics.SourceKind, Endpoint: intent.Metrics.Endpoint, PowerMetric: intent.Metrics.PowerMetric,
			ThermalMetrics: intent.Metrics.ThermalMetrics,
		}},
		workloads: &fakeWorkloadObservationResolver{result: ResolvedWorkloadObservation{
			Image: intent.Images.WorkloadObserver,
		}},
		actuation: &fakeActuationPolicyResolver{},
	}
}

type fakeCandidateResolver struct {
	result ResolvedCandidate
	err    error
	calls  int
}

func (r *fakeCandidateResolver) ResolveCandidate(
	_ context.Context,
	_ CandidateSelection,
) (ResolvedCandidate, error) {
	r.calls++
	return r.result, r.err
}

type fakeNodeResolver struct {
	result    profilingrun.NodeSnapshot
	err       error
	calls     int
	selection NodeSelection
}

func (r *fakeNodeResolver) ResolveNode(
	_ context.Context,
	selection NodeSelection,
) (profilingrun.NodeSnapshot, error) {
	r.calls++
	r.selection = selection
	return r.result, r.err
}

type fakeMetricResolver struct {
	result   ResolvedMetricSource
	err      error
	calls    int
	nodeName string
}

type fakeWorkloadObservationResolver struct {
	result ResolvedWorkloadObservation
	err    error
	calls  int
}

type fakeActuationPolicyResolver struct {
	result    FrozenActuationPolicy
	err       error
	calls     int
	selection ActuationSelection
}

func (r *fakeActuationPolicyResolver) ResolveActuationPolicy(
	_ context.Context,
	selection ActuationSelection,
) (FrozenActuationPolicy, error) {
	r.calls++
	r.selection = selection
	return r.result, r.err
}

func (r *fakeWorkloadObservationResolver) ResolveWorkloadObservation(
	context.Context,
) (ResolvedWorkloadObservation, error) {
	r.calls++
	return r.result, r.err
}

func (r *fakeMetricResolver) ResolveMetricSource(_ context.Context, nodeName string) (ResolvedMetricSource, error) {
	r.calls++
	r.nodeName = nodeName
	return r.result, r.err
}
