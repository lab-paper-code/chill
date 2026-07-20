package profiler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profilingrun"
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
	wantDigest, err := expected.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if prepared.IntentDigest != wantDigest || string(prepared.CandidateReportBytes) != string(candidateBytes) {
		t.Fatal("prepared Run lost intent or candidate-report identity")
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
			match: "not the first-path Fixed actuator class",
		},
		{
			name: "selectable mode mixed with fixed",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.DevicePowerModes = []string{"fixed", "15W"}
			},
			match: "not the first-path Fixed actuator class",
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
			name: "missing metric source configuration identity",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.metrics.result.ConfigurationContentDigest = ""
			},
			match: "configuration digest must be a canonical SHA-256 digest",
		},
		{
			name: "unsupported runtime path",
			mutate: func(resolvers *fakePreparationResolvers) {
				resolvers.candidate.result.State.Backend = "CUDAExecutionProvider"
			},
			match: "not the supported CPU ONNX Runtime path",
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
	candidate.ArtifactDigest = candidate.State.ArtifactDigest
	report.ArtifactSupply = profilingrun.CandidateReportArtifactSupply{
		Image: candidate.ArtifactImage, Architecture: candidate.ArtifactArchitecture,
		Path: candidate.ArtifactPath, Digest: candidate.ArtifactDigest,
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
		wantCalls [3]int
	}{
		{
			name: "candidate",
			fail: func(resolvers *fakePreparationResolvers, err error) {
				resolvers.candidate.err = err
			},
			wantCalls: [3]int{1, 0, 0},
		},
		{
			name: "Node",
			fail: func(resolvers *fakePreparationResolvers, err error) {
				resolvers.node.err = err
			},
			wantCalls: [3]int{1, 1, 0},
		},
		{
			name: "metric source",
			fail: func(resolvers *fakePreparationResolvers, err error) {
				resolvers.metrics.err = err
			},
			wantCalls: [3]int{1, 1, 1},
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
			gotCalls := [3]int{resolvers.candidate.calls, resolvers.node.calls, resolvers.metrics.calls}
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
	if resolvers.candidate.calls != 1 || resolvers.node.calls != 1 || resolvers.metrics.calls != 0 {
		t.Fatalf("unexpected resolver calls: candidate=%d node=%d power=%d",
			resolvers.candidate.calls, resolvers.node.calls, resolvers.metrics.calls)
	}
}

func TestPreparerRejectsInvalidRequestBeforeResolution(t *testing.T) {
	expected, candidateBytes := preparationFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(*PreparationRequest)
	}{
		{name: "zero batch", mutate: func(request *PreparationRequest) { request.Measurement.Batch = 0 }},
		{name: "multiple repetitions", mutate: func(request *PreparationRequest) {
			request.Measurement.Repetitions = 2
		}},
		{name: "unsupported power mode", mutate: func(request *PreparationRequest) {
			request.Candidate.PowerMode = "15W"
		}},
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
			if resolvers.candidate.calls != 0 || resolvers.node.calls != 0 || resolvers.metrics.calls != 0 {
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
	intentBytes, err := os.ReadFile("../../spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(intentBytes, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	candidateBytes, err := os.ReadFile("../../spikes/profilingrun/fixtures/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	return intent, candidateBytes
}

type fakePreparationResolvers struct {
	candidate *fakeCandidateResolver
	node      *fakeNodeResolver
	metrics   *fakeMetricResolver
}

func fixtureResolvers(intent profilingrun.Intent, report []byte) *fakePreparationResolvers {
	return &fakePreparationResolvers{
		candidate: &fakeCandidateResolver{result: ResolvedCandidate{
			ReportBytes:                     append([]byte(nil), report...),
			ModelSpecContentDigest:          intent.Candidate.ModelSpecContentDigest,
			DeviceClassContentDigest:        intent.Candidate.DeviceClassContentDigest,
			RuntimeDeclarationContentDigest: intent.Candidate.RuntimeDeclarationDigest,
			Verdict:                         intent.Candidate.Verdict,
			State:                           intent.State,
			DeviceArchitecture:              intent.TargetNode.Architecture,
			DeviceNodeSelector: map[string]string{
				"edge.dacs.io/device-class": intent.State.DeviceClass,
			},
			DevicePowerModes:    []string{intent.State.PowerMode},
			RuntimeImage:        intent.Images.Runtime,
			ArtifactImage:       intent.Images.Artifact,
			ArtifactPath:        intent.State.ArtifactPath,
			ArtifactDigest:      intent.State.ArtifactDigest,
			MetricObserverImage: intent.Images.MetricObserver,
		}},
		node: &fakeNodeResolver{result: intent.TargetNode},
		metrics: &fakeMetricResolver{result: ResolvedMetricSource{
			Kind: intent.Metrics.SourceKind, Endpoint: intent.Metrics.Endpoint, PowerMetric: intent.Metrics.PowerMetric,
			ConfigurationContentDigest: intent.Metrics.ConfigurationContentDigest,
		}},
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

func (r *fakeMetricResolver) ResolveMetricSource(_ context.Context, nodeName string) (ResolvedMetricSource, error) {
	r.calls++
	r.nodeName = nodeName
	return r.result, r.err
}
