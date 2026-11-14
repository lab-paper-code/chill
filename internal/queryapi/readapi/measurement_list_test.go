package readapi

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMeasurementListRowCarriesSessionStatusAndReason(t *testing.T) {
	t.Parallel()
	list := api.BatchEnergyOptimizationList{Items: []api.BatchEnergyOptimization{{
		ObjectMeta: meta.ObjectMeta{Name: "session", Generation: 1},
		Spec:       api.BatchEnergyOptimizationSpec{Session: &api.BatchEnergyOptimizationSessionSpec{}},
		Status: api.BatchEnergyOptimizationStatus{ObservedGeneration: 1, Phase: "Failed", Outcome: "MeterUndeclared",
			CompletedBatches: 2, Conditions: []meta.Condition{{Type: "MeasurementComplete", Status: meta.ConditionFalse,
				Reason: "MeterUndeclared", Message: "no wall meter is configured for Node jetsono", ObservedGeneration: 1}}},
	}}}
	encoded, err := json.Marshal(ProjectMeasurementList(list, []json.RawMessage{json.RawMessage(`{}`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"completedBatches":2`, `"outcome":"MeterUndeclared"`, `"phase":"Failed"`,
		`"reason":"no wall meter is configured for Node jetsono"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("row %s lacks %s", encoded, want)
		}
	}
}
