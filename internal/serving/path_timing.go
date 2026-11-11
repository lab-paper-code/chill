package serving

import (
	"math"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

const (
	clientOutcomeHTTPError = "http_error"
	preprocessFailedCode   = "PreprocessFailed"
)

// ClientObservation is immutable client-reported evidence.
// Empty RequestID/WorkloadUID means unknown, never an inferred association.
type ClientObservation struct {
	ObservationID string    `json:"observationID"`
	RequestID     string    `json:"requestID,omitempty"`
	WorkloadName  string    `json:"workloadName"`
	WorkloadUID   string    `json:"workloadUID,omitempty"`
	ElapsedNanos  int64     `json:"elapsedNanos"`
	Outcome       string    `json:"outcome"`
	StatusCode    *int      `json:"statusCode,omitempty"`
	ReportedAt    time.Time `json:"reportedAt"`
	ActorUsername string    `json:"actorUsername,omitempty"`
	ActorUID      string    `json:"actorUID,omitempty"`
}

// PathTimingSample contains only an attributable finite reserve sample.
// Full client/server/native evidence remains in their original records.
type PathTimingSample struct {
	WorkloadUID       string    `json:"workloadUID"`
	RequestID         string    `json:"requestID"`
	ReceivedAt        time.Time `json:"receivedAt"`
	TotalNanos        float64   `json:"totalNanos"`
	PostNanos         float64   `json:"postNanos"`
	ClientNanos       float64   `json:"clientNanos"`
	ServingQueueNanos float64   `json:"servingQueueNanos"`
	NativeNanos       float64   `json:"nativeNanos"`
}

// PathTimingSampleFromRequest decomposes a full client reply using local elapsed
// evidence. POST includes the external client roundtrip, not one-way return time.
// Missing evidence and client failures remain in their original records.
func PathTimingSampleFromRequest(row RequestRow, client ClientObservation) (PathTimingSample, bool) {
	if !pathTimingJoinedReply(row, client) {
		return PathTimingSample{}, false
	}
	if row.AcceptedNanos == nil || row.NativeElapsedMS == nil || !row.NativeExecutionComplete ||
		!row.ExecutionComplete || row.ExchangeNanos == nil || row.ElapsedNanos == nil ||
		*row.ExchangeNanos > *row.ElapsedNanos {
		return PathTimingSample{}, false
	}
	assigned, assignedOK := row.ServerTiming.Elapsed(ServerAssigned)
	sender, senderOK := row.ServerTiming.Elapsed(ServerSenderElapsedFrozen)
	accepted := time.Duration(*row.AcceptedNanos)
	if !assignedOK || !senderOK || accepted < 0 || assigned < accepted || sender < assigned ||
		*row.ExchangeNanos < int64(sender) {
		return PathTimingSample{}, false
	}
	wait := assigned - accepted
	native := *row.NativeElapsedMS
	if native < 0 || math.IsNaN(native) || math.IsInf(native, 0) {
		return PathTimingSample{}, false
	}
	elapsed, nativeNanos := float64(client.ElapsedNanos), native*float64(time.Millisecond)
	post := elapsed - float64(sender) - nativeNanos
	total := elapsed - float64(wait) - nativeNanos
	if post < 0 || total < post {
		return PathTimingSample{}, false
	}
	return PathTimingSample{
		WorkloadUID: row.WorkloadUID, RequestID: row.RequestID, ReceivedAt: row.ReceivedAt,
		TotalNanos: total, PostNanos: post,
		ClientNanos: elapsed, ServingQueueNanos: float64(wait), NativeNanos: nativeNanos,
	}, true
}

func pathTimingJoinedReply(row RequestRow, client ClientObservation) bool {
	if row.RequestID == "" || row.RequestID != client.RequestID || row.WorkloadUID == "" ||
		client.WorkloadUID != "" && row.WorkloadUID != client.WorkloadUID || row.ReceivedAt.IsZero() ||
		client.Outcome != outcomeCompleted && client.Outcome != clientOutcomeHTTPError || client.ElapsedNanos < 0 {
		return false
	}
	// These existing outcomes prove a synchronous terminal native reply. A late
	// native supplement cannot turn an interrupted exchange into that proof.
	switch {
	case row.Outcome == outcomeCompleted && row.Error == "":
	case row.Outcome == firstfloor.OutcomePreprocessFailed && row.Error == preprocessFailedCode:
	case row.Outcome == outcomeError && row.Error == "BackendError":
	case row.Outcome == schedulerOutcomeRejected && row.Error == CodeDeadlineInfeasible && row.NativeOutcome == firstfloor.OutcomeDeadlineDrop:
	case row.Outcome == schedulerOutcomeRejected && row.Error == CodeQueueFull && row.NativeOutcome == nativeOutcomeQueueFull:
	default:
		return false
	}
	return true
}
