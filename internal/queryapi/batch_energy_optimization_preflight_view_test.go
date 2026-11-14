package queryapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildBatchEnergyOptimizationPreflightProjectsAResolvedSession(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	campaign := edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "campaign"},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{TargetNode: "orangepi"},
		},
	}

	view, err := BuildBatchEnergyOptimizationPreflight(BatchEnergyOptimizationPreflightSnapshot{
		ReadAt: readAt, Campaign: campaign,
		SessionPlan: &SessionPreflightPlan{PlannedBatchSizes: []int{1, 2, 4}, MinimumSamples: 59, SLOPercentile: 95},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.APIVersion != BatchEnergyOptimizationPreflightAPIVersion ||
		view.Kind != "BatchEnergyOptimizationPreflight" || !view.ReadAt.Equal(readAt.UTC()) ||
		view.ReadAt.Location() != time.UTC || !view.Resolves || view.Reason != "" {
		t.Fatalf("envelope = %#v", view)
	}
	if view.Campaign.Namespace != campaign.Namespace || view.Campaign.Name != campaign.Name ||
		view.Campaign.Spec.State.TargetNode != campaign.Spec.State.TargetNode {
		t.Fatalf("campaign = %#v", view.Campaign)
	}
	want := []int{1, 2, 4}
	if view.BatchPlan.ConditionMatch != "planned-only" || !view.BatchPlan.SearchMayStopEarly ||
		!reflect.DeepEqual(view.BatchPlan.PlannedBatches, want) || !reflect.DeepEqual(view.BatchPlan.MissingBatches, want) {
		t.Fatalf("batch plan = %#v", view.BatchPlan)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["reason"]; exists {
		t.Fatalf("resolved encoding carries a reason: %s", encoded)
	}
}

func TestBuildBatchEnergyOptimizationPreflightProjectsAnUnresolvedIntent(t *testing.T) {
	t.Parallel()
	const reason = " referenced DeviceClass does not exist "

	view, err := BuildBatchEnergyOptimizationPreflight(BatchEnergyOptimizationPreflightSnapshot{
		ReadAt: time.Now(), Reason: reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Resolves || view.Reason != reason || view.Session != nil {
		t.Fatalf("unresolved preflight = %#v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		BatchPlan MeasurementBatchPlan `json:"batchPlan"`
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.BatchPlan.ConditionMatch != "unknown" || len(fields.BatchPlan.Reasons) != 1 ||
		fields.BatchPlan.Reasons[0].Code != "PlanUnresolved" || !strings.Contains(string(encoded), `"plannedBatches":[]`) {
		t.Fatalf("unresolved batch plan = %s", encoded)
	}
}

func TestBuildBatchEnergyOptimizationPreflightRejectsContradictorySnapshots(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		snapshot BatchEnergyOptimizationPreflightSnapshot
		match    string
	}{
		"resolved with reason": {
			snapshot: BatchEnergyOptimizationPreflightSnapshot{
				SessionPlan: &SessionPreflightPlan{}, Reason: "resolution failed",
			},
			match: "resolved preflight carries no reason",
		},
		"unresolved without reason": {
			snapshot: BatchEnergyOptimizationPreflightSnapshot{},
			match:    "unresolved preflight requires a reason",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildBatchEnergyOptimizationPreflight(test.snapshot); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("BuildBatchEnergyOptimizationPreflight() error = %v, want %q", err, test.match)
			}
		})
	}
}
