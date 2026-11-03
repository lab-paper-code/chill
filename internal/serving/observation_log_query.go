package serving

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

type ObservationCohort struct {
	Definition              string         `json:"definition"`
	Total                   int            `json:"total"`
	Outcomes                map[string]int `json:"outcomes"`
	SampleCount             int            `json:"sampleCount"`
	LatencyNanos            []float64      `json:"latencyNanos"`
	CompletionResidualNanos []float64      `json:"completionResidualNanos"`
	Percentile              *float64       `json:"percentile,omitempty"`
	QuantileNanos           *float64       `json:"quantileNanos"`
	TargetLatencyNanos      *int64         `json:"targetLatencyNanos,omitempty"`
}

type ObservationResult struct {
	UID              string                  `json:"uid"`
	From             time.Time               `json:"from"`
	To               time.Time               `json:"to"`
	DefinitionFilter []string                `json:"definitionFilter"`
	OutcomeFilter    []string                `json:"outcomeFilter"`
	Total            int                     `json:"total"`
	Selected         int                     `json:"selected"`
	Outcomes         map[string]int          `json:"outcomes"`
	Cohorts          []ObservationCohort     `json:"cohorts"`
	Definitions      []DefinitionObservation `json:"definitions"`
	Requests         []RequestRow            `json:"requests"`
	Management       []ManagementEvent       `json:"management"`
	NextCursor       string                  `json:"nextCursor,omitempty"`
	QuantileMethod   string                  `json:"quantileMethod"`
	SamplePopulation string                  `json:"samplePopulation"`
	GapUntil         *time.Time              `json:"gapUntil,omitempty"`
	Coverage         ObservationCoverage     `json:"coverage"`
}

func (l *ObservationLog) Query(ctx context.Context, uid string, from, to time.Time, definitions, outcomes []string, limit int, cursor string) (ObservationResult, error) {
	if len(definitions) == 0 {
		definitions = l.config.Definitions
	}
	if len(outcomes) == 0 {
		outcomes = l.config.Outcomes
	}
	result := ObservationResult{
		UID: uid, From: from, To: to, DefinitionFilter: definitions, OutcomeFilter: outcomes,
		Outcomes: map[string]int{}, QuantileMethod: "gonum/stat.Empirical",
		SamplePopulation: "requests received in [from,to); final rows excluding canceled, incomplete and delivery_failed; " +
			"positive monotonic elapsed latency",
	}
	if uid == "" || (!from.IsZero() && !to.IsZero() && !from.Before(to)) || limit < 0 {
		return result, failure("InvalidQuery", "invalid observation query")
	}
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return result, failure("InvalidQuery", "invalid observation cursor")
		}
		offset = n
	}
	// Rows follow receipt, and a final row can come at any later time.
	read, err := l.prepare(ctx, map[string][2]time.Time{
		observationRequestStream: {from, {}}, observationManagementStream: {from, to},
	})
	if err != nil {
		return result, err
	}
	defer read.close()
	result.Coverage = read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}, observationManagementStream: {from, to}})
	result.GapUntil = result.Coverage.gapUntil(from)
	rows, err := l.requestRows(read, func(row *requestEnvelope) bool {
		return row.WorkloadUID == uid && observationInInterval(row.ReceivedAt, from, to)
	})
	if err != nil {
		return result, err
	}
	byID := map[string]*Definition{}
	for id, d := range read.definitions {
		if d.UID == uid && observationIncludes(result.DefinitionFilter, id) {
			byID[id] = d
		}
	}
	selected, err := result.aggregate(ctx, byID, rows)
	if err != nil {
		return result, err
	}
	sort.SliceStable(selected, func(a, b int) bool {
		if selected[a].ReceivedAt.Equal(selected[b].ReceivedAt) {
			return selected[a].RequestID < selected[b].RequestID
		}
		return selected[a].ReceivedAt.Before(selected[b].ReceivedAt)
	})
	if offset > len(selected) {
		return result, failure("InvalidQuery", "observation cursor is out of range")
	}
	end := len(selected)
	if limit > 0 && limit < end-offset {
		end = offset + limit
		result.NextCursor = strconv.Itoa(end)
	}
	result.Requests = selected[offset:end]
	return result, l.queryManagement(read, &result)
}

func (l *ObservationLog) queryManagement(read *observationRead, result *ObservationResult) error {
	err := l.scanStream(read, observationManagementStream, func(raw []byte) error {
		var e ManagementEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		filter := result.DefinitionFilter
		if e.WorkloadUID != result.UID || !observationInInterval(e.At, result.From, result.To) || len(filter) != 0 &&
			(e.Before == nil || !observationIncludes(filter, e.Before.Identity())) &&
			(e.Target == nil || !observationIncludes(filter, e.Target.Identity())) &&
			(e.After == nil || !observationIncludes(filter, e.After.Identity())) {
			return nil
		}
		result.Management = append(result.Management, e)
		return nil
	})
	sort.SliceStable(result.Management, func(a, b int) bool {
		return result.Management[a].At.Before(result.Management[b].At)
	})
	return err
}

type requestEnvelope struct {
	Kind        string    `json:"kind"`
	RequestID   string    `json:"requestID"`
	WorkloadUID string    `json:"workloadUID"`
	ReceivedAt  time.Time `json:"receivedAt"`
}

// requestRows merges the captured rows by request: final, else the open row,
// else assigned.
func (l *ObservationLog) requestRows(read *observationRead, keep func(*requestEnvelope) bool) ([]*RequestRow, error) {
	// Request rows reference definitions only. Retained immutable definitions
	// normally resolve them without opening hourly catalogs full of raw profile
	// populations. A cold archive read loads only a selected row's catalog.
	read.definitionsOnly = true
	rows := map[string]*RequestRow{}
	err := l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var envelope requestEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if envelope.Kind == RequestRowNative || !keep(&envelope) {
			return nil
		}
		if err := mergeRequestRow(rows, raw, keep, nil); err != nil {
			return err
		}
		row := rows[envelope.RequestID]
		if row == nil || row.DefinitionID == "" || read.definitions[row.DefinitionID] != nil {
			return nil
		}
		if err := l.readCatalog(read, read.scanningHour); err != nil {
			return err
		}
		if read.definitions[row.DefinitionID] == nil {
			return failure("IncompleteObservation", "catalog has no definition %s", row.DefinitionID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id, row := range read.open {
		if err := read.ctx.Err(); err != nil {
			return nil, err
		}
		if (rows[id] == nil || rows[id].Kind != requestRowFinal) &&
			keep(&requestEnvelope{RequestID: id, WorkloadUID: row.WorkloadUID, ReceivedAt: row.ReceivedAt}) {
			rows[id] = row
		}
	}
	result := make([]*RequestRow, 0, len(rows))
	for _, row := range rows {
		if err := read.ctx.Err(); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, nil
}
