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
			definition := read.definitions[row.DefinitionID]
			if definition != nil && definition.Name != observation.WorkloadName ||
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
