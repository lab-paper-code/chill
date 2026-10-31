package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func TestSchedulerSummaryWhitelist(t *testing.T) {
	at := time.Date(2026, 10, 25, 1, 2, 3, 4, time.UTC)
	seconds := 2.5
	state := servingkube.SchedulerControllerState{
		Snapshot: serving.SchedulerSnapshot{At: at, Instances: []serving.SchedulerInstance{{ID: "i", OwnerWorkloadUID: "owner", WorkloadUID: "allocated", ProfileID: "profile", Ready: true, Routable: map[string]bool{"allocated": false}, Reasons: []string{"PolicyAckPending"}}}},
		Decision: serving.SchedulerDecision{At: at, Status: "Selected", Target: &serving.SchedulerCandidate{ID: "target"}},
		Actions:  []serving.Action{{ID: "running", Kind: "prepare", Status: "running", Elapsed: time.Second, ExpectedSeconds: &seconds, ExpectedSource: &serving.ActionExpectedSource{CostID: "cost", Statistic: "mean", TrialCount: 2}}, {ID: "old", Status: "complete"}},
	}
	small, err := json.Marshal(summarizeScheduler(&state))
	if err != nil {
		t.Fatal(err)
	}
	state.Snapshot.Options = make([]serving.SchedulerOption, 10000)
	large, err := json.Marshal(summarizeScheduler(&state))
	if err != nil {
		t.Fatal(err)
	}
	if string(small) != string(large) {
		t.Fatal("summary grows with options")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(large, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 6 {
		t.Fatalf("unexpected top-level keys: %s", large)
	}
	for _, forbidden := range []string{`"snapshot"`, `"options"`, `"profile":`, `"costs"`, `"results"`, `"capacity"`} {
		if strings.Contains(string(large), forbidden) {
			t.Fatalf("leaked %s: %s", forbidden, large)
		}
	}
	summary := summarizeScheduler(&state)
	if summary.Decision.ID != at.Format(time.RFC3339Nano) || summary.Decision.TargetID != "target" || len(summary.Actions) != 1 || summary.Actions[0].ExpectedSeconds == nil || *summary.Actions[0].ExpectedSeconds != 2.5 || summary.Instances[0].Routable["allocated"] {
		t.Fatalf("projection lost authoritative fields: %+v", summary)
	}
	if len(summary.Instances[0].Reasons) != 1 || summary.Instances[0].Reasons[0] != "PolicyAckPending" {
		t.Fatalf("summary hid policy ACK waiting: %+v", summary.Instances[0])
	}
}

func TestSchedulerSummaryCountsOnlyExecutingChanges(t *testing.T) {
	state := servingkube.SchedulerControllerState{Actions: []serving.Action{
		{ID: "plan", Status: "planned"}, {ID: "start", Status: "starting"},
		{ID: "run", Status: "running"}, {ID: "done", Status: "complete"},
		{ID: "error", Status: "failed"}, {ID: "cancel", Status: "cancel"},
	}}
	got := summarizeScheduler(&state).Actions
	if len(got) != 2 || got[0].ID != "start" || got[1].ID != "run" {
		t.Fatalf("plans or terminal history counted as executing changes: %+v", got)
	}
}

func TestSchedulerSummaryViewValidation(t *testing.T) {
	for _, tc := range []struct {
		path  string
		valid bool
	}{
		{"/api/v1/scheduler/state?view=summary", true},
		{"/api/v1/scheduler/state?view=compact", true},
		{"/api/v1/scheduler/state", true},
		{"/api/v1/scheduler?view=summary", false},
		{"/api/v1/observations/scheduler?view=summary", true},
		{"/api/v1/scheduler/state?view=summary&view=summary", false},
		{"/api/v1/observations/scheduler?view=summary&view=compact", false},
		{"/api/v1/observations/load?view=summary", false},
		{"/api/v1/scheduler/state?view=full", false},
	} {
		_, err := schedulerCompactQuery(httptest.NewRequest("GET", tc.path, nil))
		if (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.path, err)
		}
	}
}
