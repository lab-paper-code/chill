package kube

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWorkloadSummaryPreservesStateAndBoundsRepeatedEvidence(t *testing.T) {
	profiles := make([]SupportedProfile, 259)
	for i := range profiles {
		profiles[i] = SupportedProfile{ID: fmt.Sprintf("profile-%03d", i), Evidence: []evidencestore.ObjectIdentity{{Key: strings.Repeat("e", 1024)}}}
	}
	views := make([]WorkloadView, 40)
	for i := range views {
		views[i] = WorkloadView{Desired: &api.Workload{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("service-%02d", i)}}, SupportedInputs: []SupportedInput{{ExecutionPath: "summary-path", Artifact: "onnx", Profiles: profiles}}, Admission: true, Readiness: "Ready", Reason: "Ready", Pending: 2, Assigned: 3, ObservedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	}
	original, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	summary := SummarizeWorkloads(views)
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if len(original) <= 8388608 || len(encoded) >= 8388608 {
		t.Fatalf("relay size regression: full=%d summary=%d", len(original), len(encoded))
	}
	var fullWire, summaryWire []map[string]json.RawMessage
	if err := json.Unmarshal(original, &fullWire); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &summaryWire); err != nil {
		t.Fatal(err)
	}
	for _, workload := range summaryWire {
		var inputs []map[string]json.RawMessage
		if err := json.Unmarshal(workload["supportedInputs"], &inputs); err != nil {
			t.Fatal(err)
		}
		for _, input := range inputs {
			if _, duplicate := input["profileCount"]; duplicate {
				t.Fatal("profileCount duplicates the profiles array length")
			}
		}
	}
	for i := range summary {
		input := summary[i].SupportedInputs[0]
		if input.ExecutionPath != "summary-path" || input.Artifact != "onnx" || len(input.Profiles) != 259 {
			t.Fatalf("input contract lost: %+v", input)
		}
		for j, profile := range input.Profiles {
			if profile.ID != profiles[j].ID {
				t.Fatalf("profile order/identity changed: %d", j)
			}
		}
		delete(fullWire[i], "supportedInputs")
		delete(summaryWire[i], "supportedInputs")
		if !reflect.DeepEqual(fullWire[i], summaryWire[i]) {
			t.Fatalf("non-profile fields changed for %d", i)
		}
	}
	if strings.Contains(string(encoded), "\"evidence\"") || strings.Contains(string(encoded), "\"condition\"") || strings.Contains(string(encoded), "\"reference\"") {
		t.Fatal("full profile details leaked into summary")
	}
	after, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("summary mutated the full list/detail contract")
	}
	if empty, err := json.Marshal(SummarizeWorkloads(nil)); err != nil || string(empty) != "[]" {
		t.Fatalf("empty list contract: %s %v", empty, err)
	}
}
