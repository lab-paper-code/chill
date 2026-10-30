package readapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRunFiltersApplyToEntireSnapshotBeforePagination(t *testing.T) {
	controller := true
	selected := api.ProfilingRun{
		ObjectMeta: meta.ObjectMeta{Namespace: "ns", Name: "matching", UID: "run-uid", Generation: 1,
			OwnerReferences: []meta.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization", UID: "parent-uid", Controller: &controller}}},
		Status: api.ProfilingRunStatus{ObservedGeneration: 1, Conditions: []meta.Condition{{Type: "ExecutionComplete", Status: meta.ConditionFalse, Reason: "JobFailed", ObservedGeneration: 1}}},
	}
	selected.Spec.Candidate = api.ProfilingRunCandidateSpec{ModelSpec: "model", DeviceClass: "device", ExecutionPath: "path", PowerMode: "mode"}
	selected.Spec.TargetNode = "node"
	quota := resource.MustParse("2")
	selected.Spec.Measurement.CPUQuota = &quota
	snapshot := operatorconsole.ProfilingRunsSnapshot{Namespace: "ns"}
	for range 30 {
		snapshot.Runs = append(snapshot.Runs, api.ProfilingRun{ObjectMeta: meta.ObjectMeta{Namespace: "ns", Name: "other"}})
	}
	snapshot.Runs = append(snapshot.Runs, selected)
	handler := NewQueryHandler(QueryReaders{ProfilingRuns: func(context.Context, string) (operatorconsole.ProfilingRunsSnapshot, error) { return snapshot, nil }})
	for key, value := range map[string]string{"model": "model", "deviceClass": "device", "node": "node", "path": "path", "powerMode": "mode", "cpu": "2", "status": "problem", "parentUID": "parent-uid"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/ns/profiling-runs?limit=1&"+url.Values{key: {value}}.Encode(), nil))
		var got struct {
			operatorconsole.ProfilingRunsView
			Page queryPage `json:"page"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || got.Page.Total != 1 || len(got.Runs) != 1 || got.Runs[0].UID != "run-uid" {
			t.Fatalf("%s ignored/outside first page: %d %s", key, response.Code, response.Body.String())
		}
	}
	if snapshot.Runs[0].Name != "other" {
		t.Fatal("filter changed shared source snapshot")
	}
	encoded, _ := json.Marshal(selected.Spec)
	var group map[string]any
	if err := json.Unmarshal(encoded, &group); err != nil {
		t.Fatal(err)
	}
	group["namespace"] = "ns"
	delete(group["measurement"].(map[string]any), "batch")
	encoded, _ = json.Marshal(group)
	canonical, valid := canonicalRunGroup(string(encoded))
	if !valid || !matchesRunFilters(selected, map[string]string{"group": canonical}) {
		t.Fatal("group matching lost condition fields")
	}
	selected.Spec.Measurement.Batch = 16
	if !matchesRunFilters(selected, map[string]string{"group": canonical}) {
		t.Fatal("batch should not split condition groups")
	}
	selected.Spec.TargetNode = "different"
	if matchesRunFilters(selected, map[string]string{"group": canonical}) {
		t.Fatal("different frozen node merged into group")
	}
}
