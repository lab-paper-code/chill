package profilederivation

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFreezeCurveFamilyContractCarriesTheEligibleBatches(t *testing.T) {
	shared := validSharedContract()
	shared.EligibleBatches = []int{4, 1, 2}

	contract, err := FreezeCurveFamilyContract(shared)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}

	got := contract.EligibleBatches()
	if !reflect.DeepEqual(got, []int{1, 2, 4}) {
		t.Fatalf("eligible batches = %v, want sorted [1 2 4]", got)
	}
}

func TestFreezeCurveFamilyContractRejectsAnEmptyEligibleSet(t *testing.T) {
	shared := validSharedContract()
	shared.EligibleBatches = nil

	if _, err := FreezeCurveFamilyContract(shared); err == nil {
		t.Fatal("expected an empty eligible batch set to be rejected")
	}
}

func TestFreezeCurveFamilyContractRejectsInvalidArtifactAndEligibleBatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CurveFamilySharedContract)
	}{
		{name: "empty artifact field", mutate: func(shared *CurveFamilySharedContract) {
			shared.Artifact.Image = ""
		}},
		{name: "non-positive batch", mutate: func(shared *CurveFamilySharedContract) {
			shared.EligibleBatches = []int{0, 1}
		}},
		{name: "duplicate batch", mutate: func(shared *CurveFamilySharedContract) {
			shared.EligibleBatches = []int{1, 2, 1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shared := validSharedContract()
			test.mutate(&shared)
			if _, err := FreezeCurveFamilyContract(shared); err == nil {
				t.Fatal("expected invalid curve-family contract to be rejected")
			}
		})
	}
}

func TestFreezeAndVerifyCurveFamilyState(t *testing.T) {
	shared := validSharedContract()
	contract, err := FreezeCurveFamilyContract(shared)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := contract.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCurveFamilyContract(append([]byte(" \n"), canonical...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Shared(), contract.Shared()) ||
		!reflect.DeepEqual(decoded.EligibleBatches(), contract.EligibleBatches()) {
		t.Fatal("decoded contract lost configured semantics")
	}
	unknown := bytes.Replace(canonical, []byte(`"schemaVersion":`), []byte(`"unknown":1,"schemaVersion":`), 1)
	if _, err := DecodeCurveFamilyContract(unknown); err == nil {
		t.Fatal("expected unknown contract field rejection")
	}
	if _, err := DecodeCurveFamilyContract(append(canonical, []byte(` {}`)...)); err == nil {
		t.Fatal("expected trailing contract value rejection")
	}

	// Inputs and projections are copies, not mutation paths into the frozen record.
	shared.EligibleBatches[0] = 99
	shared.Tensor.NonBatchShape[0] = 99
	projected := contract.Shared()
	projected.EligibleBatches[0] = 99
	projected.Tensor.NonBatchShape[0] = 99
	if !reflect.DeepEqual(contract.EligibleBatches(), []int{1, 2, 4}) ||
		contract.Shared().Tensor.NonBatchShape[0] != 224 {
		t.Fatal("frozen curve-family contract was mutable through a caller slice")
	}

	verified := testVerifiedState(contract)
	family, err := VerifyCurveFamily(contract, verified)
	if err != nil {
		t.Fatal(err)
	}
	if family.Name() != contract.Shared().Name || family.EnvironmentIdentity() != verified.EnvironmentIdentity {
		t.Fatalf("unexpected verified family: %s", family.Name())
	}
	verified.Shared.Conversion.ToolVersion = "other-version"
	if _, err := VerifyCurveFamily(contract, verified); err == nil {
		t.Fatal("expected verified conversion drift rejection")
	}
}

func TestCurveRequiresExactStateBaselinesAndIndependentEvidence(t *testing.T) {
	_, family := testVerifiedFamily(t, 2)
	baselines := testFamilyBaselines(t, family)
	pass := testPass(t, family, []float64{1, .8}, "discovery")
	curve, err := DeriveCurve([]Pass{pass}, family, baselines, pointPolicy("curve-point-v1"))
	if err != nil {
		t.Fatal(err)
	}
	points := curve.Points()
	if len(points) != 2 || points[0].Batch != 1 || points[1].Batch != 2 ||
		math.Abs(points[0].MeanIncrementalJoulesPerItem-1) > 1e-12 ||
		math.Abs(points[1].MeanIncrementalJoulesPerItem-.8) > 1e-12 ||
		points[0].Confidence.VarianceOfMean == nil || *points[0].Confidence.VarianceOfMean != 0 ||
		points[0].Confidence.EffectiveDegreesOfFreedom != nil || points[0].Confidence.HalfWidth != 0 ||
		len(curve.IdleBaselinesByState()) != 1 {
		t.Fatalf("unexpected exact-baseline curve points: %+v", points)
	}
	points[0].Trials[0].IncrementalJoulesPerItem = 99
	*points[0].Confidence.VarianceOfMean = 99
	freshPoint := curve.Points()[0]
	if freshPoint.Trials[0].IncrementalJoulesPerItem == 99 || *freshPoint.Confidence.VarianceOfMean == 99 {
		t.Fatal("Curve.Points exposed mutable point statistics")
	}

	missing := testFamilyBaselines(t, family)
	delete(missing, family.state.StateIdentity)
	if _, err := DeriveCurve([]Pass{pass}, family, missing, pointPolicy("curve-point-v1")); err == nil {
		t.Fatal("expected missing exact-state baseline rejection")
	}

	mismatched := testFamilyBaselines(t, family)
	state := family.state.StateIdentity
	baseline := mismatched[state]
	baseline.EnvironmentIdentity = testEnvironmentIdentity("f")
	mismatched[state] = baseline
	if _, err := DeriveCurve([]Pass{pass}, family, mismatched, pointPolicy("curve-point-v1")); err == nil {
		t.Fatal("expected baseline environment mismatch rejection")
	}
	duplicate := testPass(t, family, []float64{1}, "same")
	confirmatory := duplicate
	confirmatory.Role = PassConfirmatory
	if _, err := DeriveCurve(
		[]Pass{duplicate, confirmatory}, family, baselines, pointPolicy("curve-point-v1"),
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
				len(got.EpsilonByBatch) != len(test.means) ||
				len(got.MeasuredBatches) != len(test.means) {
				t.Fatalf("unexpected sequential result: %+v", got)
			}
			if test.wantBoundary == 0 && got.BoundaryBatch != nil {
				t.Fatalf("unexpected boundary: %+v", got.BoundaryBatch)
			}
			if test.wantBoundary > 0 && (got.BoundaryBatch == nil || *got.BoundaryBatch != test.wantBoundary) {
				t.Fatalf("boundary = %+v, want %d", got.BoundaryBatch, test.wantBoundary)
			}
			if got.CurveFamilyName == "" ||
				len(got.IdleBaselinesByState) != 1 {
				t.Fatalf("sequential provenance is incomplete: %+v", got)
			}
		})
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
	memberCount := len(means)
	_, family := testVerifiedFamily(t, memberCount)
	baselines := testFamilyBaselines(t, family)
	pass := testPass(t, family, means, "sequential")
	if boundary {
		pass.Batches = append(pass.Batches, PassBatch{Batch: memberCount + 1, CapabilityBoundary: true})
	}
	curve, err := DeriveCurve([]Pass{pass}, family, baselines, pointPolicy("curve-point-v1"))
	if err != nil {
		t.Fatal(err)
	}
	return curve
}

func validSharedContract() CurveFamilySharedContract {
	shared := CurveFamilySharedContract{
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
		EligibleBatches: []int{1, 2, 4},
		Tensor:          CurveTensorContract{Layout: "NHWC", DataType: "float32", NonBatchShape: []int{224, 224, 3}},
	}
	shared.ActiveProtocol.WarmupIterations = 20
	shared.IdleProtocol.InputGenerator = "none"
	shared.IdleProtocol.OutputValidation = "none"
	shared.IdleProtocol.SettlingDurationSeconds = 5
	return shared
}

func testCurveContract(batchCount int) CurveFamilySharedContract {
	shared := validSharedContract()
	shared.EligibleBatches = make([]int, batchCount)
	for index := range shared.EligibleBatches {
		shared.EligibleBatches[index] = index + 1
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

func testVerifiedFamily(t *testing.T, batchCount int) (CurveFamilyContract, CurveFamily) {
	t.Helper()
	shared := testCurveContract(batchCount)
	contract, err := FreezeCurveFamilyContract(shared)
	if err != nil {
		t.Fatal(err)
	}
	family, err := VerifyCurveFamily(contract, testVerifiedState(contract))
	if err != nil {
		t.Fatal(err)
	}
	return contract, family
}

func testVerifiedState(contract CurveFamilyContract) VerifiedCurveFamilyState {
	return VerifiedCurveFamilyState{
		Shared:        contract.Shared(),
		StateIdentity: testStateIdentity("1"), EnvironmentIdentity: testEnvironmentIdentity("1"),
		ActiveProtocolIdentity: testProtocolIdentity("a"),
		IdleProtocolIdentity:   testProtocolIdentity("e"),
	}
}

func testFamilyBaselines(t *testing.T, family CurveFamily) map[string]IdleBaseline {
	t.Helper()
	state := family.state
	trials := make([]IdleTrial, 3)
	for trialIndex := range trials {
		key := "idle-a-" + string(rune('1'+trialIndex))
		trials[trialIndex] = IdleTrial{
			StateIdentity: state.StateIdentity, EnvironmentIdentity: state.EnvironmentIdentity,
			MeasurementProtocolIdentity: state.IdleProtocolIdentity,
			TrialID:                     key, RawEvidence: rawEvidence(key),
			AdmissionPolicyVersion: "idle-admission-v1", MeanWallWatts: 1,
		}
	}
	baseline, err := DeriveIdleBaseline(trials, pointPolicy("idle-point-v1"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]IdleBaseline{state.StateIdentity: baseline}
}

func testPass(t *testing.T, family CurveFamily, means []float64, idPrefix string) Pass {
	t.Helper()
	state := family.state
	pass := Pass{Role: PassDiscovery}
	for index, mean := range means {
		batch := index + 1
		batchInput := PassBatch{Batch: batch}
		for trialIndex := 0; trialIndex < 3; trialIndex++ {
			trialID := idPrefix + "-b" + string(rune('0'+batch)) + "-" + string(rune('1'+trialIndex))
			total := mean + 1
			trial := Trial{
				StateIdentity: state.StateIdentity, EnvironmentIdentity: state.EnvironmentIdentity,
				MeasurementProtocolIdentity: state.ActiveProtocolIdentity,
				TrialID:                     trialID, Batch: batch, CompletedItems: 1,
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
