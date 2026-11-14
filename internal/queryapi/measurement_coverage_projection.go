package queryapi

import (
	"slices"
	"sort"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func CoverageCellID(model, class, path, mode string) string {
	return strings.Join([]string{model, class, path, mode}, "/")
}
func CoverageRequestID(task api.BatchEnergyOptimization) string {
	return task.Namespace + "/" + task.Name + "/" + string(task.UID)
}
func CoverageBatches(values []int) []int {
	out := append([]int{}, values...)
	sort.Ints(out)
	return slices.Compact(out)
}
func CoverageContains(values []int, batch int) bool { return slices.Contains(values, batch) }
func CoverageNewCell(
	group MeasurementCoverageGroup, path, mode string,
	support map[string]ModelCompatibilityView,
) MeasurementCoverageCell {
	cell := MeasurementCoverageCell{
		ID:    CoverageCellID(group.Model.Name, group.DeviceClass.Name, path, mode),
		Model: group.Model, DeviceClass: group.DeviceClass, ExecutionPath: path, PowerMode: mode,
		Variants: []MeasurementCoverageVariant{}, FailureSummary: []MeasurementFailure{},
		History: []MeasurementHistory{},
		Support: MeasurementSupport{
			Verdict: MeasurementSupportUnknown, Reasons: []ModelCompatibilityReason{},
			Diagnostics: []string{},
		},
	}
	row, found := support[strings.Join([]string{group.Model.Name, group.DeviceClass.Name, path}, "/")]
	if !found {
		cell.Support.Reasons = append(cell.Support.Reasons, ModelCompatibilityReason{
			Kind: MeasurementSupportUnknown, Code: "SupportInputMissing", Message: "선택 Node의 지원 입력을 확인할 수 없습니다",
		})
		return cell
	}
	cell.Support.Verdict, cell.Support.Reasons = row.Verdict, row.Reasons
	cell.Support.Diagnostics = row.Diagnostics
	if row.Inputs.Node.Name != "" {
		cell.Support.Node = &MeasurementIdentity{Name: row.Inputs.Node.Name, UID: row.Inputs.Node.UID}
	}
	return cell
}
func CoverageMeasurementTime(task api.BatchEnergyOptimization, measuredAt time.Time) time.Time {
	if task.Status.Phase != string(api.MeasurementPhaseComplete) {
		measuredAt = time.Time{}
	}
	if (task.Status.Phase == string(api.MeasurementPhaseComplete) ||
		task.Status.Phase == string(api.MeasurementPhaseFailed)) &&
		task.Status.ObservedGeneration == task.Generation {
		for _, condition := range task.Status.Conditions {
			if condition.Type == "MeasurementComplete" && condition.ObservedGeneration == task.Generation {
				return profilederivation.MeasurementTime(measuredAt, condition.LastTransitionTime.Time)
			}
		}
	}
	return profilederivation.MeasurementTime(measuredAt, time.Time{})
}

// Keep repeat measurements separate until current selection. The public variant
// ID remains the frozen-condition fingerprint used by request preflight.
func CoverageFindVariant(
	cell *MeasurementCoverageCell, measurementID string, condition any, basis string,
) *MeasurementCoverageVariant {
	for i := range cell.Variants {
		if slices.Contains(cell.Variants[i].RequestIDs, measurementID) {
			return &cell.Variants[i]
		}
	}
	cell.Variants = append(cell.Variants, MeasurementCoverageVariant{
		ID: MeasurementVariantID(condition), Condition: condition,
		ResultRows: []ResultRow{}, RequestIDs: []string{},
		RunIDs: []string{}, ProfileIDs: []string{},
		BatchProgress: MeasurementBatchProgress{
			Basis: basis, Target: []int{}, Measured: []int{}, CriteriaMet: []int{}, Missing: []int{},
			InProgress: []int{}, Unknown: []int{},
		},
	})
	return &cell.Variants[len(cell.Variants)-1]
}
