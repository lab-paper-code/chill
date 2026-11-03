package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"k8s.io/utils/ptr"
)

type coverageProjection struct {
	base      *measurementCoverageBase
	namespace string
	query     httpapi.MeasurementCoverageQuery
	out       queryapi.MeasurementCoverageResponse
	indices   map[string]int
	owners    map[string][]api.ProfilingRun
	profiles  map[string][]serving.Profile
	adopted   map[string]bool
	replaced  map[string]bool
	occupancy []api.DispatchOccupancyMeasurement
}

func buildMeasurementCoverage(
	base *measurementCoverageBase, namespace string, query httpapi.MeasurementCoverageQuery,
) queryapi.MeasurementCoverageResponse {
	p := coverageProjection{base: base, namespace: namespace, query: query}
	p.initialize()
	p.addSources()
	p.addCells()
	p.indexEvidence()
	for _, task := range base.requests.Items {
		p.addRequest(task)
	}
	p.addRuns()
	for i := range p.out.Cells {
		p.assessCell(&p.out.Cells[i])
		p.addOccupancy(&p.out.Cells[i])
	}
	queryapi.SummarizeMeasurementCoverage(&p.out, base.failures)
	queryapi.CompactMeasurementCoverage(&p.out)
	return p.out
}

func (p *coverageProjection) initialize() {
	p.occupancy = append([]api.DispatchOccupancyMeasurement{}, p.base.occupancies.Items...)
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
	p.out = queryapi.MeasurementCoverageResponse{
		SchemaVersion: "measurement-coverage.v1", AssessmentVersion: "result-checklist.v1",
		SnapshotID: p.namespace + "/" + p.base.catalog.Revision + "/" +
			p.base.requests.ResourceVersion + "/" + p.base.runs.ResourceVersion + "/" + p.base.occupancies.ResourceVersion,
		DataAsOf:  p.base.oldest().UTC().Format(time.RFC3339Nano),
		Freshness: "fresh", Completeness: queryapi.MeasurementComplete,
		Scope: queryapi.MeasurementCoverageScope{
			Namespace: p.namespace, SupportBasis: supportBasis, GoalBasis: basis,
			TargetBatches: append([]int{}, p.query.Batches...),
		},
		Sources: []queryapi.MeasurementCoverageSource{}, Groups: []queryapi.MeasurementCoverageGroup{},
		Cells: []queryapi.MeasurementCoverageCell{}, NextActions: []queryapi.MeasurementNextAction{},
		Entities: queryapi.MeasurementCoverageEntities{
			Requests: []queryapi.MeasurementEntity{}, Profiles: []queryapi.MeasurementEntity{},
			Runs: []queryapi.MeasurementRunEntity{},
		},
	}
	if time.Since(p.base.oldest()) > 30*time.Second || len(p.base.failures) > 0 {
		p.out.Freshness = coverageFreshnessStale
	}
}

func (p *coverageProjection) addSources() {
	for _, kind := range measurementCoverageSourceKinds {
		source := queryapi.MeasurementCoverageSource{
			Kind: kind, ResourceVersions: []string{}, Complete: true,
		}
		switch kind {
		case "inventory":
			source.ResourceVersions = []string{
				p.base.inventory.ModelSpecsResourceVersion, p.base.inventory.DeviceClassesResourceVersion,
				p.base.inventory.NodesResourceVersion,
			}
		case coverageCatalogSource:
			source.ResourceVersions = []string{p.base.catalog.Revision}
		case "input-contract":
			source.ResourceVersions = []string{p.base.datasets.ResourceVersion, p.base.preprocessors.ResourceVersion}
		case "BEO":
			source.ResourceVersions = []string{p.base.requests.ResourceVersion}
		case "Run":
			source.ResourceVersions = []string{p.base.runs.ResourceVersion}
		case "DOM":
			source.ResourceVersions = []string{p.base.occupancies.ResourceVersion}
		case "compatibility":
			for _, row := range p.base.compatibility.NodeRows {
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
		if p.base.failures[kind] != nil {
			source.Complete = false
			source.Error = &queryapi.MeasurementCoverageSourceError{
				Code: "SourceUnavailable", Message: "입력 갱신 실패: " + kind, Retryable: true,
			}
			p.out.Completeness = queryapi.MeasurementPartial
		}
		p.out.Sources = append(p.out.Sources, source)
	}
}

func (p *coverageProjection) supportIndex() map[string]queryapi.ModelCompatibilityView {
	support := map[string]queryapi.ModelCompatibilityView{}
	rows := p.base.compatibility.Rows
	if p.query.Node != "" {
		rows = p.base.compatibility.NodeRows
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
	classes := append([]queryapi.InventoryClassView{}, p.base.inventory.Classes...)
	slices.SortFunc(classes, func(a, b queryapi.InventoryClassView) int { return strings.Compare(a.Name, b.Name) })
	models := append([]queryapi.InventoryModelView{}, p.base.inventory.Models...)
	slices.SortFunc(models, func(a, b queryapi.InventoryModelView) int { return strings.Compare(a.Name, b.Name) })
	p.indices = map[string]int{}
	for _, model := range models {
		for _, class := range classes {
			matches := slices.ContainsFunc(class.Nodes, func(node queryapi.InventoryNodeView) bool {
				return node.Name == p.query.Node
			})
			if p.query.Node != "" && !matches {
				continue
			}
			group := queryapi.MeasurementCoverageGroup{
				Model:       queryapi.MeasurementIdentity{Name: model.Name, UID: model.UID},
				DeviceClass: queryapi.MeasurementIdentity{Name: class.Name, UID: class.UID},
				CellIDs:     []string{}, Supported: ptr.To(0),
			}
			for _, path := range model.Spec.ExecutionPaths {
				for _, mode := range class.PowerModes {
					cell := queryapi.CoverageNewCell(group, path.Name, mode.Name, support)
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
	p.owners = queryapi.CoverageOwnedRuns(p.base.requests.Items, p.base.runs.Items)
	p.profiles = map[string][]serving.Profile{}
	p.adopted = map[string]bool{}
	byName := make(map[string]string, len(p.base.requests.Items))
	replacements := map[string]string{}
	for _, task := range p.base.requests.Items {
		id := queryapi.CoverageRequestID(task)
		byName[task.Namespace+"/"+task.Name] = id
		if name := task.Annotations[profilederivation.ReplacesMeasurementAnnotation]; name != "" &&
			task.DeletionTimestamp == nil {
			replacements[id] = task.Namespace + "/" + name
		}
	}
	p.replaced = profilederivation.ReplacedMeasurements(replacements, byName)
	for _, profile := range p.base.catalog.Profiles {
		if facts, ok := p.base.catalog.MeasurementFacts[profile.ID]; ok {
			id := facts.Request.Namespace + "/" + facts.Request.Name + "/" + facts.Request.UID
			p.profiles[id] = append(p.profiles[id], profile)
		}
		p.adopted[profile.ID] = p.base.adopted == nil || p.base.adopted[profile.ID]
	}
	// Verified superseded facts remain measurable, not currently adopted inputs.
	ids := make([]string, 0, len(p.base.catalog.MeasurementFacts))
	for id := range p.base.catalog.MeasurementFacts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		facts := p.base.catalog.MeasurementFacts[id]
		requestID := facts.Request.Namespace + "/" + facts.Request.Name + "/" + facts.Request.UID
		if facts.Request.Namespace != p.namespace || len(p.profiles[requestID]) > 0 {
			continue
		}
		p.profiles[requestID] = []serving.Profile{{
			ID: id, Condition: facts.Condition, NodeName: facts.Condition.MetricSource.NodeName,
		}}
	}
}

func (p *coverageProjection) addRequest(task api.BatchEnergyOptimization) {
	if p.query.Node != "" && task.Spec.State.TargetNode != p.query.Node {
		return
	}
	candidate := task.Spec.State.Candidate
	id := queryapi.CoverageCellID(candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode)
	index, exists := p.indices[id]
	requestID := queryapi.CoverageRequestID(task)
	assessment := assessMeasurementRequest(task, p.base.catalog, p.base.catalog)
	failures := queryapi.MeasurementRequestFailures(task, p.owners[string(task.UID)], assessment)
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
	p.out.Entities.Requests = append(p.out.Entities.Requests, queryapi.MeasurementEntity{
		UID: string(task.UID), ID: requestID, Status: assessment.DisplayStatus, Label: assessment.Label,
		CellIDs: cellIDs, Failed: assessment.Status == queryapi.MeasurementFailed,
		FailureSummary: failures,
	})
	if !exists {
		return
	}
	cell := &p.out.Cells[index]
	cell.History = append(cell.History, queryapi.MeasurementHistory{
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
	cell *queryapi.MeasurementCoverageCell, task api.BatchEnergyOptimization,
	assessment queryapi.MeasurementAssessment,
) {
	variant := queryapi.CoverageFindVariant(cell, queryapi.CoverageRequestID(task), task.Spec, p.out.Scope.GoalBasis)
	variant.MeasuredAt = queryapi.CoverageMeasurementTime(task, time.Time{})
	variant.RequestIDs = append(variant.RequestIDs, queryapi.CoverageRequestID(task))
	variant.Assessment = assessment
	variant.Assessment.Reasons = append(variant.Assessment.Reasons, queryapi.CoverageReason(
		"PlannedConditionUnconfirmed", "요청 spec만 확인했으며 동결 실행 조건의 중복 여부는 검사에서 확인해야 합니다",
	))
	targets := make([]int, 0, len(task.Spec.BatchSizes))
	for _, batch := range task.Spec.BatchSizes {
		targets = append(targets, int(batch))
	}
	variant.BatchProgress.Target = queryapi.CoverageBatches(append(variant.BatchProgress.Target, targets...))
	for _, run := range p.owners[string(task.UID)] {
		variant.RunIDs = append(variant.RunIDs, string(run.UID))
	}
}

func (p *coverageProjection) addProfile(
	cell *queryapi.MeasurementCoverageCell, task api.BatchEnergyOptimization, profile serving.Profile,
) {
	facts := p.base.catalog.MeasurementFacts[profile.ID]
	variant := queryapi.CoverageFindVariant(cell, queryapi.CoverageRequestID(task), facts.Condition, p.out.Scope.GoalBasis)
	variant.MeasuredAt = queryapi.CoverageMeasurementTime(task, profile.MeasuredAt)
	variant.RequestIDs = append(variant.RequestIDs, queryapi.CoverageRequestID(task))
	variant.ProfileIDs = append(variant.ProfileIDs, profile.ID)
	variant.BatchProgress.Target = queryapi.CoverageBatches(append(
		variant.BatchProgress.Target, facts.Condition.PlannedBatchSizes...,
	))
	for _, run := range p.owners[string(task.UID)] {
		variant.RunIDs = append(variant.RunIDs, string(run.UID))
	}
	for _, batch := range facts.Batches {
		row := coverageResultRow(profile, facts, batch, "direct")
		row.Assessment.Scope.SchedulerUsed = ptr.To(p.adopted[profile.ID])
		if !p.adopted[profile.ID] {
			row.Assessment.Scope.Facts = append(row.Assessment.Scope.Facts, "현재 스케줄러 미사용")
		}
		if profile.Reuse != nil {
			row.RowKind = queryapi.ResultRowConfirmation
			row.SourceProfileID = ptr.To(profile.Reuse.SourceProfileID)
			row.Reuse = profile.Reuse
		}
		variant.ResultRows = append(variant.ResultRows, row)
		variant.BatchProgress.Measured = append(variant.BatchProgress.Measured, batch.Batch)
		if row.Assessment.Status == queryapi.MeasurementCriteriaMet {
			variant.BatchProgress.CriteriaMet = append(variant.BatchProgress.CriteriaMet, batch.Batch)
		}
	}
	p.addReuseSource(variant, profile)
	used := p.adopted[profile.ID]
	assessment := queryapi.AssessMeasurement(queryapi.MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: profile.Reuse != nil, SchedulerUsed: &used,
	})
	p.out.Entities.Profiles = append(p.out.Entities.Profiles, queryapi.MeasurementEntity{
		UID: facts.Request.UID, ID: profile.ID, Status: assessment.DisplayStatus, Label: assessment.Label,
		CellIDs: []string{cell.ID},
	})
}

func (p *coverageProjection) addReuseSource(
	variant *queryapi.MeasurementCoverageVariant, profile serving.Profile,
) {
	if profile.Reuse == nil {
		return
	}
	source, ok := p.base.catalog.MeasurementFacts[profile.Reuse.SourceProfileID]
	if !ok || source.Request.Namespace != p.namespace {
		return
	}
	for _, batch := range source.Batches {
		row := coverageResultRow(profile, source, batch, queryapi.ResultRowSourceReference)
		row.ProfileID = profile.Reuse.SourceProfileID
		row.SourceProfileID = ptr.To(profile.Reuse.SourceProfileID)
		row.TotalJoulesPerItem = nil
		row.Reuse = profile.Reuse
		variant.ResultRows = append(variant.ResultRows, row)
	}
}

func (p *coverageProjection) addRuns() {
	for _, run := range p.base.runs.Items {
		if p.query.Node != "" && run.Spec.TargetNode != p.query.Node {
			continue
		}
		candidate := run.Spec.Candidate
		id := queryapi.CoverageCellID(
			candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode,
		)
		cellIDs := []string{}
		if _, exists := p.indices[id]; exists {
			cellIDs = append(cellIDs, id)
		}
		assessment := queryapi.AssessProfilingRun(run)
		p.out.Entities.Runs = append(p.out.Entities.Runs, queryapi.MeasurementRunEntity{
			UID: string(run.UID), Status: assessment.DisplayStatus, Label: assessment.Label, CellIDs: cellIDs,
		})
	}
}

func (p *coverageProjection) assessCell(cell *queryapi.MeasurementCoverageCell) {
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
	*cell = queryapi.AssessMeasurementCoverageCell(*cell, current,
		p.base.failures["BEO"] == nil && p.base.failures["Run"] == nil)
}

func (p *coverageProjection) assessVariant(variant *queryapi.MeasurementCoverageVariant) {
	progress := &variant.BatchProgress
	if len(p.query.Batches) > 0 {
		progress.Target = append([]int{}, p.query.Batches...)
	}
	progress.Target = queryapi.CoverageBatches(progress.Target)
	progress.Measured = queryapi.CoverageBatches(progress.Measured)
	progress.CriteriaMet = queryapi.CoverageBatches(progress.CriteriaMet)
	progress.InProgress = queryapi.CoverageBatches(progress.InProgress)
	for _, batch := range progress.Target {
		if queryapi.CoverageContains(progress.Measured, batch) {
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
	checks := []queryapi.MeasurementAssessment{}
	used, reused := false, false
	for _, id := range variant.ProfileIDs {
		used = used || p.adopted[id]
	}
	for _, row := range variant.ResultRows {
		if row.RowKind != queryapi.ResultRowSourceReference && queryapi.CoverageContains(progress.Target, row.Batch) {
			assessment := row.Assessment
			checks = append(checks, assessment)
		}
		reused = reused || row.RowKind == queryapi.ResultRowConfirmation
	}
	input := queryapi.MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: reused, SchedulerUsed: &used, MissingBatches: progress.Missing,
	}
	if len(progress.Missing) == 0 && len(checks) > 0 {
		input.Children = checks
		if condition, ok := variant.Condition.(profilederivation.CurvePlanSpec); ok {
			input.Children = append(input.Children, queryapi.AssessMeasurement(queryapi.MeasurementAssessmentInput{
				Accepted: true, Available: true,
				Checks: []queryapi.MeasurementCheck{coverageDatasetCheck(p.base, condition)},
			}))
		}
	}
	variant.Assessment = queryapi.AssessMeasurement(input)
}

func coverageResultRow(
	profile serving.Profile, facts profilederivation.MeasurementFacts,
	batch profilederivation.MeasurementBatchFacts, kind string,
) queryapi.ResultRow {
	row := queryapi.MeasurementResultRow(facts, batch, profile.ID, profile.Reuse != nil, true)
	row.RowKind = kind
	row.ID = fmt.Sprintf("%s/%s/%d", profile.ID, kind, batch.Batch)
	return row
}

func coverageDatasetCheck(
	base *measurementCoverageBase, condition profilederivation.CurvePlanSpec,
) queryapi.MeasurementCheck {
	check := queryapi.MeasurementCheck{
		Code: "DatasetInputContract", Result: "unknown",
		Message:   "입력 계약 검증 기록 없음: 현재 카탈로그와 동결 조건의 연결을 확인하지 못했습니다",
		SourceIDs: []string{},
	}
	if base.failures["input-contract"] != nil || condition.ActiveProtocol.Dataset == nil {
		return check
	}
	for _, dataset := range base.datasets.Items {
		if dataset.Name != condition.ActiveProtocol.Dataset.Name ||
			string(dataset.UID) != condition.ActiveProtocol.Dataset.UID {
			continue
		}
		for _, pre := range base.preprocessors.Items {
			if pre.Name != condition.Preprocessor.Name || len(pre.Spec.Input.Fields) == 0 {
				continue
			}
			artifact, found := modelcatalog.PreprocessorArtifact(&pre, condition.LogicalModel, condition.Artifact.Name)
			if !found || artifact.Source.Image != condition.Preprocessor.Image ||
				artifact.Source.Path != condition.Preprocessor.Path {
				continue
			}
			if mismatched := modelcatalog.Compatible(&dataset, &pre); len(mismatched) > 0 {
				check.Result = "fail"
				check.Message = "현재 카탈로그의 모델·데이터셋 입력 필드 불일치: " + strings.Join(mismatched, ", ")
			} else {
				check.Result = "pass"
				check.Message = "현재 카탈로그의 모델·데이터셋 입력 필드 일치 · 데이터 용도 승인은 별도"
			}
			return check
		}
	}
	return check
}
