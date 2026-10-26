package profilederivation

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	mutatedValue = "mutated"
	mutatedPath  = "/mutated"
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
		{name: "no artifact files", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Files = nil
		}},
		{name: "empty artifact image", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Image = ""
		}},
		{name: "root artifact file", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Files = []string{"/"}
		}},
		{name: "relative artifact file", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Files = []string{"artifact/model.rknn"}
		}},
		{name: "unclean artifact file", mutate: func(shared *CurvePlanSpec) {
			shared.Artifact.Files = []string{"/artifact/../model.rknn"}
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

	// Inputs and projections are copies, not mutation paths into the initialized record.
	shared.PlannedBatchSizes[0] = 99
	shared.Artifact.Files[0] = mutatedPath
	shared.Preprocessor.Inputs[0] = mutatedValue
	shared.ActiveProtocol.Dataset.UID = mutatedValue
	projected := contract.Shared()
	projected.PlannedBatchSizes[0] = 99
	projected.Artifact.Files[0] = "/projected"
	projected.Preprocessor.Outputs[0] = "projected"
	projected.ActiveProtocol.Dataset.UID = "projected"
	if !reflect.DeepEqual(contract.PlannedBatchSizes(), []int{1, 2, 4}) ||
		!reflect.DeepEqual(contract.Shared().Artifact, validCurvePlanSpec().Artifact) ||
		!reflect.DeepEqual(contract.Shared().Preprocessor, validCurvePlanSpec().Preprocessor) ||
		!reflect.DeepEqual(contract.Shared().ActiveProtocol, validCurvePlanSpec().ActiveProtocol) {
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

func TestCurvePlanFreezesPreprocessorAndDatasetIdentity(t *testing.T) {
	contract, err := NewCurvePlan(validCurvePlanSpec())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := contract.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CurvePlanSpec){
		"preprocessor image":  func(shared *CurvePlanSpec) { shared.Preprocessor.Image = testImage("preprocessor", "e") },
		"preprocessor inputs": func(shared *CurvePlanSpec) { shared.Preprocessor.Inputs = []string{"image", "question"} },
		"max new tokens":      func(shared *CurvePlanSpec) { shared.Preprocessor.MaxNewTokens = 16 },
		"dataset UID":         func(shared *CurvePlanSpec) { shared.ActiveProtocol.Dataset.UID = "other-dataset-uid" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := contract.Shared()
			mutate(&changed)
			other, err := NewCurvePlan(changed)
			if err != nil {
				t.Fatal(err)
			}
			otherJSON, err := other.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(canonical, otherJSON) {
				t.Fatal("changed preprocessing contract retained the frozen plan identity")
			}
			verified := testVerifiedState(contract)
			verified.Shared = changed
			if _, err := VerifyCurvePlan(contract, verified); err == nil ||
				err.Error() != "verified curve state does not match the plan" {
				t.Fatalf("changed preprocessing verification error = %v", err)
			}
		})
	}
}

func TestCurvePlanRejectsIncompletePreprocessorAndDataset(t *testing.T) {
	for name, mutate := range map[string]func(*CurvePlanSpec){
		"preprocessor path":      func(shared *CurvePlanSpec) { shared.Preprocessor.Path = "relative" },
		"preprocessor inputs":    func(shared *CurvePlanSpec) { shared.Preprocessor.Inputs = nil },
		"duplicate output":       func(shared *CurvePlanSpec) { shared.Preprocessor.Outputs = []string{"label", "label"} },
		"active without dataset": func(shared *CurvePlanSpec) { shared.ActiveProtocol.Dataset = nil },
		"idle with dataset":      func(shared *CurvePlanSpec) { shared.IdleProtocol.Dataset = &CurveDatasetContract{Name: "d", UID: "u"} },
		"dataset without UID":    func(shared *CurvePlanSpec) { shared.ActiveProtocol.Dataset.UID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			shared := validCurvePlanSpec()
			mutate(&shared)
			if _, err := NewCurvePlan(shared); err == nil {
				t.Fatal("incomplete preprocessing contract accepted")
			}
		})
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
	verified.Shared.Artifact.Files[0] = mutatedPath
	verified.Shared.Preprocessor.Inputs[0] = mutatedValue
	if plan.state.Shared.PlannedBatchSizes[0] == 99 ||
		!reflect.DeepEqual(plan.state.Shared.Artifact, validCurvePlanSpec().Artifact) ||
		!reflect.DeepEqual(plan.state.Shared.Preprocessor, validCurvePlanSpec().Preprocessor) {
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
}

func TestDeriveCurveRejectsSkippedPlannedBatchSize(t *testing.T) {
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

	wrongStart := testPass(t, plan, []float64{1, .8}, "start")
	wrongStart.Batches = wrongStart.Batches[1:]
	if _, err := DeriveCurve(
		[]Pass{wrongStart}, plan, baselines, pointPolicy("sparse-point"),
	); err == nil {
		t.Fatal("expected search not starting at the first planned batch size to be rejected")
	}
}

func TestCurveRequiresExactStateBaselinesAndIndependentEvidence(t *testing.T) {
	_, plan := testVerifiedCurvePlan(t, 2)
	baselines := testCurveBaselines(t, plan)
	pass := testPass(t, plan, []float64{1, .8}, "search")
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
	if _, err := DeriveCurve([]Pass{pass}, plan, mismatched, pointPolicy("curve-point-v1")); err != nil {
		t.Fatalf("environment observations blocked curve calculation: %v", err)
	}
	duplicate := testPass(t, plan, []float64{1}, "same")
	if _, err := DeriveCurve(
		[]Pass{duplicate, duplicate}, plan, baselines, pointPolicy("curve-point-v1"),
	); err == nil {
		t.Fatal("expected more than one search pass to be rejected")
	}
}

func TestEnergyOptimizationFollowsEveryStrictImprovement(t *testing.T) {
	policy := EnergyObjectivePolicy{Version: "energy-objective-v1", TrialsPerBatch: 3}
	tests := []struct {
		name       string
		means      []float64
		planSize   int
		wantStatus EnergySearchStatus
		wantReason EnergySearchCompletionReason
		wantBest   int
	}{
		{
			name:  "tiny improvements remain objective improvements",
			means: []float64{1, .8, .799, .7989}, planSize: 5,
			wantStatus: EnergySearchContinue, wantBest: 4,
		},
		{
			name:  "first non-improving neighbor closes the local search",
			means: []float64{1, .8, .81}, planSize: 5,
			wantStatus: EnergySearchComplete, wantReason: EnergySearchLocalMinimum, wantBest: 2,
		},
		{
			name:  "search-space exhaustion is successful completion",
			means: []float64{1, .8, .79}, planSize: 3,
			wantStatus: EnergySearchComplete, wantReason: EnergySearchSpaceExhausted, wantBest: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			curve := testEnergyOptimizationCurve(t, test.means, test.planSize)
			got, err := DeriveMinimumEnergyBatch(curve, policy)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.wantStatus || got.CompletionReason != test.wantReason ||
				got.BestBatch == nil || *got.BestBatch != test.wantBest ||
				len(got.IncrementalWorkByBatch) != len(test.means) ||
				len(got.MeasuredBatchSizes) != len(test.means) {
				t.Fatalf("unexpected energy-optimization result: %+v", got)
			}
			if got.Objective != EnergyObjectiveMinIncrementalWork || got.CurvePlanName == "" ||
				len(got.IdleBaselinesByState) != 1 {
				t.Fatalf("energy-optimization provenance is incomplete: %+v", got)
			}
		})
	}
}

func TestEnergyOptimizationFollowsSparsePlannedBatchSizes(t *testing.T) {
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
	pass := testPass(t, plan, []float64{1, .8, .79}, "sparse-energy-search")
	curve, err := DeriveCurve(
		[]Pass{pass}, plan, testCurveBaselines(t, plan), pointPolicy("sparse-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DeriveMinimumEnergyBatch(curve, EnergyObjectivePolicy{
		Version: "energy-objective-v1", TrialsPerBatch: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != EnergySearchComplete || result.CompletionReason != EnergySearchSpaceExhausted ||
		result.BestBatch == nil || *result.BestBatch != 8 ||
		!reflect.DeepEqual(result.MeasuredBatchSizes, []int{2, 4, 8}) {
		t.Fatalf("unexpected sparse energy-optimization result: %+v", result)
	}
}

func TestEnergyOptimizationRejectsPostMinimumAndPolicyDrift(t *testing.T) {
	curve := testEnergyOptimizationCurve(t, []float64{1, 1.01, .9}, 4)
	policy := EnergyObjectivePolicy{Version: "energy-objective-v1", TrialsPerBatch: 3}
	if _, err := DeriveMinimumEnergyBatch(curve, policy); err == nil {
		t.Fatal("expected points after the local minimum to be rejected")
	}
	curve = testEnergyOptimizationCurve(t, []float64{1, .8}, 3)
	policy.TrialsPerBatch = 4
	if _, err := DeriveMinimumEnergyBatch(curve, policy); !errors.Is(err, ErrEnergyObjectivePolicyMismatch) {
		t.Fatalf("expected classified trials-per-batch policy mismatch, got %v", err)
	}
	policy.TrialsPerBatch = 3
	curve.passes[0].points[0].Trials = append(curve.passes[0].points[0].Trials, curve.passes[0].points[0].Trials[0])
	if _, err := DeriveMinimumEnergyBatch(curve, policy); !errors.Is(err, ErrEnergyObjectivePolicyMismatch) {
		t.Fatalf("expected classified excess-trial policy mismatch, got %v", err)
	}
}

func testEnergyOptimizationCurve(t *testing.T, means []float64, planSize int) Curve {
	t.Helper()
	_, plan := testVerifiedCurvePlan(t, planSize)
	baselines := testCurveBaselines(t, plan)
	pass := testPass(t, plan, means, "energy-optimization")
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
			Files: []string{"/artifact/model.rknn"}, Image: testImage("artifact", "c"),
		},
		Preprocessor: CurvePreprocessorContract{
			Name: "mobilenet-imagenet", Image: testImage("preprocessor", "d"), Architecture: "arm64",
			Path: "/artifact/mobilenet-nhwc", Inputs: []string{"image"}, Outputs: []string{"label"},
		},
		PlannedBatchSizes: []int{1, 2, 4},
	}
	shared.ActiveProtocol.WarmupIterations = 20
	shared.ActiveProtocol.Dataset = &CurveDatasetContract{Name: "vqav2-val-subset", UID: "dataset-uid"}
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
		WorkloadMode:               mode,
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
			ModelLoadedIdleMeanWallWatts: 1,
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
	pass := Pass{Role: PassSearch}
	for index, mean := range means {
		plannedBatchSize := plannedBatchSizes[index]
		batchInput := PassBatch{Batch: plannedBatchSize}
		for trialIndex := 0; trialIndex < 3; trialIndex++ {
			trialID := idPrefix + "-b" + string(rune('0'+plannedBatchSize)) + "-" + string(rune('1'+trialIndex))
			total := mean*float64(plannedBatchSize) + 1
			trial := Trial{
				StateIdentity: state.StateIdentity, EnvironmentIdentity: state.EnvironmentIdentity,
				MeasurementProtocolIdentity: state.ActiveProtocolIdentity,
				TrialID:                     trialID, Batch: plannedBatchSize, CompletedItems: plannedBatchSize, CompletedCalls: 1,
				WindowStart: time.Unix(0, 0), WindowEnd: time.Unix(1, 0),
				Samples:             []Sample{{time.Unix(0, 100000000), total}, {time.Unix(0, 900000000), total}},
				LatencyMilliseconds: []float64{1},
			}
			batchInput.Trials = append(batchInput.Trials, trial)
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
