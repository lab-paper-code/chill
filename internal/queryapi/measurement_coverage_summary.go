package queryapi

import (
	"slices"

	"k8s.io/utils/ptr"
)

func SummarizeMeasurementCoverage(out *MeasurementCoverageResponse, failures map[string]error) {
	summary := newCoverageSummary(out)
	cells := make(map[string]MeasurementCoverageCell, len(out.Cells))
	actions := map[string]int{}
	for _, cell := range out.Cells {
		cells[cell.ID] = cell
		countCoverageCell(&summary, cell)
		AddCoverageActions(out, cell, actions)
	}
	CountCoverageFailedRequests(&summary, out.Entities.Requests, cells)
	for i := range out.Groups {
		summarizeCoverageGroup(&out.Groups[i], cells)
	}
	if failures["compatibility"] != nil || failures["inventory"] != nil {
		summary.SupportedCellCount = nil
		summary.UnsupportedCellCount = nil
		for i := range out.Groups {
			out.Groups[i].Supported = nil
		}
	}
	if failures["BEO"] != nil || failures["Run"] != nil {
		summary.CountsByDisplayStatus[MeasurementUnmeasured] = nil
	}
	ids := make([]string, 0, len(out.Entities.Runs))
	for _, run := range out.Entities.Runs {
		ids = append(ids, run.UID)
	}
	slices.Sort(ids)
	summary.RunCount = ptr.To(len(slices.Compact(ids)))
	out.Summary = summary
}

func newCoverageSummary(out *MeasurementCoverageResponse) MeasurementCoverageSummary {
	summary := MeasurementCoverageSummary{
		CountsByDisplayStatus: map[string]*int{}, RequestCount: ptr.To(len(out.Entities.Requests)),
		RunCount: ptr.To(len(out.Entities.Runs)), ProfileCount: ptr.To(len(out.Entities.Profiles)),
		SupportedCellCount: ptr.To(0), CompleteCellCount: ptr.To(0), CriteriaMetCellCount: ptr.To(0),
		ReusedCellCount: ptr.To(0), UnknownSupportCount: ptr.To(0), UnsupportedCellCount: ptr.To(0),
		FailedCellCount: ptr.To(0), CurrentFailedRequestCount: ptr.To(0), HistoricalFailedRequestCount: ptr.To(0),
	}
	for _, status := range []string{
		MeasurementUsable, MeasurementReview,
		MeasurementInsufficient, MeasurementProblem,
		MeasurementRunning, MeasurementUnmeasured,
		MeasurementUnsupported, MeasurementUnknown,
	} {
		summary.CountsByDisplayStatus[status] = ptr.To(0)
	}
	return summary
}

func countCoverageCell(
	summary *MeasurementCoverageSummary, cell MeasurementCoverageCell,
) {
	*summary.CountsByDisplayStatus[cell.Assessment.DisplayStatus]++
	switch cell.Support.Verdict {
	case MeasurementSupportCompatible:
		*summary.SupportedCellCount++
	case MeasurementSupportIncompatible:
		*summary.UnsupportedCellCount++
	default:
		*summary.UnknownSupportCount++
	}
	if cell.Assessment.DisplayStatus == MeasurementUsable {
		*summary.CompleteCellCount++
	}
	if cell.Assessment.Status == MeasurementCriteriaMet {
		*summary.CriteriaMetCellCount++
	}
	if cell.Assessment.Scope.Reused && cell.Assessment.DisplayStatus == MeasurementUsable {
		*summary.ReusedCellCount++
	}
	if cell.Assessment.DisplayStatus == MeasurementProblem {
		*summary.FailedCellCount++
	}
}

func AddCoverageActions(
	out *MeasurementCoverageResponse, cell MeasurementCoverageCell, actions map[string]int,
) {
	if cell.Assessment.DisplayStatus == MeasurementProblem &&
		len(cell.FailureSummary) == 0 && actions["failure"] < 3 {
		message := "문제 조건의 원인을 확인하세요"
		code := "MeasurementProblem"
		if len(cell.Assessment.Reasons) > 0 {
			message = cell.Assessment.Reasons[0].Message
			code = cell.Assessment.Reasons[0].Code
		}
		out.NextActions = append(out.NextActions, MeasurementNextAction{
			Kind: "failure", CellID: cell.ID, ReasonCode: code, Message: message,
		})
		actions["failure"]++
	}
	for _, failure := range cell.FailureSummary {
		if cell.Assessment.DisplayStatus == MeasurementUsable || actions["failure"] >= 3 {
			continue
		}
		out.NextActions = append(out.NextActions, MeasurementNextAction{
			Kind: "failure", CellID: cell.ID, EntityID: failure.RequestID, ReasonCode: failure.Code, Message: failure.Message,
		})
		actions["failure"]++
	}
	for _, variant := range cell.Variants {
		if len(variant.BatchProgress.Missing) == 0 || actions["missing-batches"] >= 3 {
			continue
		}
		out.NextActions = append(out.NextActions, MeasurementNextAction{
			Kind: "missing-batches", CellID: cell.ID, VariantID: ptr.To(variant.ID),
			ReasonCode: "TargetBatchMissing", Message: "목표 batch의 부족 범위를 확인하세요",
		})
		actions["missing-batches"]++
	}
	if (cell.Assessment.Status == MeasurementUnknown ||
		cell.Assessment.Status == MeasurementReview) &&
		cell.Support.Verdict != MeasurementSupportIncompatible && actions["evidence"] < 3 {
		message := "지원 입력과 동결 근거를 확인하세요"
		if cell.Assessment.Status == MeasurementReview {
			message = "입력 계약·품질의 확인된 불일치를 상세에서 확인하세요"
		}
		out.NextActions = append(out.NextActions, MeasurementNextAction{
			Kind: "evidence", CellID: cell.ID, ReasonCode: "EvidenceUnconfirmed", Message: message,
		})
		actions["evidence"]++
	}
}

func CountCoverageFailedRequests(
	summary *MeasurementCoverageSummary, requests []MeasurementEntity,
	cells map[string]MeasurementCoverageCell,
) {
	for _, request := range requests {
		if !request.Failed {
			continue
		}
		current := false
		for _, id := range request.CellIDs {
			selected := cells[id].CurrentMeasurement
			current = current || selected != nil && request.ID == selected.ID
		}
		if current {
			*summary.CurrentFailedRequestCount++
		} else {
			*summary.HistoricalFailedRequestCount++
		}
	}
}

func summarizeCoverageGroup(
	group *MeasurementCoverageGroup, cells map[string]MeasurementCoverageCell,
) {
	for _, id := range group.CellIDs {
		cell := cells[id]
		if cell.Assessment.DisplayStatus == MeasurementUsable {
			group.Complete++
		}
		switch cell.Support.Verdict {
		case MeasurementSupportCompatible:
			*group.Supported++
		case MeasurementSupportUnknown:
			group.UnknownSupport++
		}
	}
}
