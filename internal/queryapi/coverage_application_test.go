package queryapi

import (
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCoverageLatestCellNeverUnionsHistoricalBatches(t *testing.T) {
	now := time.Now().UTC()
	base := &CoverageBase{
		Checked: map[string]time.Time{}, Failures: map[string]error{}, Catalog: CoverageCatalog{},
	}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.Checked[kind] = now
	}
	base.Inventory = InventoryView{
		Models: []InventoryModelView{{
			Name: "model", UID: "model-uid",
			Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{Name: "cpu"}}},
		}},
		Classes: []InventoryClassView{{
			Name: "class", UID: "class-uid", PowerModes: []InventoryPowerModeView{{Name: "fixed"}},
			Nodes: []InventoryNodeView{{Name: "node", UID: "node-uid"}},
		}},
	}
	base.Compatibility.Rows = []ModelCompatibilityView{{
		Selection: ModelCompatibilitySelection{
			ModelSpec: "model", DeviceClass: "class", ExecutionPath: "cpu", Node: "node",
		},
		Verdict: "Compatible",
		Inputs: ModelCompatibilityInputs{
			Node: ModelCompatibilityObjectInput{Name: "node", UID: "node-uid"},
		},
	}}
	base.Compatibility.NodeRows = []ModelCompatibilityView{{
		Inputs: ModelCompatibilityInputs{
			RuntimeDeclaration: &ModelCompatibilityRuntimeInput{Image: "current"},
		},
	}}
	base.Catalog.MeasurementFacts = map[string]profilederivation.MeasurementFacts{}
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
				},
			},
			Status: api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
		}
		id := CoverageRequestID(task)
		batch := index + 1
		condition := profilederivation.CurvePlanSpec{
			LogicalModel: "model", DeviceClass: "class", NodeUID: "node-uid", PowerMode: "fixed",
			RuntimeImage:      "current",
			ActiveProtocol:    profilederivation.CurveProtocolContract{WarmupIterations: index + 1},
			PlannedBatchSizes: []int{1, 2},
		}
		facts := profilederivation.MeasurementFacts{
			Request: profilederivation.MeasurementRequestReference{
				Namespace: "allowed",
				Name:      name,
				UID:       string(uid),
			},
			Condition: condition, Batches: []profilederivation.MeasurementBatchFacts{{
				Batch: batch, IncrementalJoulesPerItem: &zero, P95Milliseconds: &zero,
			}},
		}
		base.Requests.Items = append(base.Requests.Items, task)
		base.Catalog.Profiles = append(base.Catalog.Profiles, CoverageProfile{
			ID: id, Condition: condition, Batches: []int{batch},
			MeasuredAt: time.Unix(int64(index+1), 0),
		})
		base.Catalog.MeasurementFacts[id] = facts
	}
	got := BuildMeasurementCoverage(base, "allowed", CoverageQuery{Batches: []int{1, 2}})
	if len(got.Cells) != 1 {
		t.Fatalf("cells %d", len(got.Cells))
	}
	cell := got.Cells[0]
	if cell.VariantCount != 1 || cell.BatchProgress == nil || cell.Representative == nil ||
		cell.CurrentMeasurement == nil || cell.CurrentMeasurement.ID != CoverageRequestID(base.Requests.Items[1]) ||
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
	base.Failures["Run"] = assertCoverageError{}
	partial := BuildMeasurementCoverage(base, "allowed", CoverageQuery{})
	if partial.Completeness != "partial" || partial.Summary.CountsByDisplayStatus["unmeasured"] != nil {
		t.Fatal("incomplete run source invented unmeasured count")
	}
}

func TestCoverageUsesCompletedDiscoveryPrefixWithoutHidingQualityOrExplicitGoals(t *testing.T) {
	base := coverageSizeBase()
	task := coverageSizeRequest(0)
	task.Status.Outcome = "BatchDiscoveryComplete"
	base.Requests.Items = []api.BatchEnergyOptimization{task}
	base.Runs.Items = nil
	base.Catalog.Profiles = nil
	base.Catalog.MeasurementFacts = map[string]profilederivation.MeasurementFacts{}
	coverageSizeAddProfile(base, task, 0)
	id := CoverageRequestID(task)
	facts := base.Catalog.MeasurementFacts[id]
	facts.Condition.PlannedBatchSizes = []int{1, 2, 4}
	facts.Batches = []profilederivation.MeasurementBatchFacts{
		coverageSizeBatch(task, facts.Condition, 1), coverageSizeBatch(task, facts.Condition, 2),
	}
	base.Catalog.MeasurementFacts[id] = facts
	base.Catalog.Profiles[0].Condition = facts.Condition
	base.Catalog.Profiles[0].Batches = []int{1, 2}
	cellID := CoverageCellID(task.Spec.State.Candidate.ModelSpec, task.Spec.State.Candidate.DeviceClass,
		task.Spec.State.Candidate.ExecutionPath, task.Spec.State.Candidate.PowerMode)
	read := func(query CoverageQuery) MeasurementCoverageCell {
		response := BuildMeasurementCoverage(base, task.Namespace, query)
		for _, cell := range response.Cells {
			if cell.ID == cellID {
				return cell
			}
		}
		t.Fatal("measured cell is absent")
		return MeasurementCoverageCell{}
	}
	cell := read(CoverageQuery{})
	if cell.Assessment.DisplayStatus != MeasurementUsable || cell.BatchProgress == nil ||
		len(cell.BatchProgress.Target) != 2 || cell.BatchProgress.Target[0] != 1 || cell.BatchProgress.Target[1] != 2 ||
		len(cell.BatchProgress.Missing) != 0 || len(cell.Variants) != 1 || len(cell.Variants[0].ResultRows) != 2 {
		t.Fatalf("completed discovery prefix became an incomplete full-domain sweep: %+v", cell)
	}
	assertFullGoalMissing := func(query CoverageQuery) {
		t.Helper()
		cell := read(query)
		if cell.Assessment.DisplayStatus != MeasurementInsufficient || cell.BatchProgress == nil ||
			len(cell.BatchProgress.Target) != 3 || len(cell.BatchProgress.Missing) != 1 {
			t.Fatalf("unconfirmed prefix or explicit goal bypassed missing-batch checks: %+v", cell)
		}
	}
	assertFullGoalMissing(CoverageQuery{Batches: []int{1, 2, 4}})
	facts.Batches[0].ActiveTrialCount = 2
	base.Catalog.MeasurementFacts[id] = facts
	if cell := read(CoverageQuery{}); cell.Assessment.DisplayStatus == MeasurementUsable {
		t.Fatalf("prefix completion hid incomplete trial quality: %+v", cell)
	}
	facts.Batches[0].ActiveTrialCount = 3
	base.Catalog.MeasurementFacts[id] = facts
	base.Catalog.Profiles[0].Condition.RuntimeImage = "different-runtime"
	assertFullGoalMissing(CoverageQuery{})
	base.Catalog.Profiles[0].Condition = facts.Condition
	base.Requests.Items[0].Status.Outcome = "MinimumFound/LocalMinimum"
	assertFullGoalMissing(CoverageQuery{})
	base.Requests.Items[0].Status.Outcome = "BatchDiscoveryComplete"
	facts.ResultReference.Key = ""
	base.Catalog.MeasurementFacts[id] = facts
	assertFullGoalMissing(CoverageQuery{})
}

type assertCoverageError struct{}

func (assertCoverageError) Error() string { return "source unavailable" }

func TestCoverageKeepsReviewChecksAcrossVariantAndCell(t *testing.T) {
	used := true
	row := ResultRow{
		Batch: 1,
		Assessment: AssessMeasurement(MeasurementAssessmentInput{
			Accepted: true, Available: true, SchedulerUsed: &used,
			Checks: []MeasurementCheck{{
				Code: "DatasetInputContract", Result: "fail", Message: "입력 계약 불일치",
			}},
		}),
	}
	cell := MeasurementCoverageCell{Variants: []MeasurementCoverageVariant{{
		ProfileIDs: []string{"profile"}, RequestIDs: []string{"request"}, ResultRows: []ResultRow{row},
		BatchProgress: MeasurementBatchProgress{Target: []int{1}, Measured: []int{1}},
	}}}
	p := coverageProjection{base: &CoverageBase{}, adopted: map[string]bool{"profile": true}}
	p.assessCell(&cell)
	if cell.Assessment.DisplayStatus != MeasurementReview || len(cell.Assessment.Checklist.Checks) != 1 ||
		cell.Variants[0].Assessment.DisplayStatus != MeasurementReview {
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
			base := &CoverageBase{}
			base.Compatibility.NodeRows = []ModelCompatibilityView{{
				Inputs: ModelCompatibilityInputs{
					RuntimeDeclaration: &ModelCompatibilityRuntimeInput{Image: "current"},
				},
			}}
			cell := MeasurementCoverageCell{
				Support: MeasurementSupport{Verdict: "Compatible"},
				History: []MeasurementHistory{{RequestID: "request"}},
			}
			for i, image := range test.images {
				progress := MeasurementBatchProgress{Target: []int{1, 2}, Measured: []int{1, 2}}
				if test.missing[i] {
					progress.Measured = []int{1}
				}
				cell.Variants = append(cell.Variants, MeasurementCoverageVariant{
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
			base := &CoverageBase{
				Failures: map[string]error{},
				Compatibility: ModelCompatibilityMatrixView{
					NodeRows: []ModelCompatibilityView{{
						Reasons: []ModelCompatibilityReason{{Code: "CompatibilityReadFailed"}},
					}},
				},
			}
			if failure == "source unavailable" {
				base.Failures["compatibility"] = assertCoverageError{}
				base.Compatibility.NodeRows = nil
			}
			cell := MeasurementCoverageCell{
				Support: MeasurementSupport{Verdict: "Unknown"},
				Variants: []MeasurementCoverageVariant{{
					Condition:  profilederivation.CurvePlanSpec{RuntimeImage: "retained"},
					ProfileIDs: []string{"profile"}, RequestIDs: []string{"request"},
					BatchProgress: MeasurementBatchProgress{
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
	base := &CoverageBase{}
	base.Datasets = []CoverageDataset{{
		Name: "records", UID: "data-uid",
		Fields: []api.RawField{{Name: "image", Format: "JPEG", MaxBytes: 1024}},
	}}
	base.Preprocessors = []CoveragePreprocessor{{
		Name: "pre", Fields: []api.RawField{{Name: "image", Format: "JPEG", MaxBytes: 2048}},
		Artifacts: []CoveragePreprocessorArtifact{{Model: "model", Artifact: "onnx", Image: "pre-image", Path: "/pre"}},
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
	base.Datasets[0].Fields[0].Format = "UTF8"
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
	failed := *base.Requests.Items[0].DeepCopy()
	failed.Name, failed.UID = "latest-failed", "latest-failed-uid"
	failed.Status.Phase = string(api.MeasurementPhaseFailed)
	failed.Status.Conditions = []meta.Condition{{
		Type: "MeasurementComplete", Status: meta.ConditionFalse, ObservedGeneration: failed.Generation,
		LastTransitionTime: meta.NewTime(time.Unix(1000, 0)), Reason: "RetryLimitExhausted",
	}}
	base.Requests.Items = append(base.Requests.Items, failed)
	response := BuildMeasurementCoverage(base, coverageSizeNamespace, CoverageQuery{})
	candidate := failed.Spec.State.Candidate
	id := CoverageCellID(candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode)
	for _, cell := range response.Cells {
		if cell.ID != id {
			continue
		}
		if cell.CurrentMeasurement == nil || cell.CurrentMeasurement.ID != CoverageRequestID(failed) ||
			cell.Assessment.Status != MeasurementFailed || cell.Assessment.Evidence.Accepted ||
			cell.Assessment.DisplayStatus != MeasurementProblem || cell.VariantCount != 1 {
			t.Fatalf("latest failure was hidden by retained successful profiles: %+v", cell)
		}
		if len(response.Entities.Profiles) != 80 || len(base.Catalog.MeasurementFacts) != 80 {
			t.Fatal("current failure selection deleted historical profile evidence")
		}
		// Existing operator replacement annotations precede recency, without a
		// second override mechanism or a preference for successful evidence.
		base.Requests.Items[0].Annotations = map[string]string{
			profilederivation.ReplacesMeasurementAnnotation: failed.Name,
		}
		replaced := BuildMeasurementCoverage(base, coverageSizeNamespace, CoverageQuery{})
		for _, selected := range replaced.Cells {
			if selected.ID == id && (selected.CurrentMeasurement == nil ||
				selected.CurrentMeasurement.ID == CoverageRequestID(failed)) {
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
	if got := CoverageMeasurementTime(task, time.Unix(1, 0)); !got.Equal(time.Unix(2, 0)) {
		t.Fatal("failed measurement used an old success instead of terminal time")
	}
	task.Status.Phase = string(api.MeasurementPhaseComplete)
	if got := CoverageMeasurementTime(task, time.Unix(1, 0)); !got.Equal(time.Unix(1, 0)) {
		t.Fatal("verified raw profile completion time lost precedence")
	}
	task.Status.Phase = "Measuring"
	if !CoverageMeasurementTime(task, time.Time{}).IsZero() {
		t.Fatal("nonterminal measurement invented a completion time")
	}
	task.Status.Phase, task.Status.ObservedGeneration = string(api.MeasurementPhaseFailed), 1
	if !CoverageMeasurementTime(task, time.Time{}).IsZero() {
		t.Fatal("stale generation invented a current completion time")
	}
	task.Status.ObservedGeneration = 2
	task.Status.Conditions[0].ObservedGeneration = 1
	if !CoverageMeasurementTime(task, time.Time{}).IsZero() {
		t.Fatal("stale condition invented a current completion time")
	}
}

func TestCoveragePreservesNonBlockingCompatibilityDiagnostics(t *testing.T) {
	cell := CoverageNewCell(MeasurementCoverageGroup{
		Model: MeasurementIdentity{Name: "model"}, DeviceClass: MeasurementIdentity{Name: "class"},
	}, "cpu", "fixed", map[string]ModelCompatibilityView{
		"model/class/cpu": {
			Verdict: MeasurementSupportCompatible, Diagnostics: []string{coverageDuplicateDiagnostic},
		},
	})
	cell.Variants = []MeasurementCoverageVariant{{
		ID: "condition", ProfileIDs: []string{"profile"}, RequestIDs: []string{"request"},
	}}
	projection := coverageProjection{base: &CoverageBase{}, adopted: map[string]bool{"profile": true}}
	projection.assessCell(&cell)
	if cell.Support.Verdict != MeasurementSupportCompatible || len(cell.Support.Reasons) != 0 ||
		len(cell.Support.Diagnostics) != 1 || cell.Support.Diagnostics[0] != coverageDuplicateDiagnostic ||
		cell.Assessment.DisplayStatus != MeasurementUsable {
		t.Fatalf("diagnostic was lost or blocked current evidence: %+v", cell)
	}
}

const coverageDuplicateDiagnostic = "Last entry selected."
