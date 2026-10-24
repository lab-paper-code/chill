package serving

import (
	"encoding/json"
	"io"
	"time"
)

// ScanOccupancyRows visits final physical evidence without Query's per-request
// map, cohort samples or response slice. Only unresolved assignments and the
// caller's physical-batch summary remain resident. Raw rows stay in the log.
// Supplemental late native evidence follows the final rows; it is not another
// HTTP completion or batch member.
func (l *ObservationLog) ScanOccupancyRows(uid, node string, from, to time.Time, visit func(RequestRow)) (*time.Time, error) {
	if uid == "" || node == "" || !from.Before(to) {
		return nil, failure("InvalidQuery", "invalid occupancy observation interval")
	}
	read, err := l.capture(map[string][2]time.Time{observationRequestStream: {from, {}}})
	if err != nil {
		return nil, err
	}
	defer read.close()
	// Missing final rows retain only assignment identity, never raw rows or
	// latency/cohort arrays. Healthy final append order keeps this near K.
	type assignment struct {
		instance string
		received time.Time
		node     *NodeReference
	}
	pending := map[string]assignment{}
	instances := map[string]bool{}
	keep := func(r *RequestRow) bool {
		return r.WorkloadUID == uid && observationInInterval(r.ReceivedAt, from, to) && (r.Node == nil || r.Node.UID == node)
	}
	err = l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var envelope requestEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if envelope.Kind == RequestRowNative || envelope.WorkloadUID != uid || !observationInInterval(envelope.ReceivedAt, from, to) {
			return nil
		}
		var r RequestRow
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if !keep(&r) {
			return nil
		}
		instances[r.Instance] = true
		if r.Kind == requestRowFinal {
			delete(pending, r.RequestID)
			visit(r)
		} else {
			pending[r.RequestID] = assignment{r.Instance, r.ReceivedAt, r.Node}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id, r := range read.open {
		if keep(r) {
			copied := *r
			copied.Kind = requestRowAssigned
			delete(pending, id)
			visit(copied)
			instances[r.Instance] = true
		}
	}
	for id, r := range pending {
		visit(RequestRow{Kind: requestRowAssigned, RequestID: id, WorkloadUID: uid, Instance: r.instance, ReceivedAt: r.received, Node: r.node})
	}
	// Native events may arrive after their HTTP row, including across minutes.
	for _, file := range read.files[observationRequestStream] {
		if _, err := file.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	err = l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var envelope requestEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if envelope.Kind != RequestRowNative || envelope.WorkloadUID != uid {
			return nil
		}
		var r RequestRow
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if instances[r.Instance] {
			visit(r)
		}
		return nil
	})
	return read.gap(from), err
}
