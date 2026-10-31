package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"k8s.io/utils/ptr"
)

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
func coverageReason(code, message string) operatorconsole.MeasurementReason {
	return operatorconsole.MeasurementReason{Code: code, Message: message, SourceIDs: []string{}}
}

type coverageProjection struct {
	base      *measurementCoverageBase
	namespace string
	query     httpapi.MeasurementCoverageQuery
	out       operatorconsole.MeasurementCoverageResponse
	indices   map[string]int
	owners    map[string][]api.ProfilingRun
	profiles  map[string][]serving.Profile
	adopted   map[string]bool
}

func buildMeasurementCoverage(
	base *measurementCoverageBase, namespace string, query httpapi.MeasurementCoverageQuery,
) operatorconsole.MeasurementCoverageResponse {
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
	p.out = operatorconsole.MeasurementCoverageResponse{
		SchemaVersion: "measurement-coverage.v1", AssessmentVersion: "result-checklist.v1",
		SnapshotID: p.namespace + "/" + p.base.catalog.Revision + "/" +
			p.base.requests.ResourceVersion + "/" + p.base.runs.ResourceVersion,
		DataAsOf:  p.base.oldest().UTC().Format(time.RFC3339Nano),
		Freshness: "fresh", Completeness: operatorconsole.MeasurementComplete,
		Scope: operatorconsole.MeasurementCoverageScope{
			Namespace: p.namespace, SupportBasis: supportBasis, GoalBasis: basis,
			TargetBatches: append([]int{}, p.query.Batches...),
		},
		Sources: []operatorconsole.MeasurementCoverageSource{}, Groups: []operatorconsole.MeasurementCoverageGroup{},
		Cells: []operatorconsole.MeasurementCoverageCell{}, NextActions: []operatorconsole.MeasurementNextAction{},
		Entities: operatorconsole.MeasurementCoverageEntities{
			Requests: []operatorconsole.MeasurementEntity{}, Profiles: []operatorconsole.MeasurementEntity{},
			Runs: []operatorconsole.MeasurementRunEntity{},
		},
	}
	if time.Since(p.base.oldest()) > 30*time.Second || len(p.base.failures) > 0 {
		p.out.Freshness = "stale"
	}
}

func (p *coverageProjection) addSources() {
	for _, kind := range measurementCoverageSourceKinds {
		source := operatorconsole.MeasurementCoverageSource{
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
			source.Error = &operatorconsole.MeasurementCoverageSourceError{
				Code: "SourceUnavailable", Message: "입력 갱신 실패: " + kind, Retryable: true,
			}
			p.out.Completeness = operatorconsole.MeasurementPartial
		}
		p.out.Sources = append(p.out.Sources, source)
	}
}

func (p *coverageProjection) supportIndex() map[string]operatorconsole.ModelCompatibilityView {
	support := map[string]operatorconsole.ModelCompatibilityView{}
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
	classes := append([]operatorconsole.InventoryClassView{}, p.base.inventory.Classes...)
	slices.SortFunc(classes, func(a, b operatorconsole.InventoryClassView) int { return strings.Compare(a.Name, b.Name) })
	models := append([]operatorconsole.InventoryModelView{}, p.base.inventory.Models...)
	slices.SortFunc(models, func(a, b operatorconsole.InventoryModelView) int { return strings.Compare(a.Name, b.Name) })
	p.indices = map[string]int{}
	for _, model := range models {
		for _, class := range classes {
			matches := slices.ContainsFunc(class.Nodes, func(node operatorconsole.InventoryNodeView) bool {
				return node.Name == p.query.Node
			})
			if p.query.Node != "" && !matches {
				continue
			}
			group := operatorconsole.MeasurementCoverageGroup{
				Model:       operatorconsole.MeasurementIdentity{Name: model.Name, UID: model.UID},
				DeviceClass: operatorconsole.MeasurementIdentity{Name: class.Name, UID: class.UID},
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
	group operatorconsole.MeasurementCoverageGroup, path, mode string,
	support map[string]operatorconsole.ModelCompatibilityView,
) operatorconsole.MeasurementCoverageCell {
	cell := operatorconsole.MeasurementCoverageCell{
		ID:    coverageCellID(group.Model.Name, group.DeviceClass.Name, path, mode),
		Model: group.Model, DeviceClass: group.DeviceClass, ExecutionPath: path, PowerMode: mode,
		Variants: []operatorconsole.MeasurementCoverageVariant{}, FailureSummary: []operatorconsole.MeasurementFailure{},
		History: []operatorconsole.MeasurementHistory{},
		Support: operatorconsole.MeasurementSupport{
			Verdict: operatorconsole.MeasurementSupportUnknown, Reasons: []operatorconsole.ModelCompatibilityReason{},
		},
	}
	row, found := support[strings.Join([]string{group.Model.Name, group.DeviceClass.Name, path}, "/")]
	if !found {
		cell.Support.Reasons = append(cell.Support.Reasons, operatorconsole.ModelCompatibilityReason{
			Kind: operatorconsole.MeasurementSupportUnknown, Code: "SupportInputMissing", Message: "선택 Node의 지원 입력을 확인할 수 없습니다",
		})
		return cell
	}
	cell.Support.Verdict, cell.Support.Reasons = row.Verdict, row.Reasons
	if row.Inputs.Node.Name != "" {
		cell.Support.Node = &operatorconsole.MeasurementIdentity{Name: row.Inputs.Node.Name, UID: row.Inputs.Node.UID}
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
	failures := operatorconsole.MeasurementRequestFailures(task, p.owners[string(task.UID)], assessment)
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
	p.out.Entities.Requests = append(p.out.Entities.Requests, operatorconsole.MeasurementEntity{
		UID: string(task.UID), ID: requestID, Status: assessment.DisplayStatus, Label: assessment.Label,
		CellIDs: cellIDs, Failed: assessment.Status == operatorconsole.MeasurementFailed,
		FailureSummary: failures,
	})
	if !exists {
		return
	}
	cell := &p.out.Cells[index]
	cell.History = append(cell.History, operatorconsole.MeasurementHistory{
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
	cell *operatorconsole.MeasurementCoverageCell, task api.BatchEnergyOptimization,
	assessment operatorconsole.MeasurementAssessment,
) {
	variant := coverageFindVariant(cell, operatorconsole.MeasurementVariantID(task.Spec), task.Spec, p.out.Scope.GoalBasis)
	variant.RequestIDs = append(variant.RequestIDs, coverageRequestID(task))
	if variant.Assessment.Status == "" || assessment.DisplayStatus == operatorconsole.MeasurementProblem ||
		assessment.Status == operatorconsole.MeasurementRunning {
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
	cell *operatorconsole.MeasurementCoverageCell, task api.BatchEnergyOptimization, profile serving.Profile,
) {
	facts := p.base.catalog.MeasurementFacts[profile.ID]
	variant := coverageFindVariant(cell, operatorconsole.MeasurementVariantID(facts.Condition),
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
			row.RowKind = operatorconsole.ResultRowConfirmation
			row.SourceProfileID = ptr.To(profile.Reuse.SourceProfileID)
			row.Reuse = profile.Reuse
		}
		variant.ResultRows = append(variant.ResultRows, row)
		variant.BatchProgress.Measured = append(variant.BatchProgress.Measured, batch.Batch)
		if row.Assessment.Status == operatorconsole.MeasurementCriteriaMet {
			variant.BatchProgress.CriteriaMet = append(variant.BatchProgress.CriteriaMet, batch.Batch)
		}
	}
	p.addReuseSource(variant, profile)
	used := p.adopted[profile.ID]
	assessment := operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: profile.Reuse != nil, SchedulerUsed: &used,
	})
	p.out.Entities.Profiles = append(p.out.Entities.Profiles, operatorconsole.MeasurementEntity{
		UID: facts.Request.UID, ID: profile.ID, Status: assessment.DisplayStatus, Label: assessment.Label,
		CellIDs: []string{cell.ID},
	})
}

func (p *coverageProjection) addReuseSource(
	variant *operatorconsole.MeasurementCoverageVariant, profile serving.Profile,
) {
	if profile.Reuse == nil {
		return
	}
	source, ok := p.base.catalog.MeasurementFacts[profile.Reuse.SourceProfileID]
	if !ok || source.Request.Namespace != p.namespace {
		return
	}
	for _, batch := range source.Batches {
		row := coverageResultRow(profile, source, batch, operatorconsole.ResultRowSourceReference)
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
		assessment := operatorconsole.AssessProfilingRun(run)
		p.out.Entities.Runs = append(p.out.Entities.Runs, operatorconsole.MeasurementRunEntity{
			UID: string(run.UID), Status: assessment.DisplayStatus, Label: assessment.Label, CellIDs: cellIDs,
		})
	}
}

func (p *coverageProjection) assessCell(cell *operatorconsole.MeasurementCoverageCell) {
	children := make([]operatorconsole.MeasurementAssessment, 0, len(cell.Variants))
	for i := range cell.Variants {
		p.assessVariant(&cell.Variants[i])
		children = append(children, cell.Variants[i].Assessment)
	}
	cell.VariantCount = len(cell.Variants)
	if len(cell.Variants) == 1 {
		variant := &cell.Variants[0]
		cell.BatchProgress = &variant.BatchProgress
		for _, row := range variant.ResultRows {
			if row.RowKind == operatorconsole.ResultRowSourceReference {
				continue
			}
			cell.Representative = &operatorconsole.MeasurementRepresentative{
				VariantID: variant.ID, Batch: row.Batch, IncrementalJoulesPerItem: row.IncrementalJoulesPerItem,
				P95Milliseconds: row.P95Milliseconds,
			}
			break
		}
	}
	cell.Assessment = operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{
		Children: children, Support: cell.Support.Verdict, Attempted: len(cell.History) > 0,
		SourcesComplete: p.base.failures["BEO"] == nil && p.base.failures["Run"] == nil,
	})
	if cell.Support.Verdict == operatorconsole.MeasurementSupportIncompatible && cell.Assessment.Evidence.Accepted {
		cell.Assessment = operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{
			Accepted: true, Available: true, Problems: []operatorconsole.MeasurementReason{
				coverageReason("CurrentDeclarationConflict", "보존 측정과 현재 지원 선언이 일치하지 않습니다"),
			},
		})
	}
}

func (p *coverageProjection) assessVariant(variant *operatorconsole.MeasurementCoverageVariant) {
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
	checks := []operatorconsole.MeasurementAssessment{}
	used, reused := false, false
	for _, id := range variant.ProfileIDs {
		used = used || p.adopted[id]
	}
	for _, row := range variant.ResultRows {
		if row.RowKind != operatorconsole.ResultRowSourceReference && coverageContains(progress.Target, row.Batch) {
			assessment := row.Assessment
			checks = append(checks, assessment)
		}
		reused = reused || row.RowKind == operatorconsole.ResultRowConfirmation
	}
	input := operatorconsole.MeasurementAssessmentInput{
		Accepted: true, Available: true, Reused: reused, SchedulerUsed: &used, MissingBatches: progress.Missing,
	}
	if len(progress.Missing) == 0 && len(checks) > 0 {
		input.Children = checks
		if condition, ok := variant.Condition.(profilederivation.CurvePlanSpec); ok {
			input.Children = append(input.Children, operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{
				Accepted: true, Available: true, Checks: []operatorconsole.MeasurementCheck{coverageDatasetCheck(p.base, condition)},
			}))
		}
	}
	variant.Assessment = operatorconsole.AssessMeasurement(input)
}

func coverageFindVariant(
	cell *operatorconsole.MeasurementCoverageCell, id string, condition any, basis string,
) *operatorconsole.MeasurementCoverageVariant {
	for i := range cell.Variants {
		if cell.Variants[i].ID == id {
			return &cell.Variants[i]
		}
	}
	cell.Variants = append(cell.Variants, operatorconsole.MeasurementCoverageVariant{
		ID: id, Condition: condition, ResultRows: []operatorconsole.ResultRow{}, RequestIDs: []string{},
		RunIDs: []string{}, ProfileIDs: []string{},
		BatchProgress: operatorconsole.MeasurementBatchProgress{
			Basis: basis, Target: []int{}, Measured: []int{}, CriteriaMet: []int{}, Missing: []int{},
			InProgress: []int{}, Unknown: []int{},
		},
	})
	return &cell.Variants[len(cell.Variants)-1]
}

func coverageResultRow(
	profile serving.Profile, facts profilederivation.MeasurementFacts,
	batch profilederivation.MeasurementBatchFacts, kind string,
) operatorconsole.ResultRow {
	row := operatorconsole.MeasurementResultRow(facts, batch, profile.ID, profile.Reuse != nil, true)
	row.RowKind = kind
	row.ID = fmt.Sprintf("%s/%s/%d", profile.ID, kind, batch.Batch)
	return row
}

func coverageDatasetCheck(base *measurementCoverageBase, condition profilederivation.CurvePlanSpec) operatorconsole.MeasurementCheck {
	check := operatorconsole.MeasurementCheck{
		Code: "DatasetInputContract", Result: "unknown",
		Message: "입력 계약 검증 기록 없음: 현재 카탈로그와 동결 조건의 연결을 확인하지 못했습니다",
		SourceIDs: []string{},
	}
	if base.failures["input-contract"] != nil || condition.ActiveProtocol.Dataset == nil {
		return check
	}
	for _, dataset := range base.datasets.Items {
		if dataset.Name != condition.ActiveProtocol.Dataset.Name || string(dataset.UID) != condition.ActiveProtocol.Dataset.UID {
			continue
		}
		for _, pre := range base.preprocessors.Items {
			if pre.Name != condition.Preprocessor.Name || len(pre.Spec.Input.Fields) == 0 {
				continue
			}
			artifact, found := modelcatalog.PreprocessorArtifact(&pre, condition.LogicalModel, condition.Artifact.Name)
			if !found || artifact.Source.Image != condition.Preprocessor.Image || artifact.Source.Path != condition.Preprocessor.Path {
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
