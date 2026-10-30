package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestR6ProfileResponseOmitsRawCatalogAndLatencySamples(t *testing.T) {
	view := httpapi.ProfilesView{ConfigVersion: "rv"}
	view.Profiles = []serving.Profile{{
		ID: "profile", Batches: []serving.BatchProfile{{Batch: 1, DurationNanos: make([]float64, 100000)}},
	}}
	view.MeasurementFacts = map[string]serving.MeasurementFacts{"unselected": {}}
	raw, err := (&measurementService{}).decorateProfiles(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"durationNanos", "measurementFacts", "preparations"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("response contains internal/raw %s", key)
		}
	}
	if len(view.Profiles[0].Batches[0].DurationNanos) != 100000 {
		t.Fatal("read projection mutated scheduler samples")
	}
}

func TestR6MissingOccupancyEvidenceDoesNotHideWholeList(t *testing.T) {
	cr := &api.DispatchOccupancyMeasurement{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "missing", UID: "uid", Generation: 1},
		Status: api.DispatchOccupancyMeasurementStatus{Phase: api.MeasurementPhaseComplete,
			Result: &api.ProfilingRunEvidenceReference{Store: "evidence", Key: "missing.json"}},
	}
	service := &measurementService{
		client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(cr).Build(),
		objects: &operationProjectionStore{err: evidencestore.ErrNotFound},
		opts:    options{servingNamespace: "allowed", maxEvidenceBytes: 1 << 20},
	}
	list, err := service.listDispatchOccupancies(context.Background(), "allowed")
	if err != nil {
		t.Fatalf("one missing object hid native list: %v", err)
	}
	var assessment operatorconsole.MeasurementAssessment
	if len(list.Items) != 1 {
		t.Fatalf("missing record dropped: %+v", list)
	}
	if err := json.Unmarshal(list.Items[0].Assessment, &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.DisplayStatus != operatorconsole.MeasurementProblem || assessment.Evidence.Accepted ||
		assessment.Evidence.Available || len(assessment.Reasons) == 0 ||
		assessment.Reasons[0].Code != "OccupancyEvidenceMissing" {
		t.Fatalf("missing evidence falsely accepted or hidden: %+v", assessment)
	}
}

func TestR6FailureSummaryUsesOwnedRuntimeLog(t *testing.T) {
	controller := true
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "request", UID: "request-uid", Generation: 1},
		Status:     api.BatchEnergyOptimizationStatus{Phase: "Failed", ObservedGeneration: 1},
	}
	run := api.ProfilingRun{
		ObjectMeta: meta.ObjectMeta{
			Namespace: task.Namespace, Name: "run", UID: "run-uid", Generation: 1,
			OwnerReferences: []meta.OwnerReference{{
				APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization",
				Name: task.Name, UID: task.UID, Controller: &controller,
			}},
		},
		Status: api.ProfilingRunStatus{
			ObservedGeneration: 1, Conditions: []meta.Condition{{
				Type: "ExecutionComplete", Status: meta.ConditionFalse, Reason: "JobFailed",
				Message: "Job failed", ObservedGeneration: 1,
			}},
			RuntimeTermination: &api.ProfilingRunRuntimeTermination{
				ExitCode: 4,
				LogTail:  "starting\nmetric observation failed: wait for metric result file: context deadline exceeded\n",
			},
		},
	}
	base := &measurementCoverageBase{
		requests: api.BatchEnergyOptimizationList{Items: []api.BatchEnergyOptimization{task}},
		runs:     api.ProfilingRunList{Items: []api.ProfilingRun{run}},
	}
	assessment := assessMeasurementRequest(task, httpapi.ProfilesView{})
	failures := operatorconsole.MeasurementRequestFailures(task, coverageOwnedRuns(base)[string(task.UID)], assessment)
	cell := operatorconsole.MeasurementCoverageCell{FailureSummary: failures}
	snapshot := operatorconsole.BatchEnergyOptimizationResultSnapshot{Campaign: task, FailedRuns: []api.ProfilingRun{run}}
	detail, err := operatorconsole.BuildBatchEnergyOptimizationResult(snapshot)
	if err != nil || detail.Assessment.DisplayStatus != operatorconsole.MeasurementProblem ||
		len(detail.Assessment.FailureSummary) != 1 || detail.MeasurementFactsReason == nil ||
		*detail.MeasurementFactsReason != "RequestFailed" {
		t.Fatalf("failed request detail became pending/empty: %+v %v", detail, err)
	}
	foreign := *run.DeepCopy()
	foreign.Namespace = "outside"
	got := operatorconsole.MeasurementRequestFailures(task, []api.ProfilingRun{foreign}, detail.Assessment)
	if len(got) != 1 || got[0].RunID != "" {
		t.Fatalf("foreign Run borrowed as failure cause: %+v", got)
	}
	if len(cell.FailureSummary) == 0 {
		t.Fatal("failure summary absent")
	}
	last := cell.FailureSummary[len(cell.FailureSummary)-1]
	if last.RunID != string(run.UID) || last.ExitCode == nil || *last.ExitCode != 4 ||
		!strings.Contains(last.Message, "metric observation failed") || strings.Contains(last.Message, "\n") {
		t.Fatalf("lost runtime cause: %+v", last)
	}
}
