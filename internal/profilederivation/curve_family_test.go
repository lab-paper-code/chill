package profilederivation

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestFreezeAndVerifyCurveFamily(t *testing.T) {
	shared, members := testCurveContract(2)
	contract, err := FreezeCurveFamilyContract(shared, []CurveFamilyMemberContract{members[1], members[0]})
	if err != nil {
		t.Fatal(err)
	}
	ordered := contract.Members()
	if ordered[0].Batch != 1 || ordered[1].Batch != 2 ||
		!strings.HasPrefix(contract.Digest(), curveFamilyContractSchemaVersion+"@sha256:") {
		t.Fatalf("unexpected frozen contract: %s %+v", contract.Digest(), ordered)
	}
	second, err := FreezeCurveFamilyContract(shared, members)
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest() != contract.Digest() {
		t.Fatalf("batch order changed the contract digest: %s != %s", second.Digest(), contract.Digest())
	}
	canonical, err := contract.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCurveFamilyContract(append([]byte(" \n"), canonical...))
	if err != nil || decoded.Digest() != contract.Digest() {
		t.Fatalf("decode did not reproduce the frozen contract: %s %v", decoded.Digest(), err)
	}
	unknown := bytes.Replace(canonical, []byte(`"schemaVersion":`), []byte(`"unknown":1,"schemaVersion":`), 1)
	if _, err := DecodeCurveFamilyContract(unknown); err == nil {
		t.Fatal("expected unknown contract field rejection")
	}
	if _, err := DecodeCurveFamilyContract(append(canonical, []byte(` {}`)...)); err == nil {
		t.Fatal("expected trailing contract value rejection")
	}

	// Inputs and projections are copies, not mutation paths into the frozen record.
	members[0].InputShape[0] = 99
	ordered[0].InputShape[0] = 99
	if contract.Members()[0].InputShape[0] != 1 {
		t.Fatal("frozen curve-family member was mutable through a caller slice")
	}

	verified := testVerifiedMembers(contract)
	family, err := VerifyCurveFamily(contract, verified)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(family.Identity(), curveFamilyIdentitySchemaVersion+"@sha256:") ||
		family.ContractDigest() != contract.Digest() || len(family.Members()) != 2 {
		t.Fatalf("unexpected verified family: %s %s", family.Identity(), family.ContractDigest())
	}
	reversed := []VerifiedCurveFamilyMember{verified[1], verified[0]}
	reorderedFamily, err := VerifyCurveFamily(contract, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedFamily.Identity() != family.Identity() {
		t.Fatal("verified member order changed the realized family identity")
	}

	drifted := testVerifiedMembers(contract)
	drifted[1].Shared.Conversion.RecipeDigest = testDigest("f")
	if _, err := VerifyCurveFamily(contract, drifted); err == nil {
		t.Fatal("expected verified conversion drift rejection")
	}
	mixedEnvironment := testVerifiedMembers(contract)
	mixedEnvironment[1].EnvironmentIdentity = testEnvironmentIdentity("e")
	if _, err := VerifyCurveFamily(contract, mixedEnvironment); err == nil {
		t.Fatal("expected mixed environment rejection")
	}
	sameStateDifferentProtocol := testVerifiedMembers(contract)
	sameStateDifferentProtocol[1].StateIdentity = sameStateDifferentProtocol[0].StateIdentity
	if _, err := VerifyCurveFamily(contract, sameStateDifferentProtocol); err == nil {
		t.Fatal("expected one-state protocol ambiguity rejection")
	}
}

func TestCurveRequiresExactStateBaselinesAndIndependentEvidence(t *testing.T) {
	contract, family := testVerifiedFamily(t, 2)
	_ = contract
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
		points[0].Confidence.EffectiveDegreesOfFreedom != nil || points[0].Confidence.HalfWidth != 0 {
		t.Fatalf("unexpected exact-baseline curve points: %+v", points)
	}
	points[0].Trials[0].IncrementalJoulesPerItem = 99
	*points[0].Confidence.VarianceOfMean = 99
	freshPoint := curve.Points()[0]
	if freshPoint.Trials[0].IncrementalJoulesPerItem == 99 || *freshPoint.Confidence.VarianceOfMean == 99 {
		t.Fatal("Curve.Points exposed mutable point statistics")
	}

	missing := testFamilyBaselines(t, family)
	delete(missing, family.Members()[1].StateIdentity)
	if _, err := DeriveCurve([]Pass{pass}, family, missing, pointPolicy("curve-point-v1")); err == nil {
		t.Fatal("expected missing exact-state baseline rejection")
	}

	mismatched := testFamilyBaselines(t, family)
	state := family.Members()[1].StateIdentity
	baseline := mismatched[state]
	baseline.EnvironmentIdentity = testEnvironmentIdentity("f")
	mismatched[state] = baseline
	if _, err := DeriveCurve([]Pass{pass}, family, mismatched, pointPolicy("curve-point-v1")); err == nil {
		t.Fatal("expected baseline environment mismatch rejection")
	}

	reused := testFamilyBaselines(t, family)
	firstState := family.Members()[0].StateIdentity
	secondState := family.Members()[1].StateIdentity
	second := reused[secondState]
	second.Trials[0].RawEvidence = reused[firstState].Trials[0].RawEvidence
	reused[secondState] = second
	if _, err := DeriveCurve([]Pass{pass}, family, reused, pointPolicy("curve-point-v1")); err == nil {
		t.Fatal("expected cross-state idle evidence reuse rejection")
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
			name: "one worse member can recover", means: []float64{1, 1.1, .8},
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
			if got.CurveFamilyContractDigest == "" || got.CurveFamilyIdentity == "" ||
				len(got.IdleBaselinesByState) != len(test.means) {
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

func testCurveContract(memberCount int) (CurveFamilySharedContract, []CurveFamilyMemberContract) {
	shared := CurveFamilySharedContract{
		LogicalModel: "mobilenet-v2-050", SourceArtifactDigest: testDigest("1"),
		ModelSpecContentDigest: testDigest("2"), DeviceClassContentDigest: testDigest("3"),
		RuntimeDeclarationDigest: testDigest("4"), DeviceClass: "orangepi-5-plus-16g",
		NodeUID: "node-uid", RuntimeFamily: "rknnlite", Backend: "not-applicable",
		RuntimeImage: testImage("runtime", "5"), PowerMode: "fixed", HostContractDigest: testDigest("6"),
		CPU: CurveCPUContract{
			Policy: "allocatable", PolicyVersion: "v1", Request: "7", Limit: "8",
		},
		MetricSource: CurveMetricSourceContract{
			SourceKind: "prometheus", NodeName: "orangepi", Endpoint: "http://metrics",
			PowerMetric: "power_watts", ConfigurationContentDigest: testDigest("7"),
		},
		ActiveProtocol: testCurveProtocol("saturated", testImage("metric", "8"), testImage("workload", "9")),
		IdleProtocol:   testCurveProtocol("idle-model-loaded", testImage("metric", "8"), testImage("workload", "9")),
		Conversion: CurveConversionContract{
			Derived: true, TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
			ToolDistributionDigest: testDigest("a"), RecipeDigest: testDigest("b"),
		},
		Tensor: CurveTensorContract{Layout: "NHWC", DataType: "float32", NonBatchShape: []int{224, 224, 3}},
	}
	shared.ActiveProtocol.WarmupIterations = 20
	shared.IdleProtocol.InputGenerator = "none"
	shared.IdleProtocol.OutputValidation = "none"
	shared.IdleProtocol.SettlingDurationSeconds = 5
	members := make([]CurveFamilyMemberContract, memberCount)
	for index := range members {
		batch := index + 1
		members[index] = CurveFamilyMemberContract{
			Batch: batch, ExecutionPath: "rknnlite-bs" + string(rune('0'+batch)),
			ArtifactName: "artifact-bs" + string(rune('0'+batch)), ArtifactDigest: testDigest(string(rune('c' + index))),
			ArtifactPath:  "/artifact/model-bs" + string(rune('0'+batch)) + ".rknn",
			ArtifactImage: testImage("artifact", string(rune('c'+index))), FixedBatchCapability: batch,
			InputShape: []int{batch, 224, 224, 3},
		}
	}
	return shared, members
}

func testCurveProtocol(mode, metricImage, workloadImage string) CurveProtocolContract {
	return CurveProtocolContract{
		WorkloadMode: mode, InputGenerator: "seeded", OutputValidation: "shape",
		DurationSeconds: 30, Repetitions: 1,
		MetricIntervalMilliseconds: 1000, MetricTimeoutMilliseconds: 900,
		MetricDurationSeconds: 31, MetricObserverImage: metricImage,
		WorkloadObserverImage: workloadImage, WorkloadContractDigest: testDigest("d"),
	}
}

func testVerifiedFamily(t *testing.T, memberCount int) (CurveFamilyContract, CurveFamily) {
	t.Helper()
	shared, members := testCurveContract(memberCount)
	contract, err := FreezeCurveFamilyContract(shared, members)
	if err != nil {
		t.Fatal(err)
	}
	family, err := VerifyCurveFamily(contract, testVerifiedMembers(contract))
	if err != nil {
		t.Fatal(err)
	}
	return contract, family
}

func testVerifiedMembers(contract CurveFamilyContract) []VerifiedCurveFamilyMember {
	members := contract.Members()
	result := make([]VerifiedCurveFamilyMember, len(members))
	activeCharacters := "abcdef0123456789"
	idleCharacters := "ef0123456789abcd"
	for index, member := range members {
		identityByte := string(rune('1' + index))
		result[index] = VerifiedCurveFamilyMember{
			Shared: contract.Shared(), Member: member,
			StateIdentity: testStateIdentity(identityByte), EnvironmentIdentity: testEnvironmentIdentity("1"),
			ActiveProtocolIdentity: testProtocolIdentity(string(activeCharacters[index])),
			IdleProtocolIdentity:   testProtocolIdentity(string(idleCharacters[index])),
		}
	}
	return result
}

func testFamilyBaselines(t *testing.T, family CurveFamily) map[string]IdleBaseline {
	t.Helper()
	result := make(map[string]IdleBaseline)
	for memberIndex, member := range family.Members() {
		trials := make([]IdleTrial, 3)
		for trialIndex := range trials {
			key := "idle-" + string(rune('a'+memberIndex)) + "-" + string(rune('1'+trialIndex))
			trials[trialIndex] = IdleTrial{
				StateIdentity: member.StateIdentity, EnvironmentIdentity: member.EnvironmentIdentity,
				MeasurementProtocolIdentity: member.IdleProtocolIdentity,
				TrialID:                     key, RawEvidence: rawEvidence(key),
				AdmissionPolicyVersion: "idle-admission-v1", MeanWallWatts: 1,
			}
		}
		baseline, err := DeriveIdleBaseline(trials, pointPolicy("idle-point-v1"))
		if err != nil {
			t.Fatal(err)
		}
		result[member.StateIdentity] = baseline
	}
	return result
}

func testPass(t *testing.T, family CurveFamily, means []float64, idPrefix string) Pass {
	t.Helper()
	members := family.Members()
	pass := Pass{Role: PassDiscovery}
	for index, mean := range means {
		member := members[index]
		batch := index + 1
		batchInput := PassBatch{Batch: batch}
		for trialIndex := 0; trialIndex < 3; trialIndex++ {
			trialID := idPrefix + "-b" + string(rune('0'+batch)) + "-" + string(rune('1'+trialIndex))
			total := mean + 1
			trial := Trial{
				StateIdentity: member.StateIdentity, EnvironmentIdentity: member.EnvironmentIdentity,
				MeasurementProtocolIdentity: member.ActiveProtocolIdentity,
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

func testDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func testImage(name, character string) string {
	return "registry.example/chill/" + name + "@" + testDigest(character)
}

func testStateIdentity(character string) string {
	return "chill.dacs.io/execution-state-identity.v1alpha2@" + testDigest(character)
}

func testEnvironmentIdentity(character string) string {
	return "chill.dacs.io/external-environment-identity.v1alpha1@" + testDigest(character)
}

func testProtocolIdentity(character string) string {
	return "chill.dacs.io/measurement-protocol-identity.v1alpha1@" + testDigest(character)
}
