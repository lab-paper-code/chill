package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func validEvaluationRunSpec() EvaluationRunSpec {
	image := EvaluationImage{Image: "example/harness@sha256:identity", SourceSHA: "source"}
	ref := EvaluationEvidenceReference{Store: "evidence", Key: "start-state", Size: 10}
	return EvaluationRunSpec{
		Question: "Does the policy preserve SLO?", EvidenceStore: "evidence",
		Target:  TargetIdentity{Components: []EvaluationTargetComponent{{Name: "server", Resource: EvaluationResourceReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "chill-system", Name: "server"}, EvaluationImage: image}}},
		Harness: image, LoadGenerator: Generator{EvaluationImage: image, Shards: map[string]int32{"workload": 1}, Barrier: GeneratorBarrier{MinimumReadySamples: 2}},
		PowerMeasurement: PowerMeasurement{Meters: []EvaluationMeter{{ID: "meter", SourceIdentity: "meter-source"}}, Nodes: []EvaluationObjectReference{{Name: "node", UID: "node-uid"}}, Series: "power", Sampling: PowerSampling{IntervalNanos: 1_000_000_000}},
		Control:          ControlSettings{ScaleDownHeadroomFraction: .25, MinimumDemand: map[string]EvaluationNonNegative{"workload-uid": 0}},
		Judgment:         Judgment{GeneratorQuality: GeneratorQuality{LatenessToleranceNanos: 50_000_000, MaxExceedingFraction: .01}, EnergyCoverage: EnergyCoverage{MaxGapFraction: .01, MaxSingleGapSeconds: 30}},
		Stages: []StageTemplate{
			{ID: "first", Row: "E5-3m", Mode: EvaluationModeAutonomous, LoadScenarioRef: EvaluationObjectReference{Name: "load", UID: "load-uid"}, StartState: EvaluationStartState{EvidenceRef: &ref}},
			{ID: "second", Row: "E5-3m", Mode: EvaluationModeFixed, LoadScenarioRef: EvaluationObjectReference{Name: "load", UID: "load-uid"}, StartState: EvaluationStartState{StageResultRef: &EvaluationStageOutputReference{StageID: "first", Output: "start-state"}}},
		},
		Comparisons: []EvaluationComparison{{Left: EvaluationComparisonReference{StageID: "first"}, Right: EvaluationComparisonReference{StageID: "second"}, Reason: "policy effect"}},
	}
}

func TestEvaluationRunValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*EvaluationRunSpec)
		wantError bool
	}{
		{name: "valid", mutate: func(*EvaluationRunSpec) {}},
		{name: "forward stage", mutate: func(s *EvaluationRunSpec) {
			s.Stages[0].StartState = EvaluationStartState{StageResultRef: &EvaluationStageOutputReference{StageID: "second", Output: "state"}}
		}, wantError: true},
		{name: "self stage", mutate: func(s *EvaluationRunSpec) {
			s.Stages[0].StartState = EvaluationStartState{StageResultRef: &EvaluationStageOutputReference{StageID: "first", Output: "state"}}
		}, wantError: true},
		{name: "duplicate ID", mutate: func(s *EvaluationRunSpec) { s.Stages[1].ID = "first" }, wantError: true},
		{name: "invalid DNS label", mutate: func(s *EvaluationRunSpec) { s.Stages[0].ID = "Not-A-Label" }, wantError: true},
		{name: "invalid mode", mutate: func(s *EvaluationRunSpec) { s.Stages[0].Mode = "full" }, wantError: true},
		{name: "missing uncordon predecessor", mutate: func(s *EvaluationRunSpec) {
			s.Stages[0].FailureInjection = []FaultInjectionPlan{{ID: "restore", Action: "uncordon", OffsetSeconds: 30, PriorInjectionID: "missing"}}
		}, wantError: true},
		{name: "missing comparison", mutate: func(s *EvaluationRunSpec) { s.Comparisons[0].Left.StageID = "missing" }, wantError: true},
		{name: "image not pinned", mutate: func(s *EvaluationRunSpec) { s.Harness.Image = "harness:latest" }, wantError: true},
		{name: "out of range", mutate: func(s *EvaluationRunSpec) { s.Judgment.EnergyCoverage.MaxGapFraction = 2 }, wantError: true},
		{name: "valid injections", mutate: func(s *EvaluationRunSpec) {
			s.Stages[0].FailureInjection = []FaultInjectionPlan{{ID: "cordon", Action: "cordon-delete", OffsetSeconds: 10, Selection: &FaultInjectionSelection{WorkloadUIDs: []types.UID{"workload-uid"}, OrderBy: []FaultInjectionOrderKey{"node", "pod", "instance"}}}, {ID: "restore", Action: "uncordon", OffsetSeconds: 30, PriorInjectionID: "cordon"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := validEvaluationRunSpec()
			test.mutate(&s)
			if err := s.Validate(); (err != nil) != test.wantError {
				t.Fatalf("Validate()=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestLoadScenarioValidate(t *testing.T) {
	ref := EvaluationEvidenceReference{Store: "evidence", Key: "input", Size: 10}
	valid := LoadScenarioSpec{Workloads: []EvaluationWorkloadReference{{Name: "workload", UID: "uid", Generation: 1}}, Parts: []LoadScenarioPart{{ID: "steady", DurationSeconds: 10, Rates: map[string]EvaluationNonNegative{"workload": 1}}}, ArrivalDistribution: map[string]string{"workload": "poisson"}, SeedRef: ref, PayloadManifestRef: ref}
	tests := []struct {
		name      string
		mutate    func(*LoadScenarioSpec)
		wantError bool
	}{
		{name: "valid", mutate: func(*LoadScenarioSpec) {}},
		{name: "rates plus alpha", mutate: func(s *LoadScenarioSpec) { s.Parts[0].Alpha = map[string]EvaluationNonNegative{"workload": 1} }, wantError: true},
		{name: "alpha missing evidence", mutate: func(s *LoadScenarioSpec) {
			s.Parts[0].Rates = nil
			s.Parts[0].Alpha = map[string]EvaluationNonNegative{"workload": 1}
		}, wantError: true},
		{name: "alpha with evidence", mutate: func(s *LoadScenarioSpec) {
			s.Parts[0].Rates = nil
			s.Parts[0].Alpha = map[string]EvaluationNonNegative{"workload": 1}
			s.RateBasisRef = &ref
			s.KappaRef = &ref
		}},
		{name: "unknown workload", mutate: func(s *LoadScenarioSpec) { s.Parts[0].Rates = map[string]EvaluationNonNegative{"missing": 1} }, wantError: true},
		{name: "duplicate part", mutate: func(s *LoadScenarioSpec) { s.Parts = append(s.Parts, s.Parts[0]) }, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := valid.DeepCopy()
			test.mutate(s)
			if err := s.Validate(); (err != nil) != test.wantError {
				t.Fatalf("Validate()=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestAggregateMeasurement(t *testing.T) {
	for _, test := range []struct {
		name     string
		verdicts []MeasurementVerdict
		status   metav1.ConditionStatus
		verdict  MeasurementVerdict
	}{
		{"invalid precedes incomplete and fail", []MeasurementVerdict{MeasurementFAIL, MeasurementNotEvaluated, MeasurementINVALID}, metav1.ConditionFalse, MeasurementINVALID},
		{"incomplete precedes fail", []MeasurementVerdict{MeasurementFAIL, MeasurementNotEvaluated, MeasurementPASS}, metav1.ConditionUnknown, MeasurementNotEvaluated},
		{"fail precedes pass", []MeasurementVerdict{MeasurementPASS, MeasurementFAIL}, metav1.ConditionFalse, MeasurementFAIL},
		{"all pass", []MeasurementVerdict{MeasurementPASS, MeasurementPASS}, metav1.ConditionTrue, MeasurementPASS},
		{"empty", nil, metav1.ConditionUnknown, MeasurementNotEvaluated},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, verdict := AggregateMeasurement(test.verdicts...)
			if status != test.status || verdict != test.verdict {
				t.Fatalf("got %s/%s, want %s/%s", status, verdict, test.status, test.verdict)
			}
		})
	}
}

