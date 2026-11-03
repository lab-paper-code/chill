package serving

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"gonum.org/v1/gonum/stat"
)

func observationIncludes(filter []string, value string) bool {
	return len(filter) == 0 || slices.Contains(filter, value)
}

func observationInInterval(at, from, to time.Time) bool {
	return !at.IsZero() && (from.IsZero() || !at.Before(from)) && (to.IsZero() || at.Before(to))
}

func requestOutcome(row *RequestRow) string {
	switch {
	case row.Outcome != "":
		return row.Outcome
	case row.ElapsedNanos != nil:
		return outcomeCompleted
	case row.accepted():
		return outcomeIncomplete
	default:
		return phasePending
	}
}

// Aggregate the whole selected population before projecting any row page.
func (result *ObservationResult) aggregate(ctx context.Context, definitions map[string]*Definition, rows []*RequestRow) ([]RequestRow, error) {
	cohorts := make(map[string]*ObservationCohort)
	for id, d := range definitions {
		p := d.SLO.Percentile
		target := int64(d.SLO.TargetLatency)
		cohorts[id] = &ObservationCohort{
			Definition: id, Outcomes: make(map[string]int), Percentile: &p, TargetLatencyNanos: &target,
		}
		result.Definitions = append(result.Definitions, DefinitionObservation{Identity: id, Definition: d})
	}
	selected := []RequestRow{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !observationInInterval(row.ReceivedAt, result.From, result.To) ||
			!observationIncludes(result.DefinitionFilter, row.DefinitionID) {
			continue
		}
		result.Total++
		outcome := requestOutcome(row)
		result.Outcomes[outcome]++
		if !observationIncludes(result.OutcomeFilter, outcome) {
			continue
		}
		result.Selected++
		selected = append(selected, *row)
		cohort := cohorts[row.DefinitionID]
		if cohort == nil {
			cohort = &ObservationCohort{Definition: row.DefinitionID, Outcomes: make(map[string]int)}
			cohorts[row.DefinitionID] = cohort
		}
		cohort.Total++
		cohort.Outcomes[outcome]++
		switch outcome {
		case "delivery_failed", outcomeIncomplete, "canceled":
			continue
		}
		if row.ElapsedNanos != nil && *row.ElapsedNanos > 0 {
			elapsed := *row.ElapsedNanos
			cohort.LatencyNanos = append(cohort.LatencyNanos, float64(elapsed))
			if native := row.NativeElapsedMS; native != nil && row.DispatchedNanos != nil && row.ExecutionComplete &&
				schedulerFinite(*native) && *native >= 0 {
				cohort.CompletionResidualNanos = append(cohort.CompletionResidualNanos,
					float64(elapsed-*row.DispatchedNanos)-*native*float64(time.Millisecond))
			}
		}
	}
	for _, c := range cohorts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sort.Float64s(c.LatencyNanos)
		sort.Float64s(c.CompletionResidualNanos)
		c.SampleCount = len(c.LatencyNanos)
		if c.SampleCount > 0 && c.Percentile != nil && *c.Percentile > 0 && *c.Percentile <= 100 {
			q := stat.Quantile(*c.Percentile/100, stat.Empirical, c.LatencyNanos, nil)
			c.QuantileNanos = &q
		}
		result.Cohorts = append(result.Cohorts, *c)
	}
	sort.Slice(result.Cohorts, func(a, b int) bool { return result.Cohorts[a].Definition < result.Cohorts[b].Definition })
	sort.Slice(result.Definitions, func(a, b int) bool { return result.Definitions[a].Identity < result.Definitions[b].Identity })
	return selected, ctx.Err()
}

// mergeRequestRow decodes raw once with the standard decoder. A row the
// selector skips needs only valid syntax, so a decode error counts only when
// the envelope keeps the row.
func mergeRequestRow(rows map[string]*RequestRow, raw []byte, keep func(*requestEnvelope) bool, catalog *ObservationCatalog) error {
	row := &RequestRow{}
	if decodeErr := json.Unmarshal(raw, row); decodeErr != nil {
		var envelope requestEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if !keep(&envelope) || envelope.Kind != requestRowFinal && rows[envelope.RequestID] != nil {
			return nil
		}
		return decodeErr
	}
	if row.Kind == RequestRowNative {
		return nil // Supplemental physical evidence is not an HTTP request row.
	}
	envelope := requestEnvelope{Kind: row.Kind, RequestID: row.RequestID, WorkloadUID: row.WorkloadUID, ReceivedAt: row.ReceivedAt}
	if keep(&envelope) && (row.Kind == requestRowFinal || rows[row.RequestID] == nil) {
		if catalog != nil && row.DefinitionID != "" && len(catalog.rows["definition/"+row.DefinitionID]) == 0 {
			return failure("IncompleteObservation", "catalog has no definition %s", row.DefinitionID)
		}
		rows[row.RequestID] = row
	}
	return nil
}
