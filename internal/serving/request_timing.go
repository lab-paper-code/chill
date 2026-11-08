package serving

import (
	"encoding/json"
	"strconv"
	"time"
)

// ServerStage identifies a fixed server boundary measured from request receipt.
type ServerStage uint8

const (
	ServerReceiveLockAttempt ServerStage = iota
	ServerReceiveLockAcquired
	ServerReceiveDone
	ServerAuthStart
	ServerAuthDone
	ServerBodyDecodeStart
	ServerBodyDecodeDone
	ServerAcceptCheckLockAttempt
	ServerAcceptCheckLockAcquired
	ServerAcceptCheckDone
	ServerInputValidateStart
	ServerInputValidateDone
	ServerValidatedEventDone
	ServerAcceptLockAttempt
	ServerAcceptLockAcquired
	ServerAcceptDone
	ServerExecuteStart
	ServerAssignedEventDone
	ServerWorkerHTTPStart
	ServerWorkerHTTPDone
	ServerResultLockAttempt
	ServerResultLockAcquired
	ServerReplySent
	ServerReplyReceived
	ServerResponseWriteStart
	ServerResponseWriteDone
	ServerResponseFlushStart
	ServerResponseFlushDone
	ServerSenderFreeze
	serverStageCount
)

var serverStageNames = [serverStageCount]string{
	"receiveLockAttempt",
	"receiveLockAcquired",
	"receiveDone",
	"authStart",
	"authDone",
	"bodyDecodeStart",
	"bodyDecodeDone",
	"acceptCheckLockAttempt",
	"acceptCheckLockAcquired",
	"acceptCheckDone",
	"inputValidateStart",
	"inputValidateDone",
	"validatedEventDone",
	"acceptLockAttempt",
	"acceptLockAcquired",
	"acceptDone",
	"executeStart",
	"assignedEventDone",
	"workerHTTPStart",
	"workerHTTPDone",
	"resultLockAttempt",
	"resultLockAcquired",
	"replySent",
	"replyReceived",
	"responseWriteStart",
	"responseWriteDone",
	"responseFlushStart",
	"responseFlushDone",
	"senderFreeze",
}

// RequestTimings owns its offsets. Copies never share mutable backing storage.
// Presence is separate because zero nanoseconds is a valid observation.
// Each owner records locally; merging uses the caller's existing synchronization.
type RequestTimings struct {
	offsets [serverStageCount]int64
	present uint32
}

// Record keeps the first observation of a reached stage.
func (t *RequestTimings) Record(stage ServerStage, elapsed time.Duration) {
	if stage >= serverStageCount || t.present&(uint32(1)<<stage) != 0 {
		return
	}
	t.offsets[stage] = int64(elapsed)
	t.present |= uint32(1) << stage
}

// Mark uses the original receipt time, including its monotonic clock reading.
func (t *RequestTimings) Mark(stage ServerStage, origin time.Time) {
	if origin.IsZero() {
		return
	}
	t.Record(stage, time.Since(origin))
}

// Merge fills only stages not already observed by this owner.
func (t *RequestTimings) Merge(other RequestTimings) {
	missing := other.present &^ t.present
	if missing == 0 {
		return
	}
	for stage := ServerStage(0); stage < serverStageCount; stage++ {
		if missing&(uint32(1)<<stage) != 0 {
			t.offsets[stage] = other.offsets[stage]
		}
	}
	t.present |= missing
}

func (t RequestTimings) Elapsed(stage ServerStage) (time.Duration, bool) {
	if stage >= serverStageCount || t.present&(uint32(1)<<stage) == 0 {
		return 0, false
	}
	return time.Duration(t.offsets[stage]), true
}

func (t RequestTimings) IsZero() bool { return t.present == 0 }

// MarshalJSON orders stages for stable serialization, not causal ordering.
func (t RequestTimings) MarshalJSON() ([]byte, error) {
	capacity := 2
	for stage, name := range serverStageNames {
		if t.present&(uint32(1)<<stage) != 0 {
			// Object punctuation, field names, comma, and at most 20 integer bytes.
			capacity += len(name) + 49
		}
	}
	body := make([]byte, 0, capacity)
	body = append(body, '[')
	for stage, name := range serverStageNames {
		if t.present&(uint32(1)<<stage) == 0 {
			continue
		}
		if len(body) > 1 {
			body = append(body, ',')
		}
		body = append(body, `{"stage":"`...)
		body = append(body, name...)
		body = append(body, `","elapsedNanos":`...)
		body = strconv.AppendInt(body, t.offsets[stage], 10)
		body = append(body, '}')
	}
	return append(body, ']'), nil
}

func (t *RequestTimings) UnmarshalJSON(body []byte) error {
	var entries []struct {
		Stage        string `json:"stage"`
		ElapsedNanos int64  `json:"elapsedNanos"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return err
	}
	var decoded RequestTimings
	for _, entry := range entries {
		for stage, name := range serverStageNames {
			if entry.Stage == name {
				decoded.Record(ServerStage(stage), time.Duration(entry.ElapsedNanos))
				break
			}
		}
	}
	*t = decoded
	return nil
}
