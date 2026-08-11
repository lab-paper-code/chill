package profilederivation

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestNewCurvePlanCarriesThePlannedBatchSizes(t *testing.T) {
	shared := validCurvePlanSpec()
	shared.PlannedBatchSizes = []int{4, 1, 2}

	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	got := contract.PlannedBatchSizes()
	if !reflect.DeepEqual(got, []int{1, 2, 4}) {
		t.Fatalf("planned batch sizes = %v, want sorted [1 2 4]", got)
	}
}

func TestNewCurvePlanRejectsAnEmptyPlannedBatchSizeSet(t *testing.T) {
	shared := validCurvePlanSpec()
	shared.PlannedBatchSizes = nil

	if _, err := NewCurvePlan(shared); err == nil {
		t.Fatal("expected an empty planned batch size set to be rejected")
	}
}

func TestNewCurvePlanRejectsInvalidArtifactAndPlannedBatchSizes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CurvePlanSpec)
	}{
		{name: "empty execution path", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.ExecutionPath = ""
		}},
		{name: "empty artifact name", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Name = ""
		}},
		{name: "empty artifact path", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Path = ""
		}},
		{name: "empty artifact image", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Image = ""
		}},
		{name: "root artifact path", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Path = "/"
		}},
		{name: "relative artifact path", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Path = "artifact/model.rknn"
		}},
		{name: "unclean artifact path", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Path = "/artifact/../model.rknn"
		}},
		{name: "non-positive batch", mutate: func(shared *CurvePlanSpec) {
			shared.PlannedBatchSizes = []int{0, 1}
		}},
		{name: "duplicate batch", mutate: func(shared *CurvePlanSpec) {
			shared.PlannedBatchSizes = []int{1, 2, 1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shared := validCurvePlanSpec()
			test.mutate(&shared)
			if _, err := NewCurvePlan(shared); err == nil {
				t.Fatal("expected invalid curve plan to be rejected")
			}
		})
	}
}

func TestNewCurvePlanAllowsAllOrNoConversionProvenance(t *testing.T) {
	withoutConverter := validCurvePlanSpec()
	withoutConverter.Conversion = CurveConversionContract{}
	if _, err := NewCurvePlan(withoutConverter); err != nil {
		t.Fatalf("construct converter-free plan: %v", err)
	}

	tests := []struct {
		name       string
		conversion CurveConversionContract
	}{
		{name: "target only", conversion: CurveConversionContract{TargetPlatform: "rk3588"}},
		{name: "tool only", conversion: CurveConversionContract{Tool: "rknn-toolkit2"}},
		{name: "version only", conversion: CurveConversionContract{ToolVersion: "2.3.2"}},
		{name: "target and tool", conversion: CurveConversionContract{TargetPlatform: "rk3588", Tool: "rknn-toolkit2"}},
		{name: "target and version", conversion: CurveConversionContract{TargetPlatform: "rk3588", ToolVersion: "2.3.2"}},
		{name: "tool and version", conversion: CurveConversionContract{Tool: "rknn-toolkit2", ToolVersion: "2.3.2"}},
		{name: "whitespace", conversion: CurveConversionContract{
			TargetPlatform: "rk3588", Tool: " ", ToolVersion: "2.3.2",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shared := validCurvePlanSpec()
			shared.Conversion = test.conversion
			if _, err := NewCurvePlan(shared); err == nil {
				t.Fatal("expected partial or invalid conversion provenance to be rejected")
			}
		})
	}
}

func TestConstructAndVerifyCurvePlan(t *testing.T) {
	shared := validCurvePlanSpec()
	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := contract.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCurvePlan(append([]byte(" \n"), canonical...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Shared(), contract.Shared()) ||
		!reflect.DeepEqual(decoded.PlannedBatchSizes(), contract.PlannedBatchSizes()) {
		t.Fatal("decoded plan lost configured semantics")
	}
	unknown := bytes.Replace(canonical, []byte(`"schemaVersion":`), []byte(`"unknown":1,"schemaVersion":`), 1)
	if _, err := DecodeCurvePlan(unknown); err == nil {
		t.Fatal("expected unknown plan field rejection")
	}
	if _, err := DecodeCurvePlan(append(canonical, []byte(` {}`)...)); err == nil {
		t.Fatal("expected trailing plan value rejection")
	}
	previousSchema := bytes.Replace(
		canonical,
		[]byte(curvePlanSchemaVersion),
		[]byte("chill.dacs.io/curve-family-contract.v1alpha3"),
		1,
	)
	if _, err := DecodeCurvePlan(previousSchema); err == nil {
		t.Fatal("expected previous curve-plan schema rejection")
	}

	// Inputs and projections are copies, not mutation paths into the initialized record.
	shared.PlannedBatchSizes[0] = 99
	shared.Tensor.InputSampleShape[0] = 99
	projected := contract.Shared()
	projected.PlannedBatchSizes[0] = 99
	projected.Tensor.InputSampleShape[0] = 99
	if !reflect.DeepEqual(contract.PlannedBatchSizes(), []int{1, 2, 4}) ||
		contract.Shared().Tensor.InputSampleShape[0] != 224 {
		t.Fatal("curve plan was mutable through a caller slice")
	}

	verified := testVerifiedState(contract)
	plan, err := VerifyCurvePlan(contract, verified)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name() != contract.Shared().Name || plan.EnvironmentIdentity() != verified.EnvironmentIdentity {
		t.Fatalf("unexpected verified plan: %s", plan.Name())
	}
	verified.Shared.Conversion.ToolVersion = "other-version"
	if _, err := VerifyCurvePlan(contract, verified); err == nil {
		t.Fatal("expected verified conversion drift rejection")
	}
}

func TestVerifyCurvePlanRejectsInvalidDerivationIdentities(t *testing.T) {
	shared := validCurvePlanSpec()
	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*VerifiedCurveState)
	}{
		{name: "state", mutate: func(state *VerifiedCurveState) { state.StateIdentity = "" }},
		{name: "environment", mutate: func(state *VerifiedCurveState) { state.EnvironmentIdentity = "" }},
		{name: "active protocol", mutate: func(state *VerifiedCurveState) { state.ActiveProtocolIdentity = "" }},
		{name: "idle protocol", mutate: func(state *VerifiedCurveState) { state.IdleProtocolIdentity = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verified := testVerifiedState(contract)
			test.mutate(&verified)
			if _, err := VerifyCurvePlan(contract, verified); err == nil {
				t.Fatal("expected invalid derivation identity to be rejected")
			}
		})
	}
}

func TestVerifyCurvePlanOwnsTheVerifiedState(t *testing.T) {
	shared := validCurvePlanSpec()
	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	verified := testVerifiedState(contract)
	plan, err := VerifyCurvePlan(contract, verified)
	if err != nil {
		t.Fatal(err)
	}
	verified.Shared.PlannedBatchSizes[0] = 99
	verified.Shared.Tensor.InputSampleShape[0] = 99
	if plan.state.Shared.PlannedBatchSizes[0] == 99 || plan.state.Shared.Tensor.InputSampleShape[0] == 99 {
		t.Fatal("verified curve-plan state was mutable through caller slices")
	}
}

func TestDeriveCurveFollowsSparsePlannedBatchSizeSequence(t *testing.T) {
	shared := validCurvePlanSpec()
	shared.PlannedBatchSizes = []int{2, 4, 8}
	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := VerifyCurvePlan(contract, testVerifiedState(contract))
	if err != nil {
		t.Fatal(err)
	}
	pass := testPass(t, plan, []float64{1, .8, .7}, "sparse")
	pass.Batches = append(pass.Batches, PassBatch{Batch: 9, CapabilityBoundary: true})
	curve, err := DeriveCurve(
		[]Pass{pass}, plan, testCurveBaselines(t, plan), pointPolicy("sparse-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	points := curve.Points()
	if len(points) != 3 || points[0].Batch != 2 || points[1].Batch != 4 || points[2].Batch != 8 {
		t.Fatalf("sparse points = %+v, want batches [2 4 8]", points)
	}
	discovery, _ := curve.pass(PassDiscovery)
	if discovery.capabilityBoundary == nil || *discovery.capabilityBoundary != 9 {
		t.Fatalf("boundary = %+v, want exhausted-set boundary 9", discovery.capabilityBoundary)
	}
}

func TestDeriveCurveRejectsSkippedPlannedBatchSizeAndEarlyBoundary(t *testing.T) {
	shared := validCurvePlanSpec()
	shared.PlannedBatchSizes = []int{2, 4, 8}
	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := VerifyCurvePlan(contract, testVerifiedState(contract))
	if err != nil {
		t.Fatal(err)
	}
	baselines := testCurveBaselines(t, plan)

	skipped := testPass(t, plan, []float64{1, .8, .7}, "skipped")
	skipped.Batches = append(skipped.Batches[:1], skipped.Batches[2:]...)
	if _, err := DeriveCurve([]Pass{skipped}, plan, baselines, pointPolicy("sparse-point")); err == nil {
		t.Fatal("expected a skipped planned batch size to be rejected")
	}

	earlyBoundary := testPass(t, plan, []float64{1}, "boundary")
	earlyBoundary.Batches = append(earlyBoundary.Batches, PassBatch{Batch: 5, CapabilityBoundary: true})
	if _, err := DeriveCurve(
		[]Pass{earlyBoundary}, plan, baselines, pointPolicy("sparse-point"),
	); err == nil {
		t.Fatal("expected a gap inside the planned batch size set not to count as exhaustion")
	}

	wrongStart := testPass(t, plan, []float64{1, .8}, "start")
	wrongStart.Batches = wrongStart.Batches[1:]
	if _, err := DeriveCurve(
		[]Pass{wrongStart}, plan, baselines, pointPolicy("sparse-point"),
	); err == nil {
		t.Fatal("expected discovery not starting at the first planned batch size to be rejected")
	}
}

func TestCurveRequiresExactStateBaselinesAndIndependentEvidence(t *testing.T) {
	_, plan := testVerifiedCurvePlan(t, 2)
	baselines := testCurveBaselines(t, plan)
	pass := testPass(t, plan, []float64{1, .8}, "discovery")
	curve, err := DeriveCurve([]Pass{pass}, plan, baselines, pointPolicy("curve-point-v1"))
	if err != nil {
		t.Fatal(err)
	}
	points := curve.Points()
	if len(points) != 2 || points[0].Batch != 1 || points[1].Batch != 2 ||
		math.Abs(points[0].MeanIncrementalWorkJoulesPerItem-1) > 1e-12 ||
		math.Abs(points[1].MeanIncrementalWorkJoulesPerItem-.8) > 1e-12 ||
		points[0].Confidence.VarianceOfMean == nil || *points[0].Confidence.VarianceOfMean != 0 ||
		points[0].Confidence.EffectiveDegreesOfFreedom != nil || points[0].Confidence.HalfWidth != 0 ||
		len(curve.IdleBaselinesByState()) != 1 {
		t.Fatalf("unexpected exact-baseline curve points: %+v", points)
	}
	points[0].Trials[0].IncrementalWorkJoulesPerItem = 99
	*points[0].Confidence.VarianceOfMean = 99
	freshPoint := curve.Points()[0]
	if freshPoint.Trials[0].IncrementalWorkJoulesPerItem == 99 ||
		*freshPoint.Confidence.VarianceOfMean == 99 {
		t.Fatal("Curve.Points exposed mutable point statistics")
	}

	missing := testCurveBaselines(t, plan)
	delete(missing, plan.state.StateIdentity)
	if _, err := DeriveCurve([]Pass{pass}, plan, missing, pointPolicy("curve-point-v1")); err == nil {
		t.Fatal("expected missing exact-state baseline rejection")
	}

	mismatched := testCurveBaselines(t, plan)
	state := plan.state.StateIdentity
	baseline := mismatched[state]
	baseline.EnvironmentIdentity = testEnvironmentIdentity("f")
	mismatched[state] = baseline
	if _, err := DeriveCurve([]Pass{pass}, plan, mismatched, pointPolicy("curve-point-v1")); err == nil {
		t.Fatal("expected baseline environment mismatch rejection")
	}
	duplicate := testPass(t, plan, []float64{1}, "same")
	confirmatory := duplicate
	confirmatory.Role = PassConfirmatory
	if _, err := DeriveCurve(
		[]Pass{duplicate, confirmatory}, plan, baselines, pointPolicy("curve-point-v1"),
	); err == nil {
		t.Fatal("expected active TrialID reuse across passes rejection")
	}
}

func TestSequentialGoldenStops(t *testing.T) {
	basePolicy := SequentialPolicy{
		Version: "sequential-v1", MinDelta: .05, Patience: 2,
		TrialsPerBatch: 3, DivergenceFactor: 1.5,
	}
	tests := []struct {
		name         string
		means        []float64
		boundary     bool
		policy       SequentialPolicy
		wantStatus   SequentialStatus
		wantReason   SequentialStopReason
		wantSelected int
		wantBoundary int
	}{
		{
			name: "no further improvement reports best not last", means: []float64{1, .8, .79, .78},
			policy: basePolicy, wantStatus: SequentialStopped,
			wantReason: StopNoFurtherImprovement, wantSelected: 2,
		},
		{
			name: "capability boundary", means: []float64{1, .8}, boundary: true,
			policy: basePolicy, wantStatus: SequentialStopped,
			wantReason: StopCapabilityBoundary, wantSelected: 2, wantBoundary: 3,
		},
		{
			name: "energy divergence", means: []float64{1, 1.6},
			policy: basePolicy, wantStatus: SequentialStopped,
			wantReason: StopEnergyDivergence, wantSelected: 1,
		},
		{
			name: "one worse batch can recover", means: []float64{1, 1.1, .8},
			policy: basePolicy, wantStatus: SequentialContinue, wantSelected: 3,
		},
		{
			name: "sub-margin gains accumulate against fixed reference", means: []float64{1, .97, .94},
			policy: basePolicy, wantStatus: SequentialContinue, wantSelected: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			curve := testSequentialCurve(t, test.means, test.boundary)
			got, err := DeriveSequentialBatchDecision(curve, test.policy)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.wantStatus || got.StopReason != test.wantReason ||
				got.SelectedBatch == nil || *got.SelectedBatch != test.wantSelected ||
				len(got.IncrementalWorkByBatch) != len(test.means) ||
				len(got.MeasuredBatchSizes) != len(test.means) {
				t.Fatalf("unexpected sequential result: %+v", got)
			}
			if test.wantBoundary == 0 && got.BoundaryBatch != nil {
				t.Fatalf("unexpected boundary: %+v", got.BoundaryBatch)
			}
			if test.wantBoundary > 0 && (got.BoundaryBatch == nil || *got.BoundaryBatch != test.wantBoundary) {
				t.Fatalf("boundary = %+v, want %d", got.BoundaryBatch, test.wantBoundary)
			}
			if got.CurvePlanName == "" ||
				len(got.IdleBaselinesByState) != 1 {
				t.Fatalf("sequential provenance is incomplete: %+v", got)
			}
		})
	}
}

func TestSequentialFollowsSparsePlannedBatchSizes(t *testing.T) {
	shared := validCurvePlanSpec()
	shared.PlannedBatchSizes = []int{2, 4, 8}
	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := VerifyCurvePlan(contract, testVerifiedState(contract))
	if err != nil {
		t.Fatal(err)
	}
	pass := testPass(t, plan, []float64{1, .8, .79}, "sparse-sequential")
	pass.Batches = append(pass.Batches, PassBatch{Batch: 9, CapabilityBoundary: true})
	curve, err := DeriveCurve(
		[]Pass{pass}, plan, testCurveBaselines(t, plan), pointPolicy("sparse-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DeriveSequentialBatchDecision(curve, SequentialPolicy{
		Version: "sequential-v1", MinDelta: .05, Patience: 3,
		TrialsPerBatch: 3, DivergenceFactor: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SequentialStopped || result.StopReason != StopCapabilityBoundary ||
		result.SelectedBatch == nil || *result.SelectedBatch != 4 ||
		result.BoundaryBatch == nil || *result.BoundaryBatch != 9 ||
		!reflect.DeepEqual(result.MeasuredBatchSizes, []int{2, 4, 8}) {
		t.Fatalf("unexpected sparse sequential result: %+v", result)
	}
}

func TestSequentialRejectsPostStopAndPolicyDrift(t *testing.T) {
	curve := testSequentialCurve(t, []float64{1, .99, .98, .97}, false)
	policy := SequentialPolicy{
		Version: "sequential-v1", MinDelta: .05, Patience: 2,
		TrialsPerBatch: 3, DivergenceFactor: 2,
	}
	if _, err := DeriveSequentialBatchDecision(curve, policy); err == nil {
		t.Fatal("expected points after the stop condition to be rejected")
	}
	curve = testSequentialCurve(t, []float64{1, .8}, false)
	policy.TrialsPerBatch = 4
	if _, err := DeriveSequentialBatchDecision(curve, policy); err == nil {
		t.Fatal("expected trials-per-batch policy mismatch rejection")
	}
}

func testSequentialCurve(t *testing.T, means []float64, boundary bool) Curve {
	t.Helper()
	batchCount := len(means)
	_, plan := testVerifiedCurvePlan(t, batchCount)
	baselines := testCurveBaselines(t, plan)
	pass := testPass(t, plan, means, "sequential")
	if boundary {
		pass.Batches = append(pass.Batches, PassBatch{Batch: batchCount + 1, CapabilityBoundary: true})
	}
	curve, err := DeriveCurve([]Pass{pass}, plan, baselines, pointPolicy("curve-point-v1"))
	if err != nil {
		t.Fatal(err)
	}
	return curve
}

func validCurvePlanSpec() CurvePlanSpec {
	shared := CurvePlanSpec{
		Name: "mobilenet-rknn-curve", LogicalModel: "mobilenet-v2-050",
		DeviceClass: "orangepi-5-plus-16g",
		NodeUID:     "node-uid", RuntimeFamily: "rknnlite", Backend: "not-applicable",
		RuntimeImage: testImage("runtime", "5"), PowerMode: "fixed",
		CPU: CurveCPUContract{
			Policy: "allocatable", PolicyVersion: "v1", Request: "7", Limit: "8",
		},
		MetricSource: CurveMetricSourceContract{
			SourceKind: "prometheus", NodeName: "orangepi", Endpoint: "http://metrics",
			PowerMetric: "power_watts",
		},
		ActiveProtocol: testCurveProtocol("saturated", testImage("metric", "8"), testImage("workload", "9")),
		IdleProtocol:   testCurveProtocol("idle-model-loaded", testImage("metric", "8"), testImage("workload", "9")),
		Conversion: CurveConversionContract{
			TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
		},
		Artifact: CurveArtifactContract{
			ExecutionPath: "rknnlite", Name: "artifact",
			Path: "/artifact/model.rknn", Image: testImage("artifact", "c"),
		},
		PlannedBatchSizes: []int{1, 2, 4},
		Tensor:            CurveTensorContract{Layout: "NHWC", DataType: "float32", InputSampleShape: []int{224, 224, 3}},
	}
	shared.ActiveProtocol.WarmupIterations = 20
	shared.IdleProtocol.InputGenerator = "none"
	shared.IdleProtocol.OutputValidation = "none"
	shared.IdleProtocol.SettlingDurationSeconds = 5
	return shared
}

func testCurvePlanSpec(batchCount int) CurvePlanSpec {
	shared := validCurvePlanSpec()
	shared.PlannedBatchSizes = make([]int, batchCount)
	for index := range shared.PlannedBatchSizes {
		shared.PlannedBatchSizes[index] = index + 1
	}
	return shared
}

func testCurveProtocol(mode, metricImage, workloadImage string) CurveProtocolContract {
	return CurveProtocolContract{
		WorkloadMode: mode, InputGenerator: "seeded", OutputValidation: "shape",
		DurationSeconds:            30,
		MetricIntervalMilliseconds: 1000, MetricTimeoutMilliseconds: 900,
		MetricDurationSeconds: 31, MetricObserverImage: metricImage,
		WorkloadObserverImage: workloadImage, WorkloadSchemaVersion: "chill.dacs.io/workload-contract.v1alpha1",
	}
}

func testVerifiedCurvePlan(t *testing.T, batchCount int) (CurvePlan, VerifiedCurvePlan) {
	t.Helper()
	shared := testCurvePlanSpec(batchCount)
	contract, err := NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := VerifyCurvePlan(contract, testVerifiedState(contract))
	if err != nil {
		t.Fatal(err)
	}
	return contract, plan
}

func testVerifiedState(contract CurvePlan) VerifiedCurveState {
	return VerifiedCurveState{
		Shared:        contract.Shared(),
		StateIdentity: testStateIdentity("1"), EnvironmentIdentity: testEnvironmentIdentity("1"),
		ActiveProtocolIdentity: testProtocolIdentity("a"),
		IdleProtocolIdentity:   testProtocolIdentity("e"),
	}
}

func testCurveBaselines(t *testing.T, plan VerifiedCurvePlan) map[string]IdleBaseline {
	t.Helper()
	state := plan.state
	trials := make([]IdleTrial, 3)
	for trialIndex := range trials {
		key := "idle-a-" + string(rune('1'+trialIndex))
		trials[trialIndex] = IdleTrial{
			StateIdentity: state.StateIdentity, EnvironmentIdentity: state.EnvironmentIdentity,
			MeasurementProtocolIdentity: state.IdleProtocolIdentity,
			TrialID:                     key, RawEvidence: rawEvidence(key),
			AdmissionPolicyVersion: "idle-admission-v1", ModelLoadedIdleMeanWallWatts: 1,
		}
	}
	baseline, err := DeriveIdleBaseline(trials, pointPolicy("idle-point-v1"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]IdleBaseline{state.StateIdentity: baseline}
}

func testPass(t *testing.T, plan VerifiedCurvePlan, means []float64, idPrefix string) Pass {
	t.Helper()
	state := plan.state
	plannedBatchSizes := plan.recorded.record.Shared.PlannedBatchSizes
	if len(means) > len(plannedBatchSizes) {
		t.Fatalf("means = %d, planned batch sizes = %d", len(means), len(plannedBatchSizes))
	}
	pass := Pass{Role: PassDiscovery}
	for index, mean := range means {
		plannedBatchSize := plannedBatchSizes[index]
		batchInput := PassBatch{Batch: plannedBatchSize}
		for trialIndex := 0; trialIndex < 3; trialIndex++ {
			trialID := idPrefix + "-b" + string(rune('0'+plannedBatchSize)) + "-" + string(rune('1'+trialIndex))
			total := mean + 1
			trial := Trial{
				StateIdentity: state.StateIdentity, EnvironmentIdentity: state.EnvironmentIdentity,
				MeasurementProtocolIdentity: state.ActiveProtocolIdentity,
				TrialID:                     trialID, Batch: plannedBatchSize, CompletedItems: 1,
				LatencyMilliseconds: []float64{1},
			}
			admission := AdmissionResult{
				Verdict: VerdictAccepted, PolicyVersion: "active-admission-v1",
				StateIdentity: trial.StateIdentity, EnvironmentIdentity: trial.EnvironmentIdentity,
				MeasurementProtocolIdentity: trial.MeasurementProtocolIdentity,
				TrialID:                     trialID, TotalWallJoules: &total, WindowSeconds: 1, CompletedItems: 1,
			}
			batchInput.Runs = append(batchInput.Runs, CurveRun{Trial: trial, Admission: admission})
		}
		pass.Batches = append(pass.Batches, batchInput)
	}
	return pass
}

func testSHA256Value(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func testImage(name, character string) string {
	return "registry.example/chill/" + name + "@" + testSHA256Value(character)
}

func testStateIdentity(character string) string {
	return "chill.dacs.io/execution-state-identity.v1alpha2@" + testSHA256Value(character)
}

func testEnvironmentIdentity(character string) string {
	return "chill.dacs.io/external-environment-identity.v1alpha1@" + testSHA256Value(character)
}

func testProtocolIdentity(character string) string {
	return "chill.dacs.io/measurement-protocol-identity.v1alpha1@" + testSHA256Value(character)
}
