package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"github.com/lab-paper-code/chill/test/fixtures"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestR6ProfileResponseOmitsRawCatalogAndLatencySamples(t *testing.T) {
	view := httpapi.ProfilesView{ConfigVersion: "rv"}
	samples := make([]firstfloor.ServiceSample, 100000)
	for i := range samples {
		samples[i] = fixtures.ServiceSample("physical-trial", uint64(i), 1000)
	}
	view.Profiles = []serving.Profile{{
		ID: "profile", Batches: []serving.BatchProfile{{Batch: 1, ServiceSamples: samples}},
	}}
	view.MeasurementFacts = map[string]serving.MeasurementFacts{"unselected": {}}
	raw, err := (&measurementService{}).profilePayload(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"serviceSamples", "measurementFacts"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("response contains internal/raw %s", key)
		}
	}
	// preparations is the catalog restore contract (관측 원시 기록 내보내기; targeted restoration)
	// and must survive the query projection.
	if !strings.Contains(string(raw), `"preparations"`) {
		t.Error("response dropped preparations")
	}
	if len(view.Profiles[0].Batches[0].ServiceSamples) != 100000 {
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
	var assessment queryapi.MeasurementAssessment
	if len(list.Items) != 1 {
		t.Fatalf("missing record dropped: %+v", list)
	}
	if err := json.Unmarshal(list.Items[0].Assessment, &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.DisplayStatus != queryapi.MeasurementProblem || assessment.Evidence.Accepted ||
		assessment.Evidence.Available || len(assessment.Reasons) == 0 ||
		assessment.Reasons[0].Code != "OccupancyEvidenceMissing" {
		t.Fatalf("missing evidence falsely accepted or hidden: %+v", assessment)
	}
}

func TestR6FailureSummaryClassifiesOwnedRuntimeLog(t *testing.T) {
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
	base := &queryapi.CoverageBase{
		Requests: api.BatchEnergyOptimizationList{Items: []api.BatchEnergyOptimization{task}},
		Runs:     api.ProfilingRunList{Items: []api.ProfilingRun{run}},
		Inventory: queryapi.InventoryView{
			Models: []queryapi.InventoryModelView{
				{Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{}}}},
			},
			Classes: []queryapi.InventoryClassView{{PowerModes: []queryapi.InventoryPowerModeView{{}}}},
		},
	}
	assessment := queryapi.AssessMeasurementRequest(task, queryapi.CoverageCatalog{}, queryapi.CoverageCatalog{})
	failures := queryapi.MeasurementRequestFailures(
		task, queryapi.CoverageOwnedRuns(base.Requests.Items, base.Runs.Items)[string(task.UID)], assessment,
	)
	projection := queryapi.BuildMeasurementCoverage(base, task.Namespace, queryapi.CoverageQuery{})
	cell := projection.Cells[0]
	foreign := *run.DeepCopy()
	foreign.Namespace = "outside"
	got := queryapi.MeasurementRequestFailures(task, []api.ProfilingRun{foreign}, assessment)
	if len(got) != 1 || got[0].RunID != "" {
		t.Fatalf("foreign Run borrowed as failure cause: %+v", got)
	}
	if len(cell.FailureSummary) == 0 {
		t.Fatal("failure summary absent")
	}
	last := cell.FailureSummary[len(cell.FailureSummary)-1]
	if last.RunID != string(run.UID) || last.ExitCode == nil || *last.ExitCode != 4 ||
		last.Code != "PowerObservationTimeout" || last.Label != "전력 관측 시간 초과" ||
		last.Message != "제한 시간 안에 전력 관측을 완료하지 못했습니다." {
		t.Fatalf("lost runtime cause: %+v", last)
	}
	service := &measurementService{
		client: fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(&run).Build(),
	}
	list, err := querykube.AssessMeasurementRequests(
		context.Background(),
		service.client,
		queryCatalogSource(service.catalog),
		base.Requests,
	)
	if err != nil || len(list) != 1 {
		t.Fatalf("request list assessment = %s, %v", list, err)
	}
	var listAssessment queryapi.MeasurementAssessment
	if err := json.Unmarshal(list[0], &listAssessment); err != nil {
		t.Fatal(err)
	}
	for _, summary := range [][]queryapi.MeasurementFailure{
		failures, projection.Entities.Requests[0].FailureSummary,
		listAssessment.FailureSummary,
		queryapi.AssessProfilingRun(run).FailureSummary,
	} {
		if len(summary) != 1 || summary[0].Code != last.Code || summary[0].Label != last.Label ||
			summary[0].Message != last.Message || summary[0].RunID != last.RunID {
			t.Fatalf("failure classification differs across projections: %+v; want %+v", summary, last)
		}
	}
}
