package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRequestAssessmentUsesCatalogIdentityNotCompletePhase(t *testing.T) {
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "old", UID: "old-uid", Generation: 1},
		Spec:       api.BatchEnergyOptimizationSpec{BatchSizes: []int32{1, 2}},
		Status:     api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
	}
	view := httpapi.ProfilesView{}
	if got := assessMeasurementRequest(task, view); got.Evidence.Accepted ||
		got.DisplayStatus == operatorconsole.MeasurementUsable {
		t.Fatalf("phase accepted without evidence: %+v", got)
	}
	view.Profiles = []serving.Profile{{
		ID: "allowed/old/old-uid", Reference: evidencestore.ObjectIdentity{Store: "evidence", Key: "result"},
		Batches: []serving.BatchProfile{{Batch: 1}},
	}}
	if got := assessMeasurementRequest(task, view); got.Status != operatorconsole.MeasurementPartial ||
		!got.Evidence.Accepted {
		t.Fatalf("partial accepted profile: %+v", got)
	}
	task.Spec.BatchSizes = []int32{1}
	if got := assessMeasurementRequest(task, view); got.Status != liveOperationComplete ||
		got.Scope.SchedulerUsed == nil || !*got.Scope.SchedulerUsed {
		t.Fatalf("published evidence: %+v", got)
	}
	newer := task
	newer.Name, newer.UID, newer.Status.Phase = "newer", "newer-uid", measurementPhaseFailed
	if got := assessMeasurementRequest(newer, view); got.Status != operatorconsole.MeasurementFailed ||
		got.Evidence.Accepted {
		t.Fatalf("new request borrowed old evidence: %+v", got)
	}
	if got := assessMeasurementRequest(task, view); !got.Evidence.Accepted {
		t.Fatal("new failure hid old accepted evidence")
	}
	view.Insufficiencies = []servingkube.CatalogIssue{{
		Source: types.NamespacedName{Namespace: task.Namespace, Name: task.Name},
		Code:   "InvalidEvidence", Reason: "real validation failure",
	}}
	if got := assessMeasurementRequest(task, view); got.DisplayStatus != operatorconsole.MeasurementProblem {
		t.Fatalf("catalog issue ignored: %+v", got)
	}
	view.Profiles = nil
	view.MeasurementFacts = map[string]serving.MeasurementFacts{"retained": {
		Request: profilederivation.MeasurementRequestReference{
			Namespace: task.Namespace, Name: task.Name, UID: string(task.UID),
		},
		ResultReference: profilederivation.RawEvidenceIdentity{Store: "evidence", Key: "retained-result"},
		Batches:         []profilederivation.MeasurementBatchFacts{{Batch: 1}},
	}}
	view.Insufficiencies = []servingkube.CatalogIssue{{
		Source: types.NamespacedName{Namespace: task.Namespace, Name: task.Name},
		Code:   "ReplacementSelection", Reason: "superseded by newer; original evidence retained",
	}}
	if got := assessMeasurementRequest(task, view); got.DisplayStatus != operatorconsole.MeasurementUsable ||
		!got.Evidence.Accepted || got.Scope.SchedulerUsed == nil || *got.Scope.SchedulerUsed {
		t.Fatalf("superseded verified result became problem or scheduler-used: %+v", got)
	}
}

func TestOperationAssessmentRequiresStoredAcceptedSteps(t *testing.T) {
	cr := api.OperationCostMeasurement{
		ObjectMeta: meta.ObjectMeta{Generation: 1},
		Spec:       api.OperationCostMeasurementSpec{Steps: []api.OperationCostStep{{Name: "idle"}}},
		Status: api.OperationCostMeasurementStatus{
			Phase: api.MeasurementPhaseComplete,
			Steps: []api.OperationCostStepStatus{{Name: "idle", Key: "stored", Verdict: measurementVerdictAccepted}},
		},
	}
	if got := assessOperationCost(cr, []servingkube.OperationStepProjection{{}}); got.Evidence.Accepted {
		t.Fatalf("status-only step accepted: %+v", got)
	}
	duration, joules := int64(100), 4.0
	steps := []servingkube.OperationStepProjection{{
		OperationID: "uid.0.idle", DurationNanos: &duration, WallJoules: &joules,
	}}
	if got := assessOperationCost(cr, steps); got.Status != liveOperationComplete ||
		!got.Evidence.Accepted || got.Execution.ObservedGeneration != 0 {
		t.Fatalf("verified operation: %+v", got)
	}
	cr.Status.Phase = api.MeasurementPhaseFailed
	cr.Status.Steps[0].Verdict = measurementPhaseFailed
	if got := assessOperationCost(cr, steps); got.Status != operatorconsole.MeasurementFailed || got.Evidence.Accepted {
		t.Fatalf("failed operation: %+v", got)
	}
}

func TestOccupancyAssessmentReadsExactRecordAndPreservesNegativeD(t *testing.T) {
	for _, d := range []int64{100, -100} {
		cr := api.DispatchOccupancyMeasurement{
			ObjectMeta: meta.ObjectMeta{
				Namespace: "allowed", Name: "occupancy", UID: "request-uid", Generation: 1,
			},
			Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Batch: 1},
			Status: api.DispatchOccupancyMeasurementStatus{
				Phase: api.MeasurementPhaseComplete, PerRequestNanos: &d, Throughput: "10",
				Result: &api.ProfilingRunEvidenceReference{
					Store: "evidence", Key: servingkube.DispatchOccupancyPrefix + "node-uid/request-uid.json",
				},
			},
		}
		record := servingkube.DispatchOccupancyRecord{
			SchemaVersion: servingkube.DispatchOccupancySchema,
			Node:          serving.NodeReference{Name: "node", UID: "node-uid"}, Batch: 1,
			PerRequestNanos: d, Throughput: 10, Conditions: cr.Spec, Verdict: measurementVerdictAccepted,
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		storage := &operationProjectionStore{payload: raw}
		service := &measurementService{
			client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(&cr).Build(),
			objects: storage, opts: options{servingNamespace: "allowed", maxEvidenceBytes: 1 << 20},
		}
		list, err := service.listDispatchOccupancies(context.Background(), "allowed")
		if err != nil {
			t.Fatal(err)
		}
		detail, err := service.getDispatchOccupancy(context.Background(), "allowed", "occupancy")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(list.Items[0], detail) {
			t.Fatal("occupancy list/detail diverged")
		}
		var assessment operatorconsole.MeasurementAssessment
		if err := json.Unmarshal(detail.Assessment, &assessment); err != nil {
			t.Fatal(err)
		}
		want := liveOperationComplete
		if d < 0 {
			want = operatorconsole.MeasurementWarning
		}
		if assessment.Status != want || !assessment.Evidence.Accepted {
			t.Fatalf("d=%d assessment=%+v", d, assessment)
		}
		if *cr.Status.PerRequestNanos != d || len(storage.reads) != 2 {
			t.Fatal("projection changed signed d or did not read exact evidence")
		}
		cr.Status.Result = nil
		without, err := service.projectDispatchOccupancy(context.Background(), cr)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(without.Assessment, &assessment); err != nil {
			t.Fatal(err)
		}
		if assessment.Evidence.Accepted {
			t.Fatal("Complete without retained record became accepted")
		}
	}
}

func TestProfilesDecorationPreservesWireFieldsWithoutInventingRows(t *testing.T) {
	view := httpapi.ProfilesView{ConfigVersion: "rv"}
	view.Profiles = []serving.Profile{{ID: "profile", Batches: []serving.BatchProfile{{Batch: 1}}}}
	service := &measurementService{}
	raw, err := service.decorateProfiles(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ConfigVersion string `json:"configVersion"`
		Profiles      []struct {
			ID         string                                `json:"id"`
			Assessment operatorconsole.MeasurementAssessment `json:"assessment"`
			ResultRows []operatorconsole.ResultRow           `json:"resultRows"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.ConfigVersion != "rv" || len(result.Profiles) != 1 || result.Profiles[0].ID != "profile" ||
		result.Profiles[0].ResultRows == nil || len(result.Profiles[0].ResultRows) != 0 ||
		result.Profiles[0].Assessment.Status != liveOperationComplete ||
		len(result.Profiles[0].Assessment.Checklist.Checks) != 1 ||
		result.Profiles[0].Assessment.Checklist.Checks[0].Result != operatorconsole.MeasurementUnknown {
		t.Fatalf("decorated profile: %s", raw)
	}
}

func TestProfilesReuseRowsKeepBothActualPopulations(t *testing.T) {
	targetJ, sourceJ := 3.0, 7.0
	reuse := &serving.ProfileReuseCheck{
		SourceProfileID: "allowed/source/uid", ConfirmationProfileID: "allowed/target/uid",
		Outcome: measurementVerdictAccepted,
	}
	view := httpapi.ProfilesView{}
	view.Profiles = []serving.Profile{{ID: "derived", Reuse: reuse}}
	view.MeasurementFacts = map[string]serving.MeasurementFacts{
		"derived":             {Batches: []profilederivation.MeasurementBatchFacts{{Batch: 1, TotalJoulesPerItem: &targetJ}}},
		reuse.SourceProfileID: {Batches: []profilederivation.MeasurementBatchFacts{{Batch: 1, TotalJoulesPerItem: &sourceJ}}},
	}
	raw, err := (&measurementService{}).decorateProfiles(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Profiles []struct {
			Assessment operatorconsole.MeasurementAssessment `json:"assessment"`
			ResultRows []operatorconsole.ResultRow           `json:"resultRows"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	rows := got.Profiles[0].ResultRows
	if len(rows) != 2 || rows[0].RowKind != operatorconsole.ResultRowConfirmation ||
		rows[1].RowKind != operatorconsole.ResultRowSourceReference ||
		*rows[0].TotalJoulesPerItem != targetJ || *rows[1].TotalJoulesPerItem != sourceJ ||
		rows[0].SourceProfileID == nil || *rows[0].SourceProfileID != reuse.SourceProfileID ||
		rows[0].Reuse == nil || got.Profiles[0].Assessment.Status == operatorconsole.MeasurementCriteriaMet {
		t.Fatalf("reuse populations mixed: %s", raw)
	}
}
