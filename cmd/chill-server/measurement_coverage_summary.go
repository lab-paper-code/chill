package main

import (
	"slices"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"k8s.io/utils/ptr"
)

func coverageSummarize(out *operatorconsole.MeasurementCoverageResponse, base *measurementCoverageBase) {
	summary := newCoverageSummary(out)
	cells := make(map[string]operatorconsole.MeasurementCoverageCell, len(out.Cells))
	actions := map[string]int{}
	for _, cell := range out.Cells {
		cells[cell.ID] = cell
		countCoverageCell(&summary, cell)
		addCoverageActions(out, cell, actions)
	}
	countCoverageFailedRequests(&summary, out.Entities.Requests, cells)
	for i := range out.Groups {
		summarizeCoverageGroup(&out.Groups[i], cells)
	}
	if base.failures["compatibility"] != nil || base.failures["inventory"] != nil {
		summary.SupportedCellCount = nil
		summary.UnsupportedCellCount = nil
		for i := range out.Groups {
			out.Groups[i].Supported = nil
		}
	}
	if base.failures["BEO"] != nil || base.failures["Run"] != nil {
		summary.CountsByDisplayStatus[operatorconsole.MeasurementUnmeasured] = nil
	}
	ids := make([]string, 0, len(out.Entities.Runs))
	for _, run := range out.Entities.Runs {
		ids = append(ids, run.UID)
	}
	slices.Sort(ids)
	summary.RunCount = ptr.To(len(slices.Compact(ids)))
	out.Summary = summary
}

func newCoverageSummary(out *operatorconsole.MeasurementCoverageResponse) operatorconsole.MeasurementCoverageSummary {
	summary := operatorconsole.MeasurementCoverageSummary{
		CountsByDisplayStatus: map[string]*int{}, RequestCount: ptr.To(len(out.Entities.Requests)),
		RunCount: ptr.To(len(out.Entities.Runs)), ProfileCount: ptr.To(len(out.Entities.Profiles)),
		SupportedCellCount: ptr.To(0), CompleteCellCount: ptr.To(0), CriteriaMetCellCount: ptr.To(0),
		ReusedCellCount: ptr.To(0), UnknownSupportCount: ptr.To(0), UnsupportedCellCount: ptr.To(0),
		FailedCellCount: ptr.To(0), CurrentFailedRequestCount: ptr.To(0), HistoricalFailedRequestCount: ptr.To(0),
	}
	for _, status := range []string{
		operatorconsole.MeasurementUsable, operatorconsole.MeasurementReview, operatorconsole.MeasurementInsufficient, operatorconsole.MeasurementProblem,
		operatorconsole.MeasurementRunning, operatorconsole.MeasurementUnmeasured,
		operatorconsole.MeasurementUnsupported, operatorconsole.MeasurementUnknown,
	} {
		summary.CountsByDisplayStatus[status] = ptr.To(0)
	}
	return summary
}

func countCoverageCell(
	summary *operatorconsole.MeasurementCoverageSummary, cell operatorconsole.MeasurementCoverageCell,
) {
	*summary.CountsByDisplayStatus[cell.Assessment.DisplayStatus]++
	switch cell.Support.Verdict {
	case operatorconsole.MeasurementSupportCompatible:
		*summary.SupportedCellCount++
	case operatorconsole.MeasurementSupportIncompatible:
		*summary.UnsupportedCellCount++
	default:
		*summary.UnknownSupportCount++
	}
	if cell.Assessment.DisplayStatus == operatorconsole.MeasurementUsable {
		*summary.CompleteCellCount++
	}
	if cell.Assessment.Status == operatorconsole.MeasurementCriteriaMet {
		*summary.CriteriaMetCellCount++
	}
	if cell.Assessment.Scope.Reused && cell.Assessment.DisplayStatus == operatorconsole.MeasurementUsable {
		*summary.ReusedCellCount++
	}
	if cell.Assessment.DisplayStatus == operatorconsole.MeasurementProblem {
		*summary.FailedCellCount++
	}
}

func addCoverageActions(
	out *operatorconsole.MeasurementCoverageResponse, cell operatorconsole.MeasurementCoverageCell, actions map[string]int,
) {
	if cell.Assessment.DisplayStatus == operatorconsole.MeasurementProblem &&
		len(cell.FailureSummary) == 0 && actions["failure"] < 3 {
		message := "문제 조건의 원인을 확인하세요"
		code := "MeasurementProblem"
		if len(cell.Assessment.Reasons) > 0 {
			message = cell.Assessment.Reasons[0].Message
			code = cell.Assessment.Reasons[0].Code
		}
		out.NextActions = append(out.NextActions, operatorconsole.MeasurementNextAction{
			Kind: "failure", CellID: cell.ID, ReasonCode: code, Message: message,
		})
		actions["failure"]++
	}
	for _, failure := range cell.FailureSummary {
		if cell.Assessment.DisplayStatus == operatorconsole.MeasurementUsable || actions["failure"] >= 3 {
			continue
		}
		out.NextActions = append(out.NextActions, operatorconsole.MeasurementNextAction{
			Kind: "failure", CellID: cell.ID, EntityID: failure.RequestID, ReasonCode: failure.Code, Message: failure.Message,
		})
		actions["failure"]++
	}
	for _, variant := range cell.Variants {
		if len(variant.BatchProgress.Missing) == 0 || actions["missing-batches"] >= 3 {
			continue
		}
		out.NextActions = append(out.NextActions, operatorconsole.MeasurementNextAction{
			Kind: "missing-batches", CellID: cell.ID, VariantID: ptr.To(variant.ID),
			ReasonCode: "TargetBatchMissing", Message: "목표 batch의 부족 범위를 확인하세요",
		})
		actions["missing-batches"]++
	}
	if (cell.Assessment.Status == operatorconsole.MeasurementUnknown || cell.Assessment.Status == operatorconsole.MeasurementReview) &&
		cell.Support.Verdict != operatorconsole.MeasurementSupportIncompatible && actions["evidence"] < 3 {
		message := "지원 입력과 동결 근거를 확인하세요"
		if cell.Assessment.Status == operatorconsole.MeasurementReview {
			message = "입력 계약·품질의 확인된 불일치를 상세에서 확인하세요"
		}
		out.NextActions = append(out.NextActions, operatorconsole.MeasurementNextAction{
			Kind: "evidence", CellID: cell.ID, ReasonCode: "EvidenceUnconfirmed", Message: message,
		})
		actions["evidence"]++
	}
}

func countCoverageFailedRequests(
	summary *operatorconsole.MeasurementCoverageSummary, requests []operatorconsole.MeasurementEntity,
	cells map[string]operatorconsole.MeasurementCoverageCell,
) {
	for _, request := range requests {
		if !request.Failed {
			continue
		}
		resolved := false
		for _, id := range request.CellIDs {
			resolved = resolved || cells[id].Assessment.DisplayStatus == operatorconsole.MeasurementUsable
		}
		if resolved {
			*summary.HistoricalFailedRequestCount++
		} else {
			*summary.CurrentFailedRequestCount++
		}
	}
}

func summarizeCoverageGroup(
	group *operatorconsole.MeasurementCoverageGroup, cells map[string]operatorconsole.MeasurementCoverageCell,
) {
	for _, id := range group.CellIDs {
		cell := cells[id]
		if cell.Assessment.DisplayStatus == operatorconsole.MeasurementUsable {
			group.Complete++
		}
		switch cell.Support.Verdict {
		case operatorconsole.MeasurementSupportCompatible:
			*group.Supported++
		case operatorconsole.MeasurementSupportUnknown:
			group.UnknownSupport++
		}
	}
}
