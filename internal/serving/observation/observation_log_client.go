package observation

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// ClientObservation admits immutable evidence, including later duplicate reports.
// Identity lookup uses only the existing retention horizon, never archived history.
func (l *ObservationLog) ClientObservation(ctx context.Context, observation serving.ClientObservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if observation.RequestID != "" {
		// ponytail: scan retained request evidence; add an index only if measured
		// feedback ingestion cost requires it, keeping the same retention bound.
		from := l.now().Add(-l.config.Retention)
		read, err := l.capture(ctx, map[string][2]time.Time{observationRequestStream: {from, {}}})
		if err != nil {
			return err
		}
		defer read.close()
		rows, err := l.requestRows(read, func(row *requestEnvelope) bool {
			return row.RequestID == observation.RequestID && !row.ReceivedAt.Before(from)
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			name := requestWorkloadName(read, row)
			if name != "" && name != observation.WorkloadName ||
				observation.WorkloadUID != "" && row.WorkloadUID != "" && observation.WorkloadUID != row.WorkloadUID {
				return &serving.Error{Code: "InvalidClientObservation", Message: "request identity does not match the reported Workload"}
			}
		}
	}
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	if err := l.submissionErrorLocked(); err != nil {
		return err
	}
	at := l.receiptLocked()
	observation.ReportedAt = at
	batch, err := l.freezeBatchLocked(observationClientStream, at, observation, nil, at)
	if err == nil {
		l.enqueueLocked(batch)
	}
	return err
}

// ClientObservationPage exposes raw reports in FIFO append order, without dedup.
type ClientObservationPage struct {
	From         time.Time                   `json:"from"`
	To           time.Time                   `json:"to"`
	Observations []serving.ClientObservation `json:"observations"`
	NextCursor   string                      `json:"nextCursor,omitempty"`
	Coverage     serving.ObservationCoverage `json:"coverage"`
}

func (l *ObservationLog) ClientObservationPage(ctx context.Context, from, to time.Time, limit, offset int) (ClientObservationPage, error) {
	result := ClientObservationPage{From: from, To: to, Observations: []serving.ClientObservation{}}
	if from.IsZero() || !from.Before(to) || limit <= 0 || offset < 0 {
		return result, &serving.Error{Code: "InvalidQuery", Message: "positive client observation window, bounded limit and nonnegative cursor are required"}
	}
	ranges := map[string][2]time.Time{observationClientStream: {from, to}}
	read, err := l.prepare(ctx, ranges)
	if err != nil {
		return result, err
	}
	defer read.close()
	position := 0
	err = l.scanStream(read, observationClientStream, func(raw []byte) error {
		var observation serving.ClientObservation
		if err := json.Unmarshal(raw, &observation); err != nil {
			return err
		}
		if !serving.ObservationInInterval(observation.ReportedAt, from, to) {
			return nil
		}
		position++
		if position <= offset {
			return nil
		}
		if len(result.Observations) == limit {
			result.NextCursor = strconv.Itoa(offset + limit)
			return errObservationStop
		}
		result.Observations = append(result.Observations, observation)
		return nil
	})
	if err == nil && offset > position {
		err = &serving.Error{Code: "InvalidQuery", Message: "client observation cursor exceeds available reports"}
	}
	result.Coverage = read.coverage(ranges)
	return result, err
}

// clientWindow selects first reports before applying cohort filters. A later
// report can link both identities, but cannot replace the first observation.
func (l *ObservationLog) clientWindow(read *observationRead, rows []*serving.RequestRow, from, to time.Time) ([]serving.PathTimingSample, []serving.ClientObservation, error) {
	retainedFrom := read.now.Add(-l.config.Retention)
	requests := make(map[string]*serving.RequestRow, len(rows))
	for _, row := range rows {
		if !row.ReceivedAt.Before(retainedFrom) && row.ReceivedAt.Before(to) {
			requests[row.RequestID] = row
		}
	}
	if err := l.clientNativeSupplements(read, requests); err != nil {
		return nil, nil, err
	}
	type identity struct{ workload, id string }
	observations, requestIDs := map[identity]bool{}, map[identity]bool{}
	samples := []serving.PathTimingSample{}
	clients := []serving.ClientObservation{}
	err := l.scanStream(read, observationClientStream, func(raw []byte) error {
		var client serving.ClientObservation
		if err := json.Unmarshal(raw, &client); err != nil {
			return err
		}
		if !serving.ObservationInInterval(client.ReportedAt, retainedFrom, to) {
			return nil
		}
		observationID := identity{client.WorkloadName, client.ObservationID}
		requestID := identity{client.WorkloadName, client.RequestID}
		duplicate := observations[observationID] || client.RequestID != "" && requestIDs[requestID]
		observations[observationID] = true
		if client.RequestID != "" {
			requestIDs[requestID] = true
		}
		if duplicate {
			return nil
		}
		row := requests[client.RequestID]
		name := ""
		if row != nil {
			name = requestWorkloadName(read, row)
		}
		if row == nil || name == "" {
			// Unknown/pending reports stay in the report receipt cohort.
			if serving.ObservationInInterval(client.ReportedAt, from, to) {
				clients = append(clients, client)
			}
			return nil
		}
		if name != client.WorkloadName ||
			client.WorkloadUID != "" && client.WorkloadUID != row.WorkloadUID {
			// A request captured after ingestion can reveal a false assertion.
			// Keep its raw evidence, but never attribute it to that request.
			if serving.ObservationInInterval(client.ReportedAt, from, to) {
				clients = append(clients, client)
			}
			return nil
		}
		if !serving.ObservationInInterval(row.ReceivedAt, from, to) {
			return nil
		}
		client.WorkloadUID = row.WorkloadUID
		clients = append(clients, client)
		if sample, ok := serving.PathTimingSampleFromRequest(*row, client); ok {
			samples = append(samples, sample)
		}
		return nil
	})
	return samples, clients, err
}

func requestWorkloadName(read *observationRead, row *serving.RequestRow) string {
	if definition := read.definitions[row.DefinitionID]; definition != nil {
		return definition.Name
	}
	// Pre-validation receipts can have a UID without a definition snapshot.
	// Resolve only that exact server-owned UID, never the current path name.
	for _, definition := range read.definitions {
		if row.WorkloadUID != "" && definition.UID == row.WorkloadUID {
			return definition.Name
		}
	}
	return ""
}

// Native supplements add direct physical evidence without changing the HTTP
// outcome or claiming that a timed-out native result reached the client.
func (l *ObservationLog) clientNativeSupplements(read *observationRead, requests map[string]*serving.RequestRow) error {
	if len(requests) == 0 {
		return nil
	}
	return l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var envelope requestEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		row := requests[envelope.RequestID]
		if envelope.Kind != serving.RequestRowNative || row == nil ||
			envelope.WorkloadUID != "" && envelope.WorkloadUID != row.WorkloadUID {
			return nil
		}
		var native serving.RequestRow
		if err := json.Unmarshal(raw, &native); err != nil {
			return err
		}
		merged := *row
		merged.ServerTiming.Merge(native.ServerTiming)
		if native.NativeFinishedAt != nil {
			merged.NativeFinishedAt = native.NativeFinishedAt
			merged.NativeExecutionComplete = native.NativeExecutionComplete
			merged.NativeOutcome, merged.NativeError = native.NativeOutcome, native.NativeError
			merged.NativeElapsedMS = native.NativeElapsedMS
		}
		requests[envelope.RequestID] = &merged
		return nil
	})
}
