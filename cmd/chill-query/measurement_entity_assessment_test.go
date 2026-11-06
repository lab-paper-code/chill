package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestR18HistoricalProfilesUseOnlyCurrentMeasurement(t *testing.T) {
	view := httpapi.ProfilesView{}
	view.CurrentMeasurements = []profilederivation.MeasurementSelection{{ID: "ns/current/uid"}}
	view.MeasurementFacts = map[string]serving.MeasurementFacts{}
	for _, id := range []string{"ns/old/uid", "ns/current/uid", "ns/current/uid/reuse-target"} {
		profile := serving.Profile{ID: id}
		if id == "ns/current/uid/reuse-target" {
			profile.Reuse = &serving.ProfileReuseCheck{ConfirmationProfileID: "ns/current/uid"}
		}
		view.MeasurementFacts[id] = serving.MeasurementFacts{
			Batches: []profilederivation.MeasurementBatchFacts{{Batch: 1}},
		}
		assessment, rows := queryapi.AssessCatalogProfile(
			queryCoverageCatalog(view),
			queryCoverageProfile(profile),
		)
		want := id != "ns/old/uid"
		if assessment.Scope.SchedulerUsed == nil || *assessment.Scope.SchedulerUsed != want {
			t.Fatalf("%s current use=%+v want=%v", id, assessment.Scope, want)
		}
		for _, row := range rows {
			if row.Assessment.Scope.SchedulerUsed == nil || *row.Assessment.Scope.SchedulerUsed != want {
				t.Fatalf("%s batch use differs", id)
			}
		}
	}
	view.CurrentMeasurements = []profilederivation.MeasurementSelection{{ID: "ns/newest-failed/uid"}}
	got, _ := queryapi.AssessCatalogProfile(
		queryCoverageCatalog(view),
		queryCoverageProfile(serving.Profile{ID: "ns/current/uid"}),
	)
	if got.Scope.SchedulerUsed == nil || *got.Scope.SchedulerUsed {
		t.Fatal("latest failed measurement revived historical success")
	}
}

func TestR18SnapshotProfilesEndpointRetainsCurrentSelection(t *testing.T) {
	for _, current := range []string{"ns/current/uid", "ns/newest-failed/uid"} {
		t.Run(current, func(t *testing.T) {
			owner := servingkube.CatalogView{
				Profiles:            []serving.Profile{{ID: "ns/old/uid"}, {ID: "ns/current/uid"}},
				CurrentMeasurements: []profilederivation.MeasurementSelection{{ID: current}},
			}
			snapshot := servingkube.ProjectCatalogSnapshot(owner, "rv", nil)
			raw, err := json.Marshal(servingkube.CatalogRead{CatalogSnapshot: snapshot, Accepted: &snapshot})
			if err != nil {
				t.Fatal(err)
			}
			var read servingkube.CatalogRead
			if err := json.Unmarshal(raw, &read); err != nil {
				t.Fatal(err)
			}
			service := &measurementService{
				catalog: func(context.Context) (servingkube.CatalogRead, error) { return read, nil },
			}
			mux := http.NewServeMux()
			service.register(mux)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/profiles", nil))
			var got profileResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || len(got.Profiles) != 2 {
				t.Fatalf("profiles response: %d %s", response.Code, response.Body.String())
			}
			for _, profile := range got.Profiles {
				used := profile.Assessment.Scope.SchedulerUsed
				if used == nil || *used != (profile.ID == current) {
					t.Fatalf("snapshot current=%s profile=%s schedulerUsed=%v", current, profile.ID, used)
				}
			}
		})
	}
}

func TestRequestAssessmentUsesCatalogIdentityNotCompletePhase(t *testing.T) {
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "old", UID: "old-uid", Generation: 1},
		Status:     api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
	}
	view := httpapi.ProfilesView{}
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); got.Evidence.Accepted ||
		got.DisplayStatus == queryapi.MeasurementUsable {
		t.Fatalf("phase accepted without evidence: %+v", got)
	}
	view.Profiles = []serving.Profile{{
		ID: "allowed/old/old-uid", Reference: evidencestore.ObjectIdentity{Store: "evidence", Key: "result"},
		Batches: []serving.BatchProfile{{Batch: 1}},
	}}
	view.CurrentMeasurements = []profilederivation.MeasurementSelection{{ID: "allowed/old/old-uid"}}
	condition := profilederivation.CurvePlanSpec{PlannedBatchSizes: []int{1, 2}}
	view.Profiles[0].Condition = condition
	zero, passed := 0.0, true
	view.MeasurementFacts = map[string]serving.MeasurementFacts{"allowed/old/old-uid": {
		Request: profilederivation.MeasurementRequestReference{
			Namespace: task.Namespace, Name: task.Name, UID: string(task.UID),
		},
		ResultReference: profilederivation.RawEvidenceIdentity{Store: "evidence", Key: "result"},
		Condition:       condition, PointPolicy: profilederivation.PointPolicy{MinimumTrials: 3},
		Batches: []profilederivation.MeasurementBatchFacts{{
			Batch: 1, ActiveTrialCount: 3, IdleTrialCount: 3, P95Milliseconds: &zero, LatencySampleCount: 2,
			Confidence:  &profilederivation.PointConfidence{Level: .95, HalfWidth: 0},
			PowerSource: profilederivation.MeasurementPowerSource{Kind: "node-wall", NodeName: "node"},
			Sources:     []profilederivation.RawEvidenceIdentity{{Store: "evidence", Key: "raw"}},
			Quality:     []profilederivation.MeasurementTrialQuality{{PolicyPassed: &passed}},
		}},
	}}
	if got := queryapi.AssessMeasurementRequest(
		task, queryCoverageCatalog(view), queryCoverageCatalog(view),
	); got.Status != queryapi.MeasurementPartial || !got.Evidence.Accepted {
		t.Fatalf("retained fixed-sweep plan lost: %+v", got)
	}
	task.Status.Outcome = "BatchDiscoveryComplete"
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); (got.Status != queryapi.MeasurementComplete && got.Status != queryapi.MeasurementCriteriaMet) ||
		got.Scope.SchedulerUsed == nil || !*got.Scope.SchedulerUsed {
		t.Fatalf("published evidence: %+v", got)
	}
	newer := task
	newer.Name, newer.UID, newer.Status.Phase = "newer", "newer-uid", string(api.MeasurementPhaseFailed)
	if got := queryapi.AssessMeasurementRequest(
		newer,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); got.Status != queryapi.MeasurementFailed ||
		got.Evidence.Accepted {
		t.Fatalf("new request borrowed old evidence: %+v", got)
	}
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); !got.Evidence.Accepted {
		t.Fatal("new failure hid old accepted evidence")
	}
	view.Insufficiencies = []servingkube.CatalogIssue{{
		Source: types.NamespacedName{Namespace: task.Namespace, Name: task.Name},
		Code:   "ReplacementSelection", Reason: "replacement target is absent; annotation did not replace a measurement",
	}}
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); got.DisplayStatus != queryapi.MeasurementUsable || !got.Evidence.Accepted {
		t.Fatalf("operator replacement diagnostic became a rejection: %+v", got)
	}
	view.Insufficiencies = []servingkube.CatalogIssue{{
		Source: types.NamespacedName{Namespace: task.Namespace, Name: task.Name},
		Code:   "InvalidEvidence", Reason: "real validation failure",
	}}
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); got.DisplayStatus != queryapi.MeasurementProblem {
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
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); got.DisplayStatus != queryapi.MeasurementReview ||
		!got.Evidence.Accepted || got.Scope.SchedulerUsed == nil || *got.Scope.SchedulerUsed {
		t.Fatalf("superseded verified result lost review or became scheduler-used: %+v", got)
	}
}

func TestOperationAssessmentRequiresStoredAcceptedSteps(t *testing.T) {
	cr := api.OperationCostMeasurement{
		ObjectMeta: meta.ObjectMeta{Generation: 1},
		Spec:       api.OperationCostMeasurementSpec{Steps: []api.OperationCostStep{{Name: "idle"}}},
		Status: api.OperationCostMeasurementStatus{
			Phase: api.MeasurementPhaseComplete,
			Steps: []api.OperationCostStepStatus{
				{Name: "idle", Key: "stored", Verdict: queryapi.MeasurementVerdictAccepted},
			},
		},
	}
	if got := queryapi.AssessOperationCost(
		cr,
		queryOperationSteps([]servingkube.OperationStepProjection{{}}),
	); got.Evidence.Accepted {
		t.Fatalf("status-only step accepted: %+v", got)
	}
	duration, joules := int64(100), 4.0
	steps := []servingkube.OperationStepProjection{{
		OperationID: "uid.0.idle", Verdict: queryapi.MeasurementVerdictAccepted,
		DurationNanos: &duration, WallJoules: &joules,
	}}
	if got := queryapi.AssessOperationCost(
		cr,
		queryOperationSteps(steps),
	); got.Status != queryapi.MeasurementComplete ||
		!got.Evidence.Accepted || got.Execution.ObservedGeneration != 0 {
		t.Fatalf("verified operation: %+v", got)
	}
	cr.Status.Phase = api.MeasurementPhaseFailed
	cr.Status.Steps[0].Verdict = string(api.MeasurementPhaseFailed)
	steps[0].Verdict = string(api.MeasurementPhaseFailed)
	if got := queryapi.AssessOperationCost(
		cr,
		queryOperationSteps(steps),
	); got.Status != queryapi.MeasurementFailed || got.Evidence.Accepted {
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
			PerRequestNanos: d, Throughput: 10, Conditions: cr.Spec, Verdict: queryapi.MeasurementVerdictAccepted,
		}
		record.Window.Start, record.Window.End = time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC()
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
		if detail.D == nil || *detail.D.PerRequestNanos != d || detail.MeasurementWindow == nil ||
			!detail.MeasurementWindow.Start.Equal(record.Window.Start) ||
			!detail.MeasurementWindow.End.Equal(record.Window.End) ||
			detail.Name != cr.Name || detail.UID != string(cr.UID) || detail.Phase == nil || *detail.Phase != "Complete" {
			t.Fatalf("progress lost signed value, evidence window or identity: %+v", detail.DispatchOccupancyProgress)
		}
		assertOccupancyWireFields(t, detail)
		var assessment queryapi.MeasurementAssessment
		if err := json.Unmarshal(detail.Assessment, &assessment); err != nil {
			t.Fatal(err)
		}
		want := queryapi.MeasurementComplete
		if d < 0 {
			want = queryapi.MeasurementWarning
		}
		if assessment.Status != want || !assessment.Evidence.Accepted {
			t.Fatalf("d=%d assessment=%+v", d, assessment)
		}
		if *cr.Status.PerRequestNanos != d || len(storage.reads) != 2 {
			t.Fatal("projection changed signed d or did not read exact evidence")
		}
		assertOccupancyRetainedEvidence(t, service, storage, cr, record)
	}
}

func assertOccupancyWireFields(t *testing.T, detail dispatchOccupancyProjection) {
	t.Helper()
	wire, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"metadata", "conditions", "status", "references", "assessment",
		"node", "d", "measurementWindow", "profileID", "createdAt",
	} {
		if _, exists := fields[key]; !exists {
			t.Fatalf("wire field %s missing", key)
		}
	}
}

func assertOccupancyRetainedEvidence(
	t *testing.T, service *measurementService, storage *operationProjectionStore,
	cr api.DispatchOccupancyMeasurement, record servingkube.DispatchOccupancyRecord,
) {
	t.Helper()
	record.Node.UID = "historical-node"
	record.Conditions.Seed++
	record.Batch++
	record.PerRequestNanos++
	cr.Status.Result.Key = "archive/occupancy.json"
	cr.Status.PerRequestNanos = nil
	var err error
	storage.payload, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := querykube.ProjectDispatchOccupancy(
		context.Background(), service.objects, service.opts.maxEvidenceBytes, cr, decodeOccupancyEvidence,
	)
	if err != nil {
		t.Fatal(err)
	}
	retained := dispatchOccupancyHTTPProjection(cr, projection)
	if projection.Record == nil || retained.MeasurementWindow == nil || retained.Evidence == nil ||
		retained.D == nil || *retained.D.PerRequestNanos != record.PerRequestNanos {
		t.Fatal("retained measurement disappeared after metadata drift")
	}
	cr.Status.Result = nil
	without, err := service.projectDispatchOccupancy(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	var assessment queryapi.MeasurementAssessment
	if err := json.Unmarshal(without.Assessment, &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.Evidence.Accepted {
		t.Fatal("Complete without retained record became accepted")
	}
	if without.MeasurementWindow != nil || without.Evidence != nil {
		t.Fatal("missing evidence invented measurement timing or reference")
	}
}

func TestProfilesDecorationPreservesWireFieldsWithoutInventingRows(t *testing.T) {
	view := httpapi.ProfilesView{ConfigVersion: "rv"}
	view.Profiles = []serving.Profile{{ID: "profile", Batches: []serving.BatchProfile{{Batch: 1}}}}
	service := &measurementService{}
	raw, err := service.profilePayload(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ConfigVersion string `json:"configVersion"`
		Profiles      []struct {
			ID         string                         `json:"id"`
			Assessment queryapi.MeasurementAssessment `json:"assessment"`
			ResultRows []queryapi.ResultRow           `json:"resultRows"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.ConfigVersion != "rv" || len(result.Profiles) != 1 || result.Profiles[0].ID != "profile" ||
		result.Profiles[0].ResultRows == nil || len(result.Profiles[0].ResultRows) != 0 ||
		result.Profiles[0].Assessment.Status != queryapi.MeasurementComplete ||
		len(result.Profiles[0].Assessment.Checklist.Checks) != 1 ||
		result.Profiles[0].Assessment.Checklist.Checks[0].Result != queryapi.MeasurementUnknown {
		t.Fatalf("decorated profile: %s", raw)
	}
}

func TestProfilesReuseRowsKeepBothActualPopulations(t *testing.T) {
	targetJ, sourceJ := 3.0, 7.0
	reuse := &serving.ProfileReuseCheck{
		SourceProfileID: "allowed/source/uid", ConfirmationProfileID: "allowed/target/uid",
		Outcome: queryapi.MeasurementVerdictAccepted,
	}
	view := httpapi.ProfilesView{}
	view.Profiles = []serving.Profile{{ID: "derived", Reuse: reuse}}
	view.MeasurementFacts = map[string]serving.MeasurementFacts{
		"derived": {
			Batches: []profilederivation.MeasurementBatchFacts{{Batch: 1, TotalJoulesPerItem: &targetJ}},
		},
		reuse.SourceProfileID: {
			Batches: []profilederivation.MeasurementBatchFacts{{Batch: 1, TotalJoulesPerItem: &sourceJ}},
		},
	}
	raw, err := (&measurementService{}).profilePayload(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Profiles []struct {
			Assessment queryapi.MeasurementAssessment `json:"assessment"`
			ResultRows []queryapi.ResultRow           `json:"resultRows"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	rows := got.Profiles[0].ResultRows
	if len(rows) != 2 || rows[0].RowKind != queryapi.ResultRowConfirmation ||
		rows[1].RowKind != queryapi.ResultRowSourceReference ||
		*rows[0].TotalJoulesPerItem != targetJ || *rows[1].TotalJoulesPerItem != sourceJ ||
		rows[0].SourceProfileID == nil || *rows[0].SourceProfileID != reuse.SourceProfileID ||
		rows[0].Reuse == nil || got.Profiles[0].Assessment.Status == queryapi.MeasurementCriteriaMet {
		t.Fatalf("reuse populations mixed: %s", raw)
	}
}

func TestNativeRequestListSurvivesUnavailableCatalog(t *testing.T) {
	for _, source := range []string{"standalone", "backend-down", "configuration"} {
		for _, phase := range []string{string(api.MeasurementPhaseComplete), string(api.MeasurementPhaseFailed)} {
			t.Run(source+"/"+phase, func(t *testing.T) {
				task := &api.BatchEnergyOptimization{
					ObjectMeta: meta.ObjectMeta{
						Namespace: "native-list", Name: "request", UID: "native-uid",
						Generation: 2, ResourceVersion: "native-version",
					},
					Status: api.BatchEnergyOptimizationStatus{
						Phase: phase, ObservedGeneration: 2, CompletedTrials: 3,
						Conditions: []meta.Condition{
							{Type: "Progress", Status: meta.ConditionTrue, Reason: "NativeFact"},
						},
					},
				}
				opts := options{servingNamespace: task.Namespace, requestTimeout: time.Second, maxRequestBytes: 1 << 20}
				if source == "backend-down" {
					owner := httptest.NewTLSServer(http.NotFoundHandler())
					opts.servingURL = owner.URL
					owner.Close()
				}
				catalog, err := newServingCatalogReader(opts)
				if err != nil {
					t.Fatal(err)
				}
				if source == "configuration" {
					catalog = func(context.Context) (servingkube.CatalogRead, error) {
						return servingkube.CatalogRead{}, &serving.Error{
							Code: "InvalidInput", Message: "catalog configuration unavailable",
						}
					}
				}
				service := &measurementService{
					client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task).Build(),
					catalog: catalog, opts: opts,
				}
				mux := http.NewServeMux()
				service.register(mux)
				response := httptest.NewRecorder()
				path := "/api/v1/namespaces/" + task.Namespace + "/batch-energy-optimizations"
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusOK {
					t.Fatalf("catalog failure hid native facts: %d %s", response.Code, response.Body.String())
				}
				var got struct {
					Items []struct {
						api.BatchEnergyOptimization
						Assessment queryapi.MeasurementAssessment `json:"assessment"`
					} `json:"items"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if len(got.Items) != 1 || got.Items[0].UID != task.UID ||
					got.Items[0].ResourceVersion != task.ResourceVersion ||
					!reflect.DeepEqual(got.Items[0].Spec, task.Spec) ||
					!reflect.DeepEqual(got.Items[0].Status, task.Status) {
					t.Fatalf("native request metadata/spec/status changed: %s", response.Body.String())
				}
				assessment := got.Items[0].Assessment
				unavailable := false
				for _, reason := range assessment.Reasons {
					unavailable = unavailable || reason.Code == "CatalogUnavailable" && reason.Message != ""
				}
				if !unavailable || assessment.Evidence.Available || assessment.Evidence.Accepted ||
					assessment.Scope.SchedulerUsed != nil || assessment.Execution == nil ||
					assessment.Execution.Phase != phase {
					t.Fatalf("unavailable catalog became accepted evidence or erased execution: %+v", assessment)
				}
			})
		}
	}
}

func TestUnadoptedAcceptedFactsNeverRepairCurrentRequestAssessment(t *testing.T) {
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "old", UID: "old-uid", Generation: 1},
		Status:     api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
	}
	accepted := httpapi.ProfilesView{CatalogView: servingkube.CatalogView{
		MeasurementFacts: map[string]serving.MeasurementFacts{"old-unadopted": {
			Request: profilederivation.MeasurementRequestReference{
				Namespace: task.Namespace, Name: task.Name, UID: string(task.UID),
			},
			ResultReference: profilederivation.RawEvidenceIdentity{Store: "evidence", Key: "old-result"},
			Batches:         []profilederivation.MeasurementBatchFacts{{Batch: 1}},
		}},
	}}
	latest := httpapi.ProfilesView{CatalogView: servingkube.CatalogView{
		Insufficiencies: []servingkube.CatalogIssue{{
			Source: types.NamespacedName{Namespace: task.Namespace, Name: task.Name},
			Code:   "InvalidEvidence", Reason: "current evidence rejected",
		}},
	}}
	got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(accepted),
		queryCoverageCatalog(latest),
	)
	if got.Evidence.Accepted || got.Evidence.Available || got.Scope.SchedulerUsed != nil ||
		got.DisplayStatus != queryapi.MeasurementProblem {
		t.Fatalf("old unadopted facts replaced current rejection: %+v", got)
	}
}

func TestR19RequestAssessmentUsesCurrentMeasurementIdentity(t *testing.T) {
	view := httpapi.ProfilesView{}
	view.CurrentMeasurements = []profilederivation.MeasurementSelection{{ID: "ns/current/uid"}}
	for _, name := range []string{"old", "current", "other"} {
		view.Profiles = append(view.Profiles, serving.Profile{
			ID: "ns/" + name + "/uid", Batches: []serving.BatchProfile{{Batch: 1}},
		})
	}
	for _, name := range []string{"old", "current", "other"} {
		task := api.BatchEnergyOptimization{
			ObjectMeta: meta.ObjectMeta{Namespace: "ns", Name: name, UID: "uid"},
		}
		got := queryapi.AssessMeasurementRequest(
			task,
			queryCoverageCatalog(view),
			queryCoverageCatalog(view),
		)
		if got.Scope.SchedulerUsed == nil || *got.Scope.SchedulerUsed != (name == "current") {
			t.Fatalf("%s scheduler scope=%+v", name, got.Scope)
		}
	}
	view.CurrentMeasurements = []profilederivation.MeasurementSelection{{ID: "ns/latest-failed/uid"}}
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "ns", Name: "current", UID: "uid"},
	}
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); got.Scope.SchedulerUsed == nil || *got.Scope.SchedulerUsed {
		t.Fatalf("latest failure revived old evidence: %+v", got.Scope)
	}
	view.Profiles = []serving.Profile{{
		ID: "ns/source/source-uid/reuse-target", Batches: []serving.BatchProfile{{Batch: 1}},
		Reuse: &serving.ProfileReuseCheck{
			ConfirmationProfileID: "ns/confirmation/confirmation-uid", Outcome: queryapi.MeasurementVerdictAccepted,
		},
	}}
	view.CurrentMeasurements = []profilederivation.MeasurementSelection{{ID: "ns/confirmation/confirmation-uid"}}
	task.Name, task.UID = "confirmation", "confirmation-uid"
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); !got.Scope.Reused ||
		got.Scope.SchedulerUsed == nil || !*got.Scope.SchedulerUsed {
		t.Fatalf("confirmation identity lost: %+v", got.Scope)
	}
	task.Name, task.UID = "source", "source-uid"
	if got := queryapi.AssessMeasurementRequest(
		task,
		queryCoverageCatalog(view),
		queryCoverageCatalog(view),
	); got.Scope.SchedulerUsed != nil && *got.Scope.SchedulerUsed {
		t.Fatalf("source reference became confirmation: %+v", got.Scope)
	}
}

func TestOperationAssessmentInputConversionPreservesConsultedFacts(t *testing.T) {
	if queryOperationSteps(nil) != nil {
		t.Fatal("nil operation evidence became an empty slice")
	}
	if got := queryOperationSteps([]servingkube.OperationStepProjection{}); got == nil || len(got) != 0 {
		t.Fatal("an empty operation evidence slice changed shape")
	}
	duration, joules := int64(-9223372036854775807), 0.0
	source := []servingkube.OperationStepProjection{
		{OperationID: "operation/uid", DurationNanos: &duration, WallJoules: &joules, Reason: "retained reason"},
		{},
	}
	got := queryOperationSteps(source)
	if len(got) != 2 || got[0].OperationID != source[0].OperationID ||
		got[0].DurationNanos != &duration || got[0].WallJoules != &joules || got[0].Reason != source[0].Reason ||
		got[1].DurationNanos != nil || got[1].WallJoules != nil || got[1].OperationID != "" || got[1].Reason != "" {
		t.Fatalf("operation evidence input changed: %+v", got)
	}
}
