package observation

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// Duplicate is server-owned admission metadata; every report body remains raw.
type clientObservationLine struct {
	serving.ClientObservation
	Duplicate bool `json:"duplicate,omitempty"`
	// Duplicate reports can introduce a new alias for an existing identity.
	// Preserve those introductions so restart does not refresh older IDs.
	NewObservationID bool `json:"newObservationID,omitempty"`
	NewRequestID     bool `json:"newRequestID,omitempty"`
}

type clientObservationIdentity struct {
	workload string
	id       string
	request  bool
}

type clientObservationIdentityReceipt struct {
	identity clientObservationIdentity
	at       time.Time
}

type clientObservationEnvelope struct {
	ObservationID string    `json:"observationID"`
	RequestID     string    `json:"requestID"`
	WorkloadName  string    `json:"workloadName"`
	ReportedAt    time.Time `json:"reportedAt"`
	Duplicate     bool      `json:"duplicate"`
	NewObservationID bool   `json:"newObservationID"`
	NewRequestID     bool   `json:"newRequestID"`
}

// ClientObservation admits immutable evidence, including later duplicate reports.
func (l *ObservationLog) ClientObservation(ctx context.Context, observation serving.ClientObservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	if err := l.submissionErrorLocked(); err != nil {
		return err
	}
	at := l.receiptLocked()
	observation.ReportedAt = at
	l.pruneClientIdentitiesLocked(at.Add(-l.config.Retention))
	observationID := clientObservationIdentity{workload: observation.WorkloadName, id: observation.ObservationID}
	requestID := clientObservationIdentity{workload: observation.WorkloadName, id: observation.RequestID, request: true}
	_, observed := l.clientSeen[observationID]
	_, requested := l.clientSeen[requestID]
	line := clientObservationLine{ClientObservation: observation, Duplicate: observed || observation.RequestID != "" && requested}
	line.NewObservationID = line.Duplicate && !observed
	line.NewRequestID = line.Duplicate && observation.RequestID != "" && !requested
	batch, err := l.freezeBatchLocked(observationClientStream, at, line, nil, at)
	if err == nil {
		l.rememberClientIdentityLocked(observationID, at)
		if observation.RequestID != "" {
			l.rememberClientIdentityLocked(requestID, at)
		}
		l.enqueueLocked(batch)
	}
	return err
}

func (l *ObservationLog) rememberClientIdentityLocked(identity clientObservationIdentity, at time.Time) {
	if l.clientSeen == nil {
		l.clientSeen = map[clientObservationIdentity]struct{}{}
	}
	if _, exists := l.clientSeen[identity]; !exists {
		l.clientSeen[identity] = struct{}{}
		l.clientExpiry = append(l.clientExpiry, clientObservationIdentityReceipt{identity: identity, at: at})
	}
}

// Each identity enters and leaves once. Zero expired slots immediately and
// compact at half consumption, keeping both work and retained storage bounded.
func (l *ObservationLog) pruneClientIdentitiesLocked(from time.Time) {
	for l.clientExpiryHead < len(l.clientExpiry) && l.clientExpiry[l.clientExpiryHead].at.Before(from) {
		expired := l.clientExpiry[l.clientExpiryHead]
		delete(l.clientSeen, expired.identity)
		l.clientExpiry[l.clientExpiryHead] = clientObservationIdentityReceipt{}
		l.clientExpiryHead++
	}
	if l.clientExpiryHead == len(l.clientExpiry) {
		l.clientSeen, l.clientExpiry, l.clientExpiryHead = nil, nil, 0
	} else if l.clientExpiryHead > 0 && l.clientExpiryHead >= len(l.clientExpiry)/2 {
		remaining := len(l.clientExpiry) - l.clientExpiryHead
		if cap(l.clientExpiry) > 4*remaining {
			compacted := make([]clientObservationIdentityReceipt, remaining)
			copy(compacted, l.clientExpiry[l.clientExpiryHead:])
			l.clientExpiry = compacted
		} else {
			copy(l.clientExpiry, l.clientExpiry[l.clientExpiryHead:])
			clear(l.clientExpiry[remaining:])
			l.clientExpiry = l.clientExpiry[:remaining]
		}
		l.clientExpiryHead = 0
	}
}

// Startup reuses the local stream lifecycle, without an archive recovery service.
func (l *ObservationLog) restoreClientIdentities(hours []time.Time) error {
	from := l.now().Add(-l.config.Retention)
	for _, hour := range hours {
		if !hour.Add(time.Hour).After(from) {
			continue
		}
		files, err := l.hourFiles(hour, observationClientStream)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !file.at.Add(time.Minute).After(from) {
				continue
			}
			if err := scanObservationFile(file.path, func(raw []byte) error {
				var envelope clientObservationEnvelope
				if err := json.Unmarshal(raw, &envelope); err != nil {
					return err
				}
				if envelope.ReportedAt.Before(from) {
					return nil
				}
				if !envelope.Duplicate || envelope.NewObservationID {
					l.rememberClientIdentityLocked(clientObservationIdentity{workload: envelope.WorkloadName, id: envelope.ObservationID}, envelope.ReportedAt)
				}
				if envelope.RequestID != "" && (!envelope.Duplicate || envelope.NewRequestID) {
					l.rememberClientIdentityLocked(clientObservationIdentity{workload: envelope.WorkloadName, id: envelope.RequestID, request: true}, envelope.ReportedAt)
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
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
func (l *ObservationLog) clientWindow(read *observationRead, rows []*serving.RequestRow, nativeRows map[string]serving.RequestRow, from, to time.Time) ([]serving.PathTimingSample, []serving.ClientObservation, error) {
	retainedFrom := read.now.Add(-l.config.Retention)
	requests := make(map[string]*serving.RequestRow, len(rows))
	for _, row := range rows {
		if !row.ReceivedAt.Before(retainedFrom) && row.ReceivedAt.Before(to) {
			if native, ok := nativeRows[row.RequestID]; ok {
				merged := *row
				merged.ServerTiming.Merge(native.ServerTiming)
				if native.NativeFinishedAt != nil {
					merged.NativeFinishedAt = native.NativeFinishedAt
					merged.NativeExecutionComplete = native.NativeExecutionComplete
					merged.NativeOutcome, merged.NativeError = native.NativeOutcome, native.NativeError
					merged.NativeElapsedMS = native.NativeElapsedMS
				}
				requests[row.RequestID] = &merged
				continue
			}
			requests[row.RequestID] = row
		}
	}
	from = serving.LaterTime(from, retainedFrom)
	samples := []serving.PathTimingSample{}
	clients := []serving.ClientObservation{}
	err := l.scanStream(read, observationClientStream, func(raw []byte) error {
		var envelope clientObservationEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if envelope.Duplicate || !serving.ObservationInInterval(envelope.ReportedAt, from, to) {
			return nil
		}
		var client serving.ClientObservation
		if err := json.Unmarshal(raw, &client); err != nil {
			return err
		}
		row := requests[client.RequestID]
		name := ""
		if row != nil {
			name = requestWorkloadName(read, row)
		}
		joined := row != nil && name != "" && name == client.WorkloadName &&
			(client.WorkloadUID == "" || client.WorkloadUID == row.WorkloadUID)
		if joined && !serving.ObservationInInterval(row.ReceivedAt, from, to) {
			return nil
		}
		if !joined {
			clients = append(clients, client)
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

