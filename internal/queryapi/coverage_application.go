package queryapi

import (
	"fmt"
	"slices"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"k8s.io/utils/ptr"
)

type coverageProjection struct {
	base      *CoverageBase
	namespace string
	query     CoverageQuery
	out       MeasurementCoverageResponse
	indices   map[string]int
	owners    map[string][]api.ProfilingRun
	profiles  map[string][]CoverageProfile
	adopted   map[string]bool
	replaced  map[string]bool
	occupancy []api.DispatchOccupancyMeasurement
}

func BuildMeasurementCoverage(
	base *CoverageBase, namespace string, query CoverageQuery,
) MeasurementCoverageResponse {
	p := coverageProjection{base: base, namespace: namespace, query: query}
	p.initialize()
	p.addSources()
	p.addCells()
	p.indexEvidence()
	for _, task := range base.Requests.Items {
		p.addRequest(task)
	}
	p.addRuns()
	for i := range p.out.Cells {
		p.assessCell(&p.out.Cells[i])
		p.addOccupancy(&p.out.Cells[i])
	}
	SummarizeMeasurementCoverage(&p.out, base.Failures)
	CompactMeasurementCoverage(&p.out)
	return p.out
}

func (p *coverageProjection) initialize() {
	p.occupancy = append([]api.DispatchOccupancyMeasurement{}, p.base.Occupancies.Items...)
	slices.SortFunc(p.occupancy, func(a, b api.DispatchOccupancyMeasurement) int {
		if compared := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); compared != 0 {
			return compared
		}
		return strings.Compare(a.Name, b.Name)
	})
	basis, supportBasis := "requested", "representative-node-static"
	if len(p.query.Batches) > 0 {
		basis = "explicit"
	}
	if p.query.Node != "" {
		supportBasis = "selected-node-static"
	}
	p.out = MeasurementCoverageResponse{
		SchemaVersion: "measurement-coverage.v1", AssessmentVersion: "result-checklist.v1",
		SnapshotID: p.namespace + "/" + p.base.Catalog.Revision + "/" +
			p.base.Requests.ResourceVersion + "/" + p.base.Runs.ResourceVersion + "/" + p.base.Occupancies.ResourceVersion,
		DataAsOf:  p.base.Oldest().UTC().Format(time.RFC3339Nano),
		Freshness: "fresh", Completeness: MeasurementComplete,
		Scope: MeasurementCoverageScope{
			Namespace: p.namespace, SupportBasis: supportBasis, GoalBasis: basis,
			TargetBatches: append([]int{}, p.query.Batches...),
		},
		Sources: []MeasurementCoverageSource{}, Groups: []MeasurementCoverageGroup{},
		Cells: []MeasurementCoverageCell{}, NextActions: []MeasurementNextAction{},
		Entities: MeasurementCoverageEntities{
			Requests: []MeasurementEntity{}, Profiles: []MeasurementEntity{},
			Runs: []MeasurementRunEntity{},
		},
	}
	if time.Since(p.base.Oldest()) > 30*time.Second || len(p.base.Failures) > 0 {
		p.out.Freshness = CoverageFreshnessStale
	}
}

func (p *coverageProjection) addSources() {
	for _, kind := range CoverageSourceKinds {
		source := MeasurementCoverageSource{
			Kind: kind, ResourceVersions: []string{}, Complete: true,
		}
		switch kind {
		case "inventory":
			source.ResourceVersions = []string{
				p.base.Inventory.ModelSpecsResourceVersion, p.base.Inventory.DeviceClassesResourceVersion,
				p.base.Inventory.NodesResourceVersion,
			}
		case CoverageCatalogSource:
			source.ResourceVersions = []string{p.base.Catalog.Revision}
		case "input-contract":
			source.ResourceVersions = []string{p.base.DatasetsResourceVersion, p.base.PreprocessorsResourceVersion}
		case "BEO":
			source.ResourceVersions = []string{p.base.Requests.ResourceVersion}
		case "Run":
			source.ResourceVersions = []string{p.base.Runs.ResourceVersion}
		case "DOM":
			source.ResourceVersions = []string{p.base.Occupancies.ResourceVersion}
		case "compatibility":
			for _, row := range p.base.Compatibility.NodeRows {
				if row.Inputs.RuntimeDeclaration != nil {
					source.ResourceVersions = append(
						source.ResourceVersions,
						row.Inputs.RuntimeDeclaration.ResourceVersion,
					)
				}
			}
			slices.Sort(source.ResourceVersions)
			source.ResourceVersions = slices.Compact(source.ResourceVersions)
		}
		if p.base.Failures[kind] != nil {
			source.Complete = false
			source.Error = &MeasurementCoverageSourceError{
				Code: "SourceUnavailable", Message: "입력 갱신 실패: " + kind, Retryable: true,
			}
			p.out.Completeness = MeasurementPartial
		}
		p.out.Sources = append(p.out.Sources, source)
	}
}

func (p *coverageProjection) supportIndex() map[string]ModelCompatibilityView {
	support := map[string]ModelCompatibilityView{}
	rows := p.base.Compatibility.Rows
	if p.query.Node != "" {
		rows = p.base.Compatibility.NodeRows
	}
	for _, row := range rows {
		if p.query.Node == "" || row.Selection.Node == p.query.Node {
			key := strings.Join([]string{
				row.Selection.ModelSpec, row.Selection.DeviceClass, row.Selection.ExecutionPath,
			}, "/")
			support[key] = row
		}
	}
	return support
}

func (p *coverageProjection) addCells() {
	support := p.supportIndex()
	classes := append([]InventoryClassView{}, p.base.Inventory.Classes...)
	slices.SortFunc(classes, func(a, b InventoryClassView) int { return strings.Compare(a.Name, b.Name) })
	models := append([]InventoryModelView{}, p.base.Inventory.Models...)
	slices.SortFunc(models, func(a, b InventoryModelView) int { return strings.Compare(a.Name, b.Name) })
	p.indices = map[string]int{}
	for _, model := range models {
		for _, class := range classes {
			matches := slices.ContainsFunc(class.Nodes, func(node InventoryNodeView) bool {
				return node.Name == p.query.Node
			})
			if p.query.Node != "" && !matches {
				continue
			}
			group := MeasurementCoverageGroup{
				Model:       MeasurementIdentity{Name: model.Name, UID: model.UID},
				DeviceClass: MeasurementIdentity{Name: class.Name, UID: class.UID},
				CellIDs:     []string{}, Supported: ptr.To(0),
			}
			for _, path := range model.Spec.ExecutionPaths {
				for _, mode := range class.PowerModes {
					cell := CoverageNewCell(group, path.Name, mode.Name, support)
					p.indices[cell.ID] = len(p.out.Cells)
					p.out.Cells = append(p.out.Cells, cell)
					group.CellIDs = append(group.CellIDs, cell.ID)
				}
			}
			p.out.Groups = append(p.out.Groups, group)
		}
	}
}

func (p *coverageProjection) indexEvidence() {
	p.owners = CoverageOwnedRuns(p.base.Requests.Items, p.base.Runs.Items)
	p.profiles = map[string][]CoverageProfile{}
	p.adopted = map[string]bool{}
	byName := make(map[string]string, len(p.base.Requests.Items))
	replacements := map[string]string{}
	for _, task := range p.base.Requests.Items {
		id := CoverageRequestID(task)
		byName[task.Namespace+"/"+task.Name] = id
		if name := task.Annotations[profilederivation.ReplacesMeasurementAnnotation]; name != "" &&
			task.DeletionTimestamp == nil {
			replacements[id] = task.Namespace + "/" + name
		}
	}
	p.replaced = profilederivation.ReplacedMeasurements(replacements, byName)
	for _, profile := range p.base.Catalog.Profiles {
		if facts, ok := p.base.Catalog.MeasurementFacts[profile.ID]; ok {
			id := facts.Request.Namespace + "/" + facts.Request.Name + "/" + facts.Request.UID
			p.profiles[id] = append(p.profiles[id], profile)
		}
		p.adopted[profile.ID] = p.base.Adopted == nil || p.base.Adopted[profile.ID]
	}
	// Verified superseded facts remain measurable, not currently adopted inputs.
	ids := make([]string, 0, len(p.base.Catalog.MeasurementFacts))
	for id := range p.base.Catalog.MeasurementFacts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		facts := p.base.Catalog.MeasurementFacts[id]
		requestID := facts.Request.Namespace + "/" + facts.Request.Name + "/" + facts.Request.UID
		if facts.Request.Namespace != p.namespace || len(p.profiles[requestID]) > 0 {
			continue
		}
		p.profiles[requestID] = []CoverageProfile{{
			ID: id, Condition: facts.Condition, NodeName: facts.Condition.MetricSource.NodeName,
		}}
	}
}

func (p *coverageProjection) addRequest(task api.BatchEnergyOptimization) {
	if p.query.Node != "" && task.Spec.State.TargetNode != p.query.Node {
		return
	}
	candidate := task.Spec.State.Candidate
	id := CoverageCellID(candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode)
	index, exists := p.indices[id]
	requestID := CoverageRequestID(task)
	assessment := AssessMeasurementRequest(task, p.base.Catalog, p.base.Catalog)
	failures := MeasurementRequestFailures(task, p.owners[string(task.UID)], assessment)
	if assessment.Evidence.Accepted {
		used := false
		for _, profile := range p.profiles[requestID] {
			used = used || p.adopted[profile.ID]
		}
		assessment.Scope.SchedulerUsed = &used
		if !used {
			assessment.Scope.Facts = append(assessment.Scope.Facts, "현재 스케줄러 미사용")
		}
	}
	cellIDs := []string{}
	if exists {
		cellIDs = append(cellIDs, id)
	}
	p.out.Entities.Requests = append(p.out.Entities.Requests, MeasurementEntity{
		UID: string(task.UID), ID: requestID, Status: assessment.DisplayStatus, Label: assessment.Label,
		CellIDs: cellIDs, Failed: assessment.Status == MeasurementFailed,
		FailureSummary: failures,
	})
	if !exists {
		return
	}
	cell := &p.out.Cells[index]
	cell.History = append(cell.History, MeasurementHistory{
		RequestID: requestID, Status: assessment.Status,
	})
	if len(p.profiles[requestID]) == 0 {
		p.addPlannedVariant(cell, task, assessment)
	}
	for _, profile := range p.profiles[requestID] {
		p.addProfile(cell, task, profile)
	}
	cell.FailureSummary = append(cell.FailureSummary, failures...)
}

func (p *coverageProjection) addPlannedVariant(
	cell *MeasurementCoverageCell, task api.BatchEnergyOptimization,
	assessment MeasurementAssessment,
) {
	variant := CoverageFindVariant(cell, CoverageRequestID(task), task.Spec, p.out.Scope.GoalBasis)
	variant.MeasuredAt = CoverageMeasurementTime(task, time.Time{})
	variant.RequestIDs = append(variant.RequestIDs, CoverageRequestID(task))
	variant.Assessment = assessment
	variant.Assessment.Reasons = append(variant.Assessment.Reasons, CoverageReason(
		"PlannedConditionUnconfirmed", "요청 spec만 확인했으며 동결 실행 조건의 중복 여부는 검사에서 확인해야 합니다",
	))
	targets := make([]int, 0, len(task.Spec.BatchSizes))
	for _, batch := range task.Spec.BatchSizes {
		targets = append(targets, int(batch))
	}
	variant.BatchProgress.Target = CoverageBatches(append(variant.BatchProgress.Target, targets...))
	for _, run := range p.owners[string(task.UID)] {
		variant.RunIDs = append(variant.RunIDs, string(run.UID))
	}
}

func (p *coverageProjection) addProfile(
	cell *MeasurementCoverageCell, task api.BatchEnergyOptimization, profile CoverageProfile,
) {
	facts := p.base.Catalog.MeasurementFacts[profile.ID]
	variant := CoverageFindVariant(cell, CoverageRequestID(task), facts.Condition, p.out.Scope.GoalBasis)
	variant.MeasuredAt = CoverageMeasurementTime(task, profile.MeasuredAt)
	variant.RequestIDs = append(variant.RequestIDs, CoverageRequestID(task))
	variant.ProfileIDs = append(variant.ProfileIDs, profile.ID)
	variant.BatchProgress.Target = CoverageBatches(append(
		variant.BatchProgress.Target, facts.Condition.PlannedBatchSizes...,
	))
	for _, run := range p.owners[string(task.UID)] {
		variant.RunIDs = append(variant.RunIDs, string(run.UID))
	}
	for _, batch := range facts.Batches {
		row := CoverageResultRow(profile, facts, batch, "direct")
		row.Assessment.Scope.SchedulerUsed = ptr.To(p.adopted[profile.ID])
		if !p.adopted[profile.ID] {
			row.Assessment.Scope.Facts = append(row.Assessment.Scope.Facts, "현재 스케줄러 미사용")
		}
		if profile.Reuse != nil {
			row.RowKind = ResultRowConfirmation
			row.SourceProfileID = ptr.To(profile.Reuse.SourceProfileID)
			row.Reuse = profile.ReusePayload
		}
		variant.ResultRows = append(variant.ResultRows, row)
		variant.BatchProgress.Measured = append(variant.BatchProgress.Measured, batch.Batch)
		if row.Assessment.Status == MeasurementCriteriaMet {
			variant.BatchProgress.CriteriaMet = append(variant.BatchProgress.CriteriaMet, batch.Batch)
		}
	}
	p.addReuseSource(variant, profile)
	used := p.adopted[profile.ID]
	assessment := AssessMeasurement(MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: profile.Reuse != nil, SchedulerUsed: &used,
	})
	p.out.Entities.Profiles = append(p.out.Entities.Profiles, MeasurementEntity{
		UID: facts.Request.UID, ID: profile.ID, Status: assessment.DisplayStatus, Label: assessment.Label,
		CellIDs: []string{cell.ID},
	})
}

func (p *coverageProjection) addReuseSource(
	variant *MeasurementCoverageVariant, profile CoverageProfile,
) {
	if profile.Reuse == nil {
		return
	}
	source, ok := p.base.Catalog.MeasurementFacts[profile.Reuse.SourceProfileID]
	if !ok || source.Request.Namespace != p.namespace {
		return
	}
	for _, batch := range source.Batches {
		row := CoverageResultRow(profile, source, batch, ResultRowSourceReference)
		row.ProfileID = profile.Reuse.SourceProfileID
		row.SourceProfileID = ptr.To(profile.Reuse.SourceProfileID)
		row.TotalJoulesPerItem = nil
		row.Reuse = profile.ReusePayload
		variant.ResultRows = append(variant.ResultRows, row)
	}
}

func (p *coverageProjection) addRuns() {
	for _, run := range p.base.Runs.Items {
		if p.query.Node != "" && run.Spec.TargetNode != p.query.Node {
			continue
		}
		candidate := run.Spec.Candidate
		id := CoverageCellID(
			candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode,
		)
		cellIDs := []string{}
		if _, exists := p.indices[id]; exists {
			cellIDs = append(cellIDs, id)
		}
		assessment := AssessProfilingRun(run)
		p.out.Entities.Runs = append(p.out.Entities.Runs, MeasurementRunEntity{
			UID: string(run.UID), Status: assessment.DisplayStatus, Label: assessment.Label, CellIDs: cellIDs,
		})
	}
}

func (p *coverageProjection) assessCell(cell *MeasurementCoverageCell) {
	candidates := make([]profilederivation.MeasurementSelection, 0, len(cell.Variants))
	for i := range cell.Variants {
		variant := &cell.Variants[i]
		p.assessVariant(variant)
		if len(variant.RequestIDs) == 0 {
			continue
		}
		candidates = append(candidates, profilederivation.MeasurementSelection{
			ID: variant.RequestIDs[0], Model: cell.Model.Name, DeviceClass: cell.DeviceClass.Name,
			ExecutionPath: cell.ExecutionPath, PowerMode: cell.PowerMode, MeasuredAt: variant.MeasuredAt,
		})
	}
	var current *profilederivation.MeasurementSelection
	if selected := profilederivation.CurrentMeasurements(candidates, p.replaced); len(selected) > 0 {
		current = &selected[0]
	}
	*cell = AssessMeasurementCoverageCell(*cell, current,
		p.base.Failures["BEO"] == nil && p.base.Failures["Run"] == nil)
}

func (p *coverageProjection) assessVariant(variant *MeasurementCoverageVariant) {
	progress := &variant.BatchProgress
	if len(p.query.Batches) > 0 {
		progress.Target = append([]int{}, p.query.Batches...)
	}
	progress.Target = CoverageBatches(progress.Target)
	progress.Measured = CoverageBatches(progress.Measured)
	progress.CriteriaMet = CoverageBatches(progress.CriteriaMet)
	progress.InProgress = CoverageBatches(progress.InProgress)
	for _, batch := range progress.Target {
		if CoverageContains(progress.Measured, batch) {
			continue
		}
		if len(variant.ProfileIDs) == 0 {
			progress.Unknown = append(progress.Unknown, batch)
		} else {
			progress.Missing = append(progress.Missing, batch)
		}
	}
	if len(variant.ProfileIDs) == 0 {
		return
	}
	checks := []MeasurementAssessment{}
	used, reused := false, false
	for _, id := range variant.ProfileIDs {
		used = used || p.adopted[id]
	}
	for _, row := range variant.ResultRows {
		if row.RowKind != ResultRowSourceReference && CoverageContains(progress.Target, row.Batch) {
			assessment := row.Assessment
			checks = append(checks, assessment)
		}
		reused = reused || row.RowKind == ResultRowConfirmation
	}
	input := MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: reused, SchedulerUsed: &used, MissingBatches: progress.Missing,
	}
	if len(progress.Missing) == 0 && len(checks) > 0 {
		input.Children = checks
		if condition, ok := variant.Condition.(profilederivation.CurvePlanSpec); ok {
			input.Children = append(input.Children, AssessMeasurement(MeasurementAssessmentInput{
				Accepted: true, Available: true,
				Checks: []MeasurementCheck{coverageDatasetCheck(p.base, condition)},
			}))
		}
	}
	variant.Assessment = AssessMeasurement(input)
}

func CoverageResultRow(
	profile CoverageProfile, facts profilederivation.MeasurementFacts,
	batch profilederivation.MeasurementBatchFacts, kind string,
) ResultRow {
	row := MeasurementResultRow(facts, batch, profile.ID, profile.Reuse != nil, true)
	row.RowKind = kind
	row.ID = fmt.Sprintf("%s/%s/%d", profile.ID, kind, batch.Batch)
	return row
}

func coverageDatasetCheck(
	base *CoverageBase, condition profilederivation.CurvePlanSpec,
) MeasurementCheck {
	check := MeasurementCheck{
		Code: "DatasetInputContract", Result: "unknown",
		Message:   "입력 계약 검증 기록 없음: 현재 카탈로그와 동결 조건의 연결을 확인하지 못했습니다",
		SourceIDs: []string{},
	}
	if base.Failures["input-contract"] != nil || condition.ActiveProtocol.Dataset == nil {
		return check
	}
	for _, dataset := range base.Datasets {
		if dataset.Name != condition.ActiveProtocol.Dataset.Name ||
			dataset.UID != condition.ActiveProtocol.Dataset.UID {
			continue
		}
		for _, pre := range base.Preprocessors {
			if pre.Name != condition.Preprocessor.Name || len(pre.Fields) == 0 {
				continue
			}
			var artifact CoveragePreprocessorArtifact
			found := false
			for _, candidate := range pre.Artifacts {
				if candidate.Model == condition.LogicalModel && candidate.Artifact == condition.Artifact.Name {
					artifact, found = candidate, true
					break
				}
			}
			if !found || artifact.Image != condition.Preprocessor.Image ||
				artifact.Path != condition.Preprocessor.Path {
				continue
			}
			records := make(map[string]api.RawField, len(dataset.Fields))
			for _, field := range dataset.Fields {
				records[field.Name] = field
			}
			var mismatched []string
			for _, field := range pre.Fields {
				record, ok := records[field.Name]
				if !ok || record.Format != field.Format || record.MaxBytes > field.MaxBytes {
					mismatched = append(mismatched, field.Name)
				}
			}
			if len(mismatched) > 0 {
				check.Result = "fail"
				check.Message = "현재 카탈로그의 모델·데이터셋 입력 필드 불일치: " + strings.Join(mismatched, ", ")
			} else {
				check.Result = measurementCheckPassed
				check.Message = "현재 카탈로그의 모델·데이터셋 입력 필드 일치 · 데이터 용도 승인은 별도"
			}
			return check
		}
	}
	return check
}
