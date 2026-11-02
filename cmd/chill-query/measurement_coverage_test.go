package main

import (
	"context"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCoverageProblemWithoutFailedRequestHasNextAction(t *testing.T) {
	out := queryapi.MeasurementCoverageResponse{}
	cell := queryapi.MeasurementCoverageCell{
		ID: "conflict",
		Assessment: queryapi.MeasurementAssessment{
			DisplayStatus: queryapi.MeasurementProblem,
			Reasons: []queryapi.MeasurementReason{{
				Code: "CurrentDeclarationConflict", Message: "현재 선언과 측정이 다릅니다",
			}},
		},
	}
	addCoverageActions(&out, cell, map[string]int{})
	if len(out.NextActions) != 1 || out.NextActions[0].ReasonCode != "CurrentDeclarationConflict" ||
		out.NextActions[0].CellID != cell.ID {
		t.Fatalf("problem must remain actionable without a failed request: %#v", out.NextActions)
	}
}

func TestCoverageLatestCellNeverUnionsHistoricalBatches(t *testing.T) {
	now := time.Now().UTC()
	base := &measurementCoverageBase{
		checked: map[string]time.Time{}, failures: map[string]error{}, catalog: httpapi.ProfilesView{},
	}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.checked[kind] = now
	}
	base.inventory = queryapi.InventoryView{
		Models: []queryapi.InventoryModelView{{
			Name: "model", UID: "model-uid",
			Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{Name: "cpu"}}},
		}},
		Classes: []queryapi.InventoryClassView{{
			Name: "class", UID: "class-uid", PowerModes: []queryapi.InventoryPowerModeView{{Name: "fixed"}},
			Nodes: []queryapi.InventoryNodeView{{Name: "node", UID: "node-uid"}},
		}},
	}
	base.compatibility.Rows = []queryapi.ModelCompatibilityView{{
		Selection: queryapi.ModelCompatibilitySelection{
			ModelSpec: "model", DeviceClass: "class", ExecutionPath: "cpu", Node: "node",
		},
		Verdict: "Compatible",
		Inputs: queryapi.ModelCompatibilityInputs{
			Node: queryapi.ModelCompatibilityObjectInput{Name: "node", UID: "node-uid"},
		},
	}}
	base.compatibility.NodeRows = []queryapi.ModelCompatibilityView{{
		Inputs: queryapi.ModelCompatibilityInputs{
			RuntimeDeclaration: &queryapi.ModelCompatibilityRuntimeInput{Image: "current"},
		},
	}}
	base.catalog.MeasurementFacts = map[string]serving.MeasurementFacts{}
	zero := 0.0
	for index := range 2 {
		name := []string{"first", "second"}[index]
		uid := types.UID(name + "-uid")
		task := api.BatchEnergyOptimization{
			ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: name, UID: uid, Generation: 1},
			Spec: api.BatchEnergyOptimizationSpec{
				State: api.MeasurementStateSpec{
					Candidate: api.ProfilingRunCandidateSpec{
						ModelSpec: "model", DeviceClass: "class", ExecutionPath: "cpu", PowerMode: "fixed",
					}, TargetNode: "node",
				}, BatchSizes: []int32{1, 2},
			},
			Status: api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
		}
		id := coverageRequestID(task)
		batch := index + 1
		condition := profilederivation.CurvePlanSpec{
			LogicalModel: "model", DeviceClass: "class", NodeUID: "node-uid", PowerMode: "fixed",
			RuntimeImage:      "current",
			ActiveProtocol:    profilederivation.CurveProtocolContract{WarmupIterations: index + 1},
			PlannedBatchSizes: []int{1, 2},
		}
		facts := serving.MeasurementFacts{
			Request: profilederivation.MeasurementRequestReference{
				Namespace: "allowed",
				Name:      name,
				UID:       string(uid),
			},
			Condition: condition, Batches: []profilederivation.MeasurementBatchFacts{{
				Batch: batch, IncrementalJoulesPerItem: &zero, P95Milliseconds: &zero,
			}},
		}
		base.requests.Items = append(base.requests.Items, task)
		base.catalog.Profiles = append(base.catalog.Profiles, serving.Profile{
			ID: id, Condition: condition, Batches: []serving.BatchProfile{{Batch: batch}},
			MeasuredAt: time.Unix(int64(index+1), 0),
		})
		base.catalog.MeasurementFacts[id] = facts
	}
	got := buildMeasurementCoverage(base, "allowed", httpapi.MeasurementCoverageQuery{Batches: []int{1, 2}})
	if len(got.Cells) != 1 {
		t.Fatalf("cells %d", len(got.Cells))
	}
	cell := got.Cells[0]
	if cell.VariantCount != 1 || cell.BatchProgress == nil || cell.Representative == nil ||
		cell.CurrentMeasurement == nil || cell.CurrentMeasurement.ID != coverageRequestID(base.requests.Items[1]) ||
		len(cell.History) != 2 || len(got.Entities.Profiles) != 2 {
		t.Fatalf("latest selection lost its history or current batch facts: %+v", cell)
	}
	if cell.Assessment.Status != "partial" || *got.Summary.CompleteCellCount != 0 {
		t.Fatalf("fake complete across protocol conditions: %+v", cell.Assessment)
	}
	for _, variant := range cell.Variants {
		if len(variant.BatchProgress.Measured) != 1 || len(variant.BatchProgress.Missing) != 1 {
			t.Fatalf("variant batch union %+v", variant.BatchProgress)
		}
	}
	// A cost-source failure is intentionally absent from the source set and cannot
	// affect coverage completeness; a real Run source failure does.
	base.failures["Run"] = assertCoverageError{}
	partial := buildMeasurementCoverage(base, "allowed", httpapi.MeasurementCoverageQuery{})
	if partial.Completeness != "partial" || partial.Summary.CountsByDisplayStatus["unmeasured"] != nil {
		t.Fatal("incomplete run source invented unmeasured count")
	}
}

type assertCoverageError struct{}

func (assertCoverageError) Error() string { return "source unavailable" }

func TestCoverageFreshNamespaceSharedByFiveCallers(t *testing.T) {
	base := &measurementCoverageBase{checked: map[string]time.Time{}, failures: map[string]error{}}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.checked[kind] = time.Now().UTC()
	}
	service := &measurementService{
		opts: options{servingNamespace: "allowed", maxRequestBytes: 8 << 20},
		coverageCache: measurementCoverageCache{
			namespaces: map[string]*measurementCoverageEntry{"allowed": {base: base}},
		},
	}
	// Nil Kubernetes/Query API dependencies make any redundant source I/O fail.
	results := make(chan error, 5)
	for range 5 {
		go func() {
			_, err := service.coverage(
				context.Background(),
				"allowed",
				httpapi.MeasurementCoverageQuery{Batches: []int{1, 2}},
			)
			results <- err
		}()
	}
	for range 5 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(base.checked) != 5 || len(base.requests.Items) != 0 {
		t.Fatal("query projection mutated base snapshot")
	}
}

func TestCoverageKeepsReviewChecksAcrossVariantAndCell(t *testing.T) {
	used := true
	row := queryapi.ResultRow{
		Batch: 1,
		Assessment: queryapi.AssessMeasurement(queryapi.MeasurementAssessmentInput{
			Accepted: true, Available: true, SchedulerUsed: &used,
			Checks: []queryapi.MeasurementCheck{{
				Code: "DatasetInputContract", Result: "fail", Message: "입력 계약 불일치",
			}},
		}),
	}
	cell := queryapi.MeasurementCoverageCell{Variants: []queryapi.MeasurementCoverageVariant{{
		ProfileIDs: []string{"profile"}, RequestIDs: []string{"request"}, ResultRows: []queryapi.ResultRow{row},
		BatchProgress: queryapi.MeasurementBatchProgress{Target: []int{1}, Measured: []int{1}},
	}}}
	p := coverageProjection{base: &measurementCoverageBase{}, adopted: map[string]bool{"profile": true}}
	p.assessCell(&cell)
	if cell.Assessment.DisplayStatus != "review" || len(cell.Assessment.Checklist.Checks) != 1 ||
		cell.Variants[0].Assessment.DisplayStatus != "review" {
		t.Fatalf("accepted measurement lost review across aggregation: %+v", cell)
	}
}

func TestCoverageCountsMeasurementsRegardlessOfRuntimeImage(t *testing.T) {
	for _, test := range []struct {
		name     string
		images   []string
		missing  []bool
		expected string
	}{
		{"different image insufficient", []string{"old"}, []bool{true}, "insufficient"},
		{"different image usable", []string{"old"}, []bool{false}, "usable"},
		{"same condition latest usable", []string{"old", "current"}, []bool{true, false}, "usable"},
		{"same condition latest insufficient", []string{"old", "current"}, []bool{false, true}, "insufficient"},
		{"current usable", []string{"current"}, []bool{false}, "usable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := &measurementCoverageBase{}
			base.compatibility.NodeRows = []queryapi.ModelCompatibilityView{{
				Inputs: queryapi.ModelCompatibilityInputs{
					RuntimeDeclaration: &queryapi.ModelCompatibilityRuntimeInput{Image: "current"},
				},
			}}
			cell := queryapi.MeasurementCoverageCell{
				Support: queryapi.MeasurementSupport{Verdict: "Compatible"},
				History: []queryapi.MeasurementHistory{{RequestID: "request"}},
			}
			for i, image := range test.images {
				progress := queryapi.MeasurementBatchProgress{Target: []int{1, 2}, Measured: []int{1, 2}}
				if test.missing[i] {
					progress.Measured = []int{1}
				}
				cell.Variants = append(cell.Variants, queryapi.MeasurementCoverageVariant{
					ID: image, Condition: profilederivation.CurvePlanSpec{RuntimeImage: image},
					MeasuredAt: time.Unix(int64(i+1), 0),
					ProfileIDs: []string{"profile"}, RequestIDs: []string{image}, BatchProgress: progress,
				})
			}
			p := coverageProjection{base: base, adopted: map[string]bool{"profile": true}}
			p.assessCell(&cell)
			if cell.Assessment.DisplayStatus != test.expected {
				t.Fatalf("display status = %q, want %q", cell.Assessment.DisplayStatus, test.expected)
			}
			if len(cell.Variants) != 1 ||
				cell.Variants[0].Condition.(profilederivation.CurvePlanSpec).RuntimeImage != test.images[len(test.images)-1] {
				t.Fatal("latest measurement must retain its recorded runtime image")
			}
		})
	}
}

func TestCoverageUnreadDeclarationsPreserveMeasurements(t *testing.T) {
	for _, failure := range []string{"CompatibilityReadFailed", "source unavailable"} {
		t.Run(failure, func(t *testing.T) {
			base := &measurementCoverageBase{
				failures: map[string]error{},
				compatibility: queryapi.ModelCompatibilityMatrixView{
					NodeRows: []queryapi.ModelCompatibilityView{{
						Reasons: []queryapi.ModelCompatibilityReason{{Code: "CompatibilityReadFailed"}},
					}},
				},
			}
			if failure == "source unavailable" {
				base.failures["compatibility"] = assertCoverageError{}
				base.compatibility.NodeRows = nil
			}
			cell := queryapi.MeasurementCoverageCell{
				Support: queryapi.MeasurementSupport{Verdict: "Unknown"},
				Variants: []queryapi.MeasurementCoverageVariant{{
					Condition:  profilederivation.CurvePlanSpec{RuntimeImage: "retained"},
					ProfileIDs: []string{"profile"}, RequestIDs: []string{"request"},
					BatchProgress: queryapi.MeasurementBatchProgress{
						Target: []int{1, 2}, Measured: []int{1},
					},
				}},
			}
			p := coverageProjection{base: base}
			p.assessCell(&cell)
			if cell.Assessment.DisplayStatus != "insufficient" || cell.Support.Verdict != "Unknown" {
				t.Fatalf("unread declarations replaced retained measurement: %+v", cell)
			}
		})
	}
}

func TestCoverageDatasetContractOnlyFailsProvenMismatch(t *testing.T) {
	base := &measurementCoverageBase{}
	base.datasets.Items = []api.Dataset{{
		ObjectMeta: meta.ObjectMeta{Name: "records", UID: "data-uid"},
		Spec:       api.DatasetSpec{Fields: []api.RawField{{Name: "image", Format: "JPEG", MaxBytes: 1024}}},
	}}
	base.preprocessors.Items = []api.Preprocessor{{
		ObjectMeta: meta.ObjectMeta{Name: "pre"},
		Spec: api.PreprocessorSpec{
			Input: api.RawInputContract{Fields: []api.RawField{{Name: "image", Format: "JPEG", MaxBytes: 2048}}},
			Artifacts: []api.PreprocessorArtifact{{
				Target: api.PreprocessorTarget{Model: "model", Artifact: "onnx"},
				Source: api.PreprocessorArtifactSource{Image: "pre-image", Path: "/pre"},
			}},
		},
	}}
	condition := profilederivation.CurvePlanSpec{
		LogicalModel: "model", Artifact: profilederivation.CurveArtifactContract{Name: "onnx"},
		Preprocessor: profilederivation.CurvePreprocessorContract{Name: "pre", Image: "pre-image", Path: "/pre"},
		ActiveProtocol: profilederivation.CurveProtocolContract{
			Dataset: &profilederivation.CurveDatasetContract{Name: "records", UID: "data-uid"},
		},
	}
	if check := coverageDatasetCheck(base, condition); check.Result != "pass" {
		t.Fatalf("compatible declared fields must pass: %+v", check)
	}
	base.datasets.Items[0].Spec.Fields[0].Format = "UTF8"
	if check := coverageDatasetCheck(base, condition); check.Result != "fail" {
		t.Fatalf("known field mismatch must fail: %+v", check)
	}
	condition.ActiveProtocol.Dataset.UID = "replaced-data"
	if check := coverageDatasetCheck(base, condition); check.Result != "unknown" {
		t.Fatalf("changed identity must not invent mismatch: %+v", check)
	}
}

func TestCoverageCurrentFailedMeasurementIsNotHiddenByOldProfiles(t *testing.T) {
	base := coverageSizeBase()
	failed := *base.requests.Items[0].DeepCopy()
	failed.Name, failed.UID = "latest-failed", "latest-failed-uid"
	failed.Status.Phase = string(api.MeasurementPhaseFailed)
	failed.Status.Conditions = []meta.Condition{{
		Type: "MeasurementComplete", Status: meta.ConditionFalse, ObservedGeneration: failed.Generation,
		LastTransitionTime: meta.NewTime(time.Unix(1000, 0)), Reason: "RetryLimitExhausted",
	}}
	base.requests.Items = append(base.requests.Items, failed)
	response := buildMeasurementCoverage(base, coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
	candidate := failed.Spec.State.Candidate
	id := coverageCellID(candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode)
	for _, cell := range response.Cells {
		if cell.ID != id {
			continue
		}
		if cell.CurrentMeasurement == nil || cell.CurrentMeasurement.ID != coverageRequestID(failed) ||
			cell.Assessment.Status != queryapi.MeasurementFailed || cell.Assessment.Evidence.Accepted ||
			cell.Assessment.DisplayStatus != queryapi.MeasurementProblem || cell.VariantCount != 1 {
			t.Fatalf("latest failure was hidden by retained successful profiles: %+v", cell)
		}
		if len(response.Entities.Profiles) != 80 || len(base.catalog.MeasurementFacts) != 80 {
			t.Fatal("current failure selection deleted historical profile evidence")
		}
		// Existing operator replacement annotations precede recency, without a
		// second override mechanism or a preference for successful evidence.
		base.requests.Items[0].Annotations = map[string]string{
			profilederivation.ReplacesMeasurementAnnotation: failed.Name,
		}
		replaced := buildMeasurementCoverage(base, coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
		for _, selected := range replaced.Cells {
			if selected.ID == id && (selected.CurrentMeasurement == nil ||
				selected.CurrentMeasurement.ID == coverageRequestID(failed)) {
				t.Fatal("explicit replacement did not precede recency")
			}
		}
		return
	}
	t.Fatal("latest failed measurement cell not found")
}

func TestCoverageMeasurementTimeUsesOnlyCurrentTerminalEvidence(t *testing.T) {
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{
			Generation: 2, CreationTimestamp: meta.NewTime(time.Unix(9000, 0)),
		},
		Status: api.BatchEnergyOptimizationStatus{
			Phase: string(api.MeasurementPhaseFailed), ObservedGeneration: 2,
			Conditions: []meta.Condition{{
				Type: "MeasurementComplete", ObservedGeneration: 2,
				LastTransitionTime: meta.NewTime(time.Unix(2, 0)),
			}},
		},
	}
	if got := coverageMeasurementTime(task, time.Unix(1, 0)); !got.Equal(time.Unix(2, 0)) {
		t.Fatal("failed measurement used an old success instead of terminal time")
	}
	task.Status.Phase = string(api.MeasurementPhaseComplete)
	if got := coverageMeasurementTime(task, time.Unix(1, 0)); !got.Equal(time.Unix(1, 0)) {
		t.Fatal("verified raw profile completion time lost precedence")
	}
	task.Status.Phase = "Measuring"
	if !coverageMeasurementTime(task, time.Time{}).IsZero() {
		t.Fatal("nonterminal measurement invented a completion time")
	}
	task.Status.Phase, task.Status.ObservedGeneration = string(api.MeasurementPhaseFailed), 1
	if !coverageMeasurementTime(task, time.Time{}).IsZero() {
		t.Fatal("stale generation invented a current completion time")
	}
	task.Status.ObservedGeneration = 2
	task.Status.Conditions[0].ObservedGeneration = 1
	if !coverageMeasurementTime(task, time.Time{}).IsZero() {
		t.Fatal("stale condition invented a current completion time")
	}
}
func TestCoverageConditionIdentitySurvivesSameConditionPreflight(t *testing.T) {
	plan := measurementPreflightTestPlan(t)
	cell := queryapi.MeasurementCoverageCell{}
	measurementID := "ns/measurement/uid"
	variant := coverageFindVariant(&cell, measurementID, plan.Shared(), "requested")
	variant.RequestIDs = append(variant.RequestIDs, measurementID)
	if variant.ID == measurementID || variant.ID != queryapi.MeasurementVariantID(plan.Shared()) {
		t.Fatal("condition identity was replaced by measurement identity")
	}
	view, err := queryapi.BuildBatchEnergyOptimizationPreflight(queryapi.BatchEnergyOptimizationPreflightSnapshot{
		Plan: plan, VariantID: variant.ID, RequestsComplete: true,
		Campaign:      api.BatchEnergyOptimization{Spec: api.BatchEnergyOptimizationSpec{BatchSizes: []int32{1, 2}}},
		ExactEvidence: []queryapi.MeasurementPreflightEvidence{{Condition: plan.Shared(), Batches: []int{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range view.BatchPlan.Reasons {
		if reason.Code == "VariantConditionChanged" {
			t.Fatal("unchanged frozen condition reported a changed variant")
		}
	}
	if view.BatchPlan.VariantID == nil || *view.BatchPlan.VariantID != variant.ID {
		t.Fatal("preflight did not retain the condition identity from coverage")
	}
}

func TestCoverageFailedRequestHistoryUsesCurrentIdentityNotCellStatus(t *testing.T) {
	current, historical := 0, 0
	summary := queryapi.MeasurementCoverageSummary{
		CurrentFailedRequestCount: &current, HistoricalFailedRequestCount: &historical,
	}
	cells := map[string]queryapi.MeasurementCoverageCell{
		"nano-int8": {
			CurrentMeasurement: &profilederivation.MeasurementSelection{ID: "latest-partial"},
			Assessment:         queryapi.MeasurementAssessment{DisplayStatus: queryapi.MeasurementInsufficient},
		},
	}
	requests := []queryapi.MeasurementEntity{}
	for _, id := range []string{"old-failure-1", "old-failure-2", "old-failure-3"} {
		requests = append(requests, queryapi.MeasurementEntity{ID: id, Failed: true, CellIDs: []string{"nano-int8"}})
	}
	for _, id := range []string{"current-failure-1", "current-failure-2", "current-failure-3", "current-failure-4"} {
		cells[id] = queryapi.MeasurementCoverageCell{
			CurrentMeasurement: &profilederivation.MeasurementSelection{ID: id},
			Assessment:         queryapi.MeasurementAssessment{DisplayStatus: queryapi.MeasurementProblem},
		}
		requests = append(requests, queryapi.MeasurementEntity{ID: id, Failed: true, CellIDs: []string{id}})
	}
	countCoverageFailedRequests(&summary, requests, cells)
	if current != 4 || historical != 3 {
		t.Fatalf("latest partial changed older failures into current failures: current=%d historical=%d", current, historical)
	}
}
