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
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const testARM64Architecture = "arm64"

func TestPreparerFirstMeasurementWithoutArchive(t *testing.T) {
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
	if prepared.RunConfig.Measurement.WorkloadUID != expected.Measurement.WorkloadUID ||
		prepared.RunConfig.Measurement.SLOPercentile != 95 || prepared.RunConfig.Measurement.MinServiceSamples != 59 {
		t.Fatal("first preparation lost actual Workload identity or SLO-derived known-gap sample stop")
	}
	if resolvers.node.selection.Name != expected.TargetNode.Name ||
		resolvers.node.selection.DeviceArchitecture != expected.TargetNode.Architecture ||
		resolvers.node.selection.DeviceClassSelector["edge.dacs.io/device-class"] != expected.State.DeviceClass {
		t.Fatalf("Node selection lost resolved DeviceClass facts: %#v", resolvers.node.selection)
	}
}

func TestPreparerAdmitsMetricScheduleWithinSelectedExecutionPolicy(t *testing.T) {
	for _, test := range []struct {
		name     string
		duration int
		idle     bool
		rejected bool
	}{
		{name: "active request", duration: 31},
		{name: "idle schedule", duration: 1172, idle: true},
		{name: "oversized active schedule", duration: 2027, rejected: true},
		{name: "rasp5 active schedule", duration: 1792, rejected: true},
		{name: "latte active schedule", duration: 1970, rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, report := preparationFixture(t)
			config.Metrics.DurationSeconds = test.duration
			if test.idle {
				config.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
				config.LoadOriginNode, config.LoadOriginGatewayPod = "", nil
				config.Measurement.OperatingPoints = nil
				config.Measurement.WorkloadName = ""
				config.Measurement.WarmupIterations = 0
				config.Measurement.MinServiceSamples = 0
				config.Measurement.SLOPercentile = 0
				config.Measurement.SettlingDurationSeconds = 2
				config.Measurement.Dataset = nil
			}
			resolvers := fixtureResolvers(config, report)
			policy := testExecutionPolicySpec(config.Measurement.WorkloadMode)
			policy.AttemptTimeoutOverheadSeconds = 1235
			policy.ActiveDeadlineOverheadSeconds = 1265
			policy.ObserverReadyTimeoutSeconds = 1160
			policy.ObserverStartOverheadSeconds = 1160
			resolvers.executionPolicy = &policy
			wantDuration := test.duration
			if !test.idle {
				points := config.Measurement.OperatingPoints
				phaseFloor := points.PhaseDurationSeconds*(len(points.Utilizations)+2) + points.PostLoadDurationSeconds
				wantDuration = max(test.duration, phaseFloor+max(0, test.duration-config.Measurement.DurationSeconds))
			}
			prepared, err := newFixturePreparer(t, resolvers).Prepare(context.Background(), fixtureRequest(config))
			if test.rejected {
				if reason, terminal := TerminalResolutionReason(err); !terminal || reason != profilingrun.ResolutionReasonExecutionPolicyInvalid {
					t.Fatalf("oversized schedule classification = %q, terminal=%t, error=%v", reason, terminal, err)
				}
				var capacity *MetricCapacityError
				if !errors.As(err, &capacity) {
					t.Fatalf("oversized schedule lost deterministic capacity cause: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if prepared.RunConfig.Metrics.DurationSeconds != wantDuration ||
				prepared.RunConfig.Measurement.WorkloadMode != config.Measurement.WorkloadMode {
				t.Fatal("admission lost the named phase floor or selected observation margin")
			}
		})
	}
}

func TestPreparerKeepsExecutionPolicyReadFailureNonterminal(t *testing.T) {
	config, report := preparationFixture(t)
	resolvers := fixtureResolvers(config, report)
	readErr := errors.New("execution-policy store unavailable")
	resolvers.executionError = readErr
	_, err := newFixturePreparer(t, resolvers).Prepare(context.Background(), fixtureRequest(config))
	if !errors.Is(err, readErr) {
		t.Fatalf("policy read failure lost cause: %v", err)
	}
	if reason, terminal := TerminalResolutionReason(err); terminal {
		t.Fatalf("policy read failure became terminal: %q", reason)
	}
}

func TestPreparerFreezesRKNNArtifactInputs(t *testing.T) {
	expected, candidateBytes := rknnPreparationFixture(t)
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
}

func TestPreparerFreezesPreprocessorAndActiveDataset(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	prepared, err := newFixturePreparer(t, fixtureResolvers(expected, candidateBytes)).Prepare(
		context.Background(), fixtureRequest(expected),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.RunConfig.State.Preprocessor, expected.State.Preprocessor) ||
		!reflect.DeepEqual(prepared.RunConfig.Measurement.Dataset, expected.Measurement.Dataset) {
		t.Fatalf("preparation did not freeze the resolved preprocessor and dataset: %+v", prepared.RunConfig)
	}
	request := fixtureRequest(expected)
	request.Candidate.Dataset = ""
	if err := request.Validate(); err == nil {
		t.Fatal("active measurement request without a dataset was accepted")
	}
}

func TestPrepareSessionFreezesTheActorPrepareFreezes(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	request := fixtureRequest(expected)
	prepared, err := newFixturePreparer(t, fixtureResolvers(expected, candidateBytes)).Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resolvers := fixtureResolvers(expected, candidateBytes)
	session, err := newFixturePreparer(t, resolvers).PrepareSession(context.Background(), SessionRequest{
		Candidate: request.Candidate, Workload: request.Workload,
		WarmupIterations: request.Measurement.StartupWarmupIterations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(session.RunConfig.Actor, prepared.RunConfig.Actor) {
		t.Fatalf("session actor differs from Prepare\nwant: %#v\n got: %#v", prepared.RunConfig.Actor, session.RunConfig.Actor)
	}
	if !reflect.DeepEqual(session.RunConfig.Delivery, prepared.RunConfig.Delivery) ||
		!reflect.DeepEqual(session.RunConfig.Measurement.Dataset, prepared.RunConfig.Measurement.Dataset) ||
		session.RunConfig.Measurement.SLOTargetLatencyNanos != prepared.RunConfig.Measurement.SLOTargetLatencyNanos {
		t.Fatalf("session lost the delivery, dataset or SLO: %+v", session.RunConfig)
	}
	if len(session.PlannedBatchSizes) == 0 || session.PlannedBatchSizes[0] != 1 {
		t.Fatalf("planned batch sizes = %v", session.PlannedBatchSizes)
	}
	if resolvers.metrics.calls != 0 || resolvers.workloads.calls != 0 || resolvers.actuation.calls != 0 {
		t.Fatal("session resolved a metric source, workload observer or actuation policy")
	}
}

func TestPreparerFreezesSharedExecutionForORTAndRKNNLite(t *testing.T) {
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
			request := fixtureRequest(expected)

			prepared, err := newFixturePreparer(t, fixtureResolvers(expected, candidateBytes)).Prepare(
				context.Background(), request,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(prepared.RunConfig, expected) {
				t.Fatalf("shared execution config drifted\nwant: %#v\n got: %#v", expected, prepared.RunConfig)
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
	if !profilingrun.IsSupportedFirstfloorRunConfig(prepared.RunConfig) {
		t.Fatalf("RKNNLite worker runtime contract drifted: %#v", prepared.RunConfig)
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
	policySpec := nodeactuator.ActuationPolicySpec{Name: "lattepanda-15w-v1", DeviceClass: selection.DeviceClass,
		PowerMode: selection.PowerMode, ActuatorKind: selection.Actuator.Kind,
		SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300}
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
			name: "missing DeviceClass selector",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.DeviceNodeSelector = nil
			},
			match: "architecture and Node selector are required",
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
		{
			name: "preprocessor architecture differs from target Node",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.State.Preprocessor.Architecture = testARM64Architecture
			},
			match: "preprocessor artifact architecture",
		},
		{
			name: "candidate preprocessor mismatch",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.State.Preprocessor.Name = "other-preprocessor"
			},
			match: "preprocessor does not match request",
		},
		{
			name: "candidate dataset missing",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.Dataset = nil
			},
			match: "dataset does not match request",
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

func TestPreparerDoesNotRequireNodeCPUCapacity(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	expected.TargetNode.AllocatableCPU = ""
	resolvers := fixtureResolvers(expected, candidateBytes)
	prepared, err := newFixturePreparer(t, resolvers).Prepare(context.Background(), fixtureRequest(expected))
	if err != nil {
		t.Fatalf("shared preparation was blocked by absent allocatable CPU: %v", err)
	}
	if prepared.RunConfig.TargetNode.AllocatableCPU != "" || resolvers.metrics.calls != 1 || resolvers.workloads.calls != 1 {
		t.Fatal("shared execution inferred CPU allocation or skipped observation resolution")
	}
}

func TestPreparerRejectsInvalidRequestBeforeResolution(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(*PreparationRequest)
	}{
		{name: "zero batch", mutate: func(request *PreparationRequest) { request.Measurement.Batch = 0 }},
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
		resolvers,
		func(context.Context, string) (string, profilingrun.LoadOriginGatewayPodContract, error) {
			return "gateway-node", profilingrun.LoadOriginGatewayPodContract{Namespace: "chill-system", Name: "serving-gateway", UID: "gateway-pod-uid"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return preparer
}

func fixtureRequest(config profilingrun.RunConfig) PreparationRequest {
	measurement := config.Measurement
	measurement.Dataset = nil
	measurement.WorkloadName = ""
	measurement.WorkloadUID = ""
	measurement.WorkloadResourceVersion = ""
	measurement.SLOPercentile = 0
	measurement.SLOTargetLatencyNanos = 0
	measurement.MinServiceSamples = 0
	request := PreparationRequest{
		Candidate: CandidateSelection{
			ModelSpec:     config.State.Model,
			DeviceClass:   config.State.DeviceClass,
			Node:          config.TargetNode.Name,
			ExecutionPath: config.Candidate.ExecutionPath,
			PowerMode:     config.State.PowerMode,
			Preprocessor:  config.State.Preprocessor.Name,
		},
		TargetNode:  config.TargetNode.Name,
		Workload:    config.Measurement.WorkloadName,
		Measurement: measurement,
		Metrics: MetricObservationPolicy{
			IntervalMilliseconds: config.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  config.Metrics.TimeoutMilliseconds,
			DurationSeconds:      config.Metrics.DurationSeconds,
		},
	}
	if config.Measurement.Dataset != nil {
		request.Candidate.Dataset = config.Measurement.Dataset.Name
	}
	return request
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
	config.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050.onnx"}
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	config.Actor = config.ActorConfig()
	config.Actor.MemoryRequestBytes = 128 << 20
	config.Actor.MemoryLimitBytes = 1 << 30
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
	config.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050-rk3588-bs2.rknn"}
	config.State.RuntimeFamily = profilingrun.FirstfloorRuntimeFamily
	config.State.Backend = profilingrun.FirstfloorRKNNLiteBackend
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	config.TargetNode.Name = "orangepi5plus"
	config.TargetNode.Architecture = testARM64Architecture
	config.State.Preprocessor.Architecture = testARM64Architecture
	config.TargetNode.AllocatableCPU = "8"
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
		Files: config.State.ArtifactFiles,
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
	config.Actor = config.ActorConfig()
	config.Actor.MemoryRequestBytes = 128 << 20
	config.Actor.MemoryLimitBytes = 1 << 30
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config, candidateBytes
}

type fakePreparationResolvers struct {
	candidate       *fakeCandidateResolver
	node            *fakeNodeResolver
	metrics         *fakeMetricResolver
	workloads       *fakeWorkloadObservationResolver
	actuation       *fakeActuationPolicyResolver
	executionPolicy *ExecutionPolicySpec
	executionError  error
}

func fixtureResolvers(config profilingrun.RunConfig, report []byte) *fakePreparationResolvers {
	return &fakePreparationResolvers{
		candidate: &fakeCandidateResolver{result: ResolvedCandidate{
			ReportBytes:        append([]byte(nil), report...),
			Delivery:           config.Delivery,
			Inputs:             config.Candidate.Inputs,
			Verdict:            config.Candidate.Verdict,
			State:              config.State,
			Dataset:            config.Measurement.Dataset,
			DeviceArchitecture: config.TargetNode.Architecture,
			DeviceNodeSelector: map[string]string{
				"edge.dacs.io/device-class": config.State.DeviceClass,
			},
			DevicePowerModes:     []string{config.State.PowerMode},
			RuntimeImage:         config.Images.Runtime,
			ArtifactImage:        config.Images.Artifact,
			ArtifactArchitecture: config.TargetNode.Architecture,
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
		}, measurement: config.Measurement},
		actuation: &fakeActuationPolicyResolver{},
	}
}

func (r *fakePreparationResolvers) Resolve(_ context.Context, config profilingrun.RunConfig) (FrozenExecutionPolicy, error) {
	policy := testExecutionPolicySpec(config.Measurement.WorkloadMode)
	policy.ExecutionPath = config.Candidate.ExecutionPath
	if r.executionPolicy != nil {
		policy = *r.executionPolicy
	}
	if r.executionError != nil {
		return FrozenExecutionPolicy{}, r.executionError
	}
	return FrozenExecutionPolicy{SchemaVersion: FrozenExecutionPolicySchemaVersion, Source: validExecutionPolicySource(),
		Policy: ExecutionPolicy{SchemaVersion: ExecutionPolicySchemaVersion, ExecutionPolicySpec: policy}}, nil
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
	result      ResolvedWorkloadObservation
	err         error
	calls       int
	measurement profilingrun.MeasurementContract
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

func (r *fakeWorkloadObservationResolver) ResolveMeasurementWorkload(
	context.Context, string, CandidateSelection,
) (profilingrun.MeasurementContract, error) {
	return r.measurement, nil
}

func (r *fakeMetricResolver) ResolveMetricSource(_ context.Context, nodeName string) (ResolvedMetricSource, error) {
	r.calls++
	r.nodeName = nodeName
	return r.result, r.err
}
