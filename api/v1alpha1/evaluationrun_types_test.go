package v1alpha1

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func validEvaluationRunSpec() EvaluationRunSpec {
	image := EvaluationImage{Image: "example/harness@sha256:identity", SourceSHA: "source"}
	return EvaluationRunSpec{
		Question: "Does the policy preserve SLO?", EvidenceStore: "evidence",
		Target:  TargetIdentity{Components: []EvaluationTargetComponent{{Name: "server", Resource: EvaluationResourceReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "chill-system", Name: "server"}}}},
		Harness: image, LoadGenerator: Generator{EvaluationImage: image, NodeName: "genesis1", Shards: map[string]int32{"workload": 1}, Barrier: GeneratorBarrier{MinimumReadySamples: 2}},
		PowerMeasurement: PowerMeasurement{Meters: []EvaluationMeter{{ID: "meter", SourceIdentity: "meter-source"}}, Nodes: []EvaluationObjectReference{{Name: "node", UID: "node-uid"}}, Series: "power", Sampling: PowerSampling{IntervalNanos: 1_000_000_000}},
		Judgment:         Judgment{GeneratorQuality: GeneratorQuality{LatenessToleranceNanos: 50_000_000, MaxExceedingFraction: .01}, EnergyCoverage: EnergyCoverage{MaxGapFraction: .01, MaxSingleGapSeconds: 30}},
		Stages: []StageTemplate{
			{ID: "first", Row: "E5-3m", LoadScenarioRef: EvaluationObjectReference{Name: "load", UID: "load-uid"}},
			{ID: "second", Row: "E5-3m", LoadScenarioRef: EvaluationObjectReference{Name: "load", UID: "load-uid"}},
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
		{name: "missing generator node", mutate: func(s *EvaluationRunSpec) { s.LoadGenerator.NodeName = "" }, wantError: true},
		{name: "public patch", mutate: func(s *EvaluationRunSpec) {
			s.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`{"controls":{"power":false}}`)}
		}},
		{name: "patch is opaque here", mutate: func(s *EvaluationRunSpec) {
			s.Stages[0].SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`{"operatorSetting":true}`)}
		}},
		{name: "patch array", mutate: func(s *EvaluationRunSpec) { s.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`[]`)} }, wantError: true},
		{name: "patch null", mutate: func(s *EvaluationRunSpec) { s.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`null`)} }, wantError: true},
		{name: "patch bad JSON", mutate: func(s *EvaluationRunSpec) { s.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`{"controls":`)} }, wantError: true},
		{name: "duplicate ID", mutate: func(s *EvaluationRunSpec) { s.Stages[1].ID = "first" }, wantError: true},
		{name: "invalid DNS label", mutate: func(s *EvaluationRunSpec) { s.Stages[0].ID = "Not-A-Label" }, wantError: true},
		{name: "missing uncordon predecessor", mutate: func(s *EvaluationRunSpec) {
			s.Stages[0].FailureInjection = []FaultInjectionPlan{{ID: "restore", Action: "uncordon", OffsetSeconds: 30, PriorInjectionID: "missing"}}
		}, wantError: true},
		{name: "missing comparison", mutate: func(s *EvaluationRunSpec) { s.Comparisons[0].Left.StageID = "missing" }, wantError: true},
		{name: "image not pinned", mutate: func(s *EvaluationRunSpec) { s.Harness.Image = "harness:latest" }, wantError: true},
		{name: "out of range", mutate: func(s *EvaluationRunSpec) { s.Judgment.EnergyCoverage.MaxGapFraction = 2 }, wantError: true},
		{name: "valid injections", mutate: func(s *EvaluationRunSpec) {
			s.Stages[0].FailureInjection = []FaultInjectionPlan{{ID: "cordon", Action: "cordon-delete", OffsetSeconds: 10, Selection: &FaultInjectionSelection{WorkloadUIDs: []types.UID{"workload-uid"}, OrderBy: []FaultInjectionOrderKey{"node", "pod", "podUID"}}}, {ID: "restore", Action: "uncordon", OffsetSeconds: 30, PriorInjectionID: "cordon"}}
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

func TestHistoricalPublicationReference(t *testing.T) {
	for _, id := range []string{"control-222-223-first-20261030", "A_b.c-1", "_", "-", "", ".", "..", ".hidden", "a/b", "a%20b", "a b", "한글"} {
		t.Run(id, func(t *testing.T) {
			spec := validEvaluationRunSpec()
			spec.Comparisons[0].Right = EvaluationComparisonReference{Historical: &EvaluationHistoricalReference{PublicationID: id, StageID: "baseline"}}
			valid := id == "control-222-223-first-20261030" || id == "A_b.c-1" || id == "_" || id == "-"
			if err := spec.Validate(); (err == nil) != valid {
				t.Fatalf("publicationID %q: %v", id, err)
			}
			spec.Comparisons[0].Right.Historical.StageID = ""
			if err := spec.Validate(); err == nil {
				t.Fatal("missing historical stage accepted")
			}
		})
	}
}

func TestLoadScenarioValidate(t *testing.T) {
	valid := LoadScenarioSpec{Workloads: []EvaluationWorkloadReference{{Name: "workload", UID: "uid", Generation: 1}}, Parts: []LoadScenarioPart{{ID: "steady", DurationSeconds: 10, Rates: map[string]EvaluationNonNegative{"workload": 1}}}, ArrivalDistribution: map[string]string{"workload": "poisson"}, Seeds: map[string]EvaluationSeed{"workload": "0"}, DatasetRefs: map[string]EvaluationObjectReference{"workload": {Name: "dataset", UID: "dataset-uid"}}}
	tests := []struct {
		name      string
		mutate    func(*LoadScenarioSpec)
		wantError bool
	}{
		{name: "valid", mutate: func(*LoadScenarioSpec) {}},
		{name: "missing rates", mutate: func(s *LoadScenarioSpec) { s.Parts[0].Rates = nil }, wantError: true},
		{name: "max seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = "18446744073709551615" }},
		{name: "seed overflow", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = "18446744073709551616" }, wantError: true},
		{name: "negative seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = "-1" }, wantError: true},
		{name: "signed seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = "+1" }, wantError: true},
		{name: "fraction seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = "1.0" }, wantError: true},
		{name: "exponent seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = "1e2" }, wantError: true},
		{name: "space seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = " 1" }, wantError: true},
		{name: "empty seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["workload"] = "" }, wantError: true},
		{name: "missing seed", mutate: func(s *LoadScenarioSpec) { delete(s.Seeds, "workload") }, wantError: true},
		{name: "foreign seed", mutate: func(s *LoadScenarioSpec) { s.Seeds = map[string]EvaluationSeed{"other": "1"} }, wantError: true},
		{name: "extra seed", mutate: func(s *LoadScenarioSpec) { s.Seeds["other"] = "1" }, wantError: true},
		{name: "missing dataset", mutate: func(s *LoadScenarioSpec) { s.DatasetRefs = nil }, wantError: true},
		{name: "foreign dataset", mutate: func(s *LoadScenarioSpec) {
			s.DatasetRefs = map[string]EvaluationObjectReference{"other": {Name: "dataset", UID: "uid"}}
		}, wantError: true},
		{name: "dataset missing UID", mutate: func(s *LoadScenarioSpec) { s.DatasetRefs["workload"] = EvaluationObjectReference{Name: "dataset"} }, wantError: true},
		{name: "dataset bad name", mutate: func(s *LoadScenarioSpec) {
			s.DatasetRefs["workload"] = EvaluationObjectReference{Name: "Not a name", UID: "uid"}
		}, wantError: true},
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
