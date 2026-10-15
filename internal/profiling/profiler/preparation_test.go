package profiler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestPreparerUpgradesProvenFirstPathRunConfig(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	resolvers := fixtureResolvers(expected, candidateBytes)
	preparer := newFixturePreparer(t, resolvers)

	prepared, err := preparer.Prepare(context.Background(), fixtureRequest(expected))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.RunConfig, expected) {
		t.Fatalf("prepared config drifted from proven first path\nwant: %#v\n got: %#v", expected, prepared.RunConfig)
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

func TestPreparerFreezesNamedArtifactInputs(t *testing.T) {
	for _, path := range []string{"ort", "rknn"} {
		t.Run(path, func(t *testing.T) {
			expected, candidateBytes := preparationFixture(t)
			if path == "rknn" {
				expected, candidateBytes = rknnPreparationFixture(t)
			}
			prepared, err := newFixturePreparer(t, fixtureResolvers(expected, candidateBytes)).Prepare(
				context.Background(), fixtureRequest(expected),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(prepared.RunConfig.Delivery.ArtifactCapability.Tensors, expected.Delivery.ArtifactCapability.Tensors) {
				t.Fatal("preparation changed the artifact's named inputs")
			}
			for _, mutate := range []func([]modelcompat.InputTensor){
				func(inputs []modelcompat.InputTensor) { inputs[0].Name = "other" },
				func(inputs []modelcompat.InputTensor) { inputs[0].Datatype = "INT32" },
				func(inputs []modelcompat.InputTensor) { inputs[0].SampleShape[0]++ },
			} {
				report, err := profilingrun.DecodeCandidateReport(candidateBytes)
				if err != nil {
					t.Fatal(err)
				}
				mutate(report.ArtifactCapability.Tensors)
				payload, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				if err := profilingrun.ValidateCandidateReport(payload, prepared.RunConfig); err == nil {
					t.Fatal("candidate input contract drift did not invalidate the frozen Run")
				}
			}
		})
	}
}

func TestPreparerFreezesConfiguredQuotaForORTAndRKNNLite(t *testing.T) {
	for _, runtimePath := range []string{"ort", "rknn"} {
		t.Run(runtimePath, func(t *testing.T) {
			var expected profilingrun.RunConfig
			var candidateBytes []byte
			if runtimePath == "ort" {
				expected, candidateBytes = preparationFixture(t)
			} else {
				expected, candidateBytes = rknnPreparationFixture(t)
			}
			expected.TargetNode.AllocatableCPU = "12"
			expected.CPU.Policy = profilingrun.ConfiguredCPUQuotaPolicy
			expected.CPU.PolicyVersion = profilingrun.CPUQuotaPolicyVersion
			expected.CPU.Limit = "4"
			if runtimePath == "ort" {
				expected.CPU.ORTIntraOpThreads = 4
			}
			request := fixtureRequest(expected)
			request.CPUQuota = "4"

			prepared, err := newFixturePreparer(t, fixtureResolvers(expected, candidateBytes)).Prepare(
				context.Background(), request,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(prepared.RunConfig, expected) {
				t.Fatalf("configured CPU quota config drifted\nwant: %#v\n got: %#v", expected, prepared.RunConfig)
			}
		})
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
	if !reflect.DeepEqual(prepared.RunConfig, expected) {
		t.Fatalf("prepared RKNNLite config drifted\nwant: %#v\n got: %#v", expected, prepared.RunConfig)
	}
	if prepared.RunConfig.CPU.ORTIntraOpThreads != 0 || prepared.RunConfig.CPU.ORTInterOpThreads != 0 ||
		!profilingrun.IsSupportedFirstfloorRunConfig(prepared.RunConfig) {
		t.Fatalf("RKNNLite worker CPU contract drifted: %#v", prepared.RunConfig.CPU)
	}
	if prepared.RunConfig.Candidate.ExecutionPath != "rknnlite-bs2" || prepared.RunConfig.Measurement.Batch != 2 {
		t.Fatalf("artifact-specific path or batch drifted: %#v", prepared.RunConfig)
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
	if prepared.RunConfig.Actuation == nil || prepared.ActuationPolicy == nil ||
		prepared.RunConfig.Actuation.PolicyName != resolvers.actuation.result.Policy.Name ||
		prepared.RunConfig.Actuation.Actuator.JetsonNVPModel.ID != 1 ||
		resolvers.actuation.calls != 1 || !reflect.DeepEqual(resolvers.actuation.selection, selection) {
		t.Fatalf("prepared actuation binding = %#v policy=%#v", prepared.RunConfig.Actuation, prepared.ActuationPolicy)
	}
	if err := prepared.ValidateActuationBinding(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparerIsDeterministicAndOwnsResolvedBytes(t *testing.T) {
	const mutation = "changed"

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
		t.Fatal("identical resolved facts produced different RunConfigs")
	}
	resolvers.candidate.result.ReportBytes[0] = 'x'
	resolvers.workloads.result.Image = mutation
	if first.RunConfig.Images.WorkloadObserver == mutation {
		t.Fatal("prepared Run retained resolver-owned workload facts")
	}
	resolvers.metrics.result.MetricLabels["hostname"] = mutation
	if first.RunConfig.Metrics.MetricLabels["hostname"] == mutation {
		t.Fatal("prepared Run retained resolver-owned metric labels")
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

func TestPreparerRejectsCPUQuotaAboveNodeCapacityBeforeMetricResolution(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	resolvers := fixtureResolvers(expected, candidateBytes)
	request := fixtureRequest(expected)
	request.CPUQuota = "5"

	_, err := newFixturePreparer(t, resolvers).Prepare(context.Background(), request)
	if !errors.Is(err, ErrCPUQuotaExceedsAllocatable) {
		t.Fatalf("expected CPU quota capacity rejection, got %v", err)
	}
	if resolvers.metrics.calls != 0 || resolvers.workloads.calls != 0 {
		t.Fatal("CPU quota capacity rejection reached downstream resolvers")
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
		{name: "fractional CPU quota", mutate: func(request *PreparationRequest) {
			request.CPUQuota = "500m"
		}},
		{name: "CPU request above quota", mutate: func(request *PreparationRequest) {
			request.CPURequest, request.CPUQuota = "2", "1"
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

func fixtureRequest(config profilingrun.RunConfig) PreparationRequest {
	return PreparationRequest{
		Candidate: CandidateSelection{
			ModelSpec:     config.State.Model,
			DeviceClass:   config.State.DeviceClass,
			ExecutionPath: config.Candidate.ExecutionPath,
			PowerMode:     config.State.PowerMode,
		},
		TargetNode:  config.TargetNode.Name,
		CPURequest:  config.CPU.Request,
		Measurement: config.Measurement,
		Metrics: MetricObservationPolicy{
			IntervalMilliseconds: config.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  config.Metrics.TimeoutMilliseconds,
			DurationSeconds:      config.Metrics.DurationSeconds,
		},
	}
}

func preparationFixture(t *testing.T) (profilingrun.RunConfig, []byte) {
	t.Helper()
	intentBytes, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(intentBytes, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	candidateBytes, err := os.ReadFile("testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	return config, candidateBytes
}

func rknnPreparationFixture(t *testing.T) (profilingrun.RunConfig, []byte) {
	t.Helper()
	config, candidateBytes := preparationFixture(t)
	report, err := profilingrun.DecodeCandidateReport(candidateBytes)
	if err != nil {
		t.Fatal(err)
	}

	config.Candidate.ExecutionPath = "rknnlite-bs2"
	config.State.DeviceClass = "orangepi-5-plus-16g"
	config.State.Artifact = "mobilenet-v2-050-rk3588-bs2"
	config.State.ArtifactPath = "/artifact/mobilenet-v2-050-rk3588-bs2.rknn"
	config.State.RuntimeFamily = profilingrun.FirstfloorRuntimeFamily
	config.State.Backend = profilingrun.FirstfloorRKNNLiteBackend
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	config.TargetNode.Name = "orangepi5plus"
	config.TargetNode.Architecture = "arm64"
	config.TargetNode.AllocatableCPU = "8"
	config.CPU.Limit = "8"
	config.CPU.ORTIntraOpThreads = 0
	config.CPU.ORTInterOpThreads = 0
	config.Metrics.NodeName = config.TargetNode.Name
	config.Measurement.Batch = 2

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
	config.State.HostContract = &host
	report.SchemaVersion = profilingrun.CandidateReportSchemaVersion
	report.Inputs.ModelSpec.Name = config.State.Model
	report.Inputs.DeviceClass.Name = config.State.DeviceClass
	report.Inputs.RuntimeDeclaration.Image = config.Images.Runtime
	report.Selection = profilingrun.CandidateReportSelection{
		ExecutionPath: config.Candidate.ExecutionPath, Artifact: config.State.Artifact,
		PowerMode: config.State.PowerMode,
	}
	report.DeviceClassFacts = profilingrun.CandidateReportDeviceClassFacts{
		Architecture: config.TargetNode.Architecture,
		NodeSelector: map[string]string{"edge.dacs.io/device-class": config.State.DeviceClass},
		PowerModes:   []string{config.State.PowerMode},
	}
	report.ArtifactSupply = profilingrun.CandidateReportArtifactSupply{
		Image: config.Images.Artifact, Architecture: config.TargetNode.Architecture,
		Path: config.State.ArtifactPath,
	}
	report.ArtifactCapability = &profilingrun.CandidateArtifactCapability{
		TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{2},
		Tensors: []modelcompat.InputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}}},
	}
	report.RuntimePackages = []profilingrun.CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}}
	report.HostContract = &host
	report.RuntimeAccess = &profilingrun.CandidateReportRuntimeAccess{
		Privileged: true, SupplementalGroups: []int64{44, 110},
	}
	report.MetricObserverImage = config.Images.MetricObserver
	candidateBytes, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	config.Candidate.Inputs = report.Inputs
	config.Delivery = report.RuntimeDelivery()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config, candidateBytes
}

type fakePreparationResolvers struct {
	candidate *fakeCandidateResolver
	node      *fakeNodeResolver
	metrics   *fakeMetricResolver
	workloads *fakeWorkloadObservationResolver
	actuation *fakeActuationPolicyResolver
}

func fixtureResolvers(config profilingrun.RunConfig, report []byte) *fakePreparationResolvers {
	return &fakePreparationResolvers{
		candidate: &fakeCandidateResolver{result: ResolvedCandidate{
			ReportBytes:        append([]byte(nil), report...),
			Inputs:             config.Candidate.Inputs,
			Verdict:            config.Candidate.Verdict,
			State:              config.State,
			DeviceArchitecture: config.TargetNode.Architecture,
			DeviceNodeSelector: map[string]string{
				"edge.dacs.io/device-class": config.State.DeviceClass,
			},
			DevicePowerModes:     []string{config.State.PowerMode},
			RuntimeImage:         config.Images.Runtime,
			ArtifactImage:        config.Images.Artifact,
			ArtifactArchitecture: config.TargetNode.Architecture,
			ArtifactPath:         config.State.ArtifactPath,
			MetricObserverImage:  config.Images.MetricObserver,
		}},
		node: &fakeNodeResolver{result: config.TargetNode},
		metrics: &fakeMetricResolver{result: ResolvedMetricSource{
			Kind: config.Metrics.SourceKind, Endpoint: config.Metrics.Endpoint, PowerMetric: config.Metrics.PowerMetric,
			MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
			ThermalMetrics: config.Metrics.ThermalMetrics,
		}},
		workloads: &fakeWorkloadObservationResolver{result: ResolvedWorkloadObservation{
			Image: config.Images.WorkloadObserver,
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
