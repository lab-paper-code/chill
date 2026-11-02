package main

import (
	"fmt"
	"slices"
	"sort"
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

const coveragePreviousRuntime = "PreviousRuntime"

func coverageCellID(model, class, path, mode string) string {
	return strings.Join([]string{model, class, path, mode}, "/")
}
func coverageRequestID(task api.BatchEnergyOptimization) string {
	return task.Namespace + "/" + task.Name + "/" + string(task.UID)
}
func coverageBatches(values []int) []int {
	out := append([]int{}, values...)
	sort.Ints(out)
	return slices.Compact(out)
}
func coverageContains(values []int, batch int) bool { return slices.Contains(values, batch) }
func coverageReason(code, message string) queryapi.MeasurementReason {
	return queryapi.MeasurementReason{Code: code, Message: message, SourceIDs: []string{}}
}

type coverageProjection struct {
	base      *measurementCoverageBase
	namespace string
	query     httpapi.MeasurementCoverageQuery
	out       queryapi.MeasurementCoverageResponse
	indices   map[string]int
	owners    map[string][]api.ProfilingRun
	profiles  map[string][]serving.Profile
	adopted   map[string]bool
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
	}
	coverageSummarize(&p.out, base)
	compactCoverageResponse(&p.out)
	return p.out
}

func (p *coverageProjection) initialize() {
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
			p.base.requests.ResourceVersion + "/" + p.base.runs.ResourceVersion,
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
		p.out.Freshness = "stale"
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
		case "catalog":
			source.ResourceVersions = []string{p.base.catalog.Revision}
		case "input-contract":
			source.ResourceVersions = []string{p.base.datasets.ResourceVersion, p.base.preprocessors.ResourceVersion}
		case "BEO":
			source.ResourceVersions = []string{p.base.requests.ResourceVersion}
		case "Run":
			source.ResourceVersions = []string{p.base.runs.ResourceVersion}
		case "compatibility":
			for _, row := range p.base.compatibility.NodeRows {
				if row.Inputs.RuntimeDeclaration != nil {
					source.ResourceVersions = append(source.ResourceVersions, row.Inputs.RuntimeDeclaration.ResourceVersion)
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
					cell := coverageNewCell(group, path.Name, mode.Name, support)
					p.indices[cell.ID] = len(p.out.Cells)
					p.out.Cells = append(p.out.Cells, cell)
					group.CellIDs = append(group.CellIDs, cell.ID)
				}
			}
			p.out.Groups = append(p.out.Groups, group)
		}
	}
}

func coverageNewCell(
	group queryapi.MeasurementCoverageGroup, path, mode string,
	support map[string]queryapi.ModelCompatibilityView,
) queryapi.MeasurementCoverageCell {
	cell := queryapi.MeasurementCoverageCell{
		ID:    coverageCellID(group.Model.Name, group.DeviceClass.Name, path, mode),
		Model: group.Model, DeviceClass: group.DeviceClass, ExecutionPath: path, PowerMode: mode,
		Variants: []queryapi.MeasurementCoverageVariant{}, FailureSummary: []queryapi.MeasurementFailure{},
		History: []queryapi.MeasurementHistory{},
		Support: queryapi.MeasurementSupport{
			Verdict: queryapi.MeasurementSupportUnknown, Reasons: []queryapi.ModelCompatibilityReason{},
		},
	}
	row, found := support[strings.Join([]string{group.Model.Name, group.DeviceClass.Name, path}, "/")]
	if !found {
		cell.Support.Reasons = append(cell.Support.Reasons, queryapi.ModelCompatibilityReason{
			Kind: queryapi.MeasurementSupportUnknown, Code: "SupportInputMissing", Message: "선택 Node의 지원 입력을 확인할 수 없습니다",
		})
		return cell
	}
	cell.Support.Verdict, cell.Support.Reasons = row.Verdict, row.Reasons
	if row.Inputs.Node.Name != "" {
		cell.Support.Node = &queryapi.MeasurementIdentity{Name: row.Inputs.Node.Name, UID: row.Inputs.Node.UID}
	}
	return cell
}

func (p *coverageProjection) indexEvidence() {
	p.owners = coverageOwnedRuns(p.base)
	p.profiles = map[string][]serving.Profile{}
	p.adopted = map[string]bool{}
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

func coverageOwnedRuns(base *measurementCoverageBase) map[string][]api.ProfilingRun {
	owners := map[string][]api.ProfilingRun{}
	requests := map[string]api.BatchEnergyOptimization{}
	for _, task := range base.requests.Items {
		requests[string(task.UID)] = task
	}
	for _, run := range base.runs.Items {
		for _, owner := range run.OwnerReferences {
			task, exists := requests[string(owner.UID)]
			if !exists || run.Namespace != task.Namespace || owner.Name != task.Name {
				continue
			}
			if owner.Controller == nil || !*owner.Controller || owner.APIVersion != api.GroupVersion.String() {
				continue
			}
			if owner.Kind == "BatchEnergyOptimization" {
				owners[string(owner.UID)] = append(owners[string(owner.UID)], run)
				break
			}
		}
	}
	return owners
}

func (p *coverageProjection) addRequest(task api.BatchEnergyOptimization) {
	if p.query.Node != "" && task.Spec.State.TargetNode != p.query.Node {
		return
	}
	candidate := task.Spec.State.Candidate
	id := coverageCellID(candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode)
	index, exists := p.indices[id]
	requestID := coverageRequestID(task)
	assessment := assessMeasurementRequest(task, p.base.catalog)
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
	variant := coverageFindVariant(cell, queryapi.MeasurementVariantID(task.Spec), task.Spec, p.out.Scope.GoalBasis)
	variant.RequestIDs = append(variant.RequestIDs, coverageRequestID(task))
	if variant.Assessment.Status == "" || assessment.DisplayStatus == queryapi.MeasurementProblem ||
		assessment.Status == queryapi.MeasurementRunning {
		variant.Assessment = assessment
	}
	variant.Assessment.Reasons = append(variant.Assessment.Reasons, coverageReason(
		"PlannedConditionUnconfirmed", "요청 spec만 확인했으며 동결 실행 조건의 중복 여부는 검사에서 확인해야 합니다",
	))
	targets := make([]int, 0, len(task.Spec.BatchSizes))
	for _, batch := range task.Spec.BatchSizes {
		targets = append(targets, int(batch))
	}
	variant.BatchProgress.Target = coverageBatches(append(variant.BatchProgress.Target, targets...))
	for _, run := range p.owners[string(task.UID)] {
		variant.RunIDs = append(variant.RunIDs, string(run.UID))
	}
}

func (p *coverageProjection) addProfile(
	cell *queryapi.MeasurementCoverageCell, task api.BatchEnergyOptimization, profile serving.Profile,
) {
	facts := p.base.catalog.MeasurementFacts[profile.ID]
	variant := coverageFindVariant(cell, queryapi.MeasurementVariantID(facts.Condition),
		facts.Condition, p.out.Scope.GoalBasis)
	variant.RequestIDs = append(variant.RequestIDs, coverageRequestID(task))
	variant.ProfileIDs = append(variant.ProfileIDs, profile.ID)
	variant.BatchProgress.Target = coverageBatches(append(
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
		id := coverageCellID(candidate.ModelSpec, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode)
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
	declarationsRead := p.base.failures["compatibility"] == nil && len(p.base.compatibility.NodeRows) > 0 &&
		!slices.ContainsFunc(p.base.compatibility.NodeRows, func(row queryapi.ModelCompatibilityView) bool {
			return slices.ContainsFunc(row.Reasons, func(reason queryapi.ModelCompatibilityReason) bool {
				return reason.Code == "CompatibilityReadFailed"
			})
		})
	children := make([]queryapi.MeasurementAssessment, 0, len(cell.Variants))
	var current *queryapi.MeasurementCoverageVariant
	for i := range cell.Variants {
		variant := &cell.Variants[i]
		p.assessVariant(variant)
		if condition, ok := variant.Condition.(profilederivation.CurvePlanSpec); ok && declarationsRead {
			declared := slices.ContainsFunc(p.base.compatibility.NodeRows,
				func(row queryapi.ModelCompatibilityView) bool {
					return condition.RuntimeImage != "" && row.Inputs.RuntimeDeclaration != nil &&
						row.Inputs.RuntimeDeclaration.Image == condition.RuntimeImage
				})
			if !declared {
				variant.Assessment.Reasons = append(variant.Assessment.Reasons, coverageReason(
					coveragePreviousRuntime, "이전 runtime · 현재 실행 선언에 없어 셀 집계에서 제외",
				))
				continue
			}
		}
		current = variant
		children = append(children, variant.Assessment)
	}
	cell.VariantCount = len(cell.Variants)
	if len(cell.Variants) == 1 && len(children) == 1 {
		variant := current
		cell.BatchProgress = &variant.BatchProgress
		for _, row := range variant.ResultRows {
			if row.RowKind == queryapi.ResultRowSourceReference {
				continue
			}
			cell.Representative = &queryapi.MeasurementRepresentative{
				VariantID: variant.ID, Batch: row.Batch, IncrementalJoulesPerItem: row.IncrementalJoulesPerItem,
				P95Milliseconds: row.P95Milliseconds,
			}
			break
		}
	}
	support := cell.Support.Verdict
	if len(cell.Variants) > 0 && len(children) == 0 {
		// Retained measurements on removed runtimes do not constitute a current attempt.
		support = queryapi.MeasurementSupportCompatible
	}
	cell.Assessment = queryapi.AssessMeasurement(queryapi.MeasurementAssessmentInput{
		Children: children, Support: support, Attempted: len(children) > 0 && len(cell.History) > 0,
		SourcesComplete: p.base.failures["BEO"] == nil && p.base.failures["Run"] == nil,
	})
	if cell.Support.Verdict == queryapi.MeasurementSupportIncompatible && cell.Assessment.Evidence.Accepted {
		cell.Assessment = queryapi.AssessMeasurement(queryapi.MeasurementAssessmentInput{
			Accepted: true, Available: true, Problems: []queryapi.MeasurementReason{
				coverageReason("CurrentDeclarationConflict", "보존 측정과 현재 지원 선언이 일치하지 않습니다"),
			},
		})
	}
}

func (p *coverageProjection) assessVariant(variant *queryapi.MeasurementCoverageVariant) {
	progress := &variant.BatchProgress
	if len(p.query.Batches) > 0 {
		progress.Target = append([]int{}, p.query.Batches...)
	}
	progress.Target = coverageBatches(progress.Target)
	progress.Measured = coverageBatches(progress.Measured)
	progress.CriteriaMet = coverageBatches(progress.CriteriaMet)
	progress.InProgress = coverageBatches(progress.InProgress)
	for _, batch := range progress.Target {
		if coverageContains(progress.Measured, batch) {
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
		if row.RowKind != queryapi.ResultRowSourceReference && coverageContains(progress.Target, row.Batch) {
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

func coverageFindVariant(
	cell *queryapi.MeasurementCoverageCell, id string, condition any, basis string,
) *queryapi.MeasurementCoverageVariant {
	for i := range cell.Variants {
		if cell.Variants[i].ID == id {
			return &cell.Variants[i]
		}
	}
	cell.Variants = append(cell.Variants, queryapi.MeasurementCoverageVariant{
		ID: id, Condition: condition, ResultRows: []queryapi.ResultRow{}, RequestIDs: []string{},
		RunIDs: []string{}, ProfileIDs: []string{},
		BatchProgress: queryapi.MeasurementBatchProgress{
			Basis: basis, Target: []int{}, Measured: []int{}, CriteriaMet: []int{}, Missing: []int{},
			InProgress: []int{}, Unknown: []int{},
		},
	})
	return &cell.Variants[len(cell.Variants)-1]
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
