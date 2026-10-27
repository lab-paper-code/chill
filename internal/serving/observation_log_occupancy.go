package serving

import (
	"context"
	"encoding/json"
	"time"
)

// ScanOccupancyRows streams final physical evidence and retains only unresolved
// assignment identities. Supplemental native rows are not HTTP completions.
func (l *ObservationLog) ScanOccupancyRows(ctx context.Context, uid, node string, from, to time.Time, visit func(RequestRow)) (*time.Time, error) {
	if uid == "" || node == "" || !from.Before(to) {
		return nil, failure("InvalidQuery", "invalid occupancy observation interval")
	}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationRequestStream: {from, {}}})
	if err != nil {
		return nil, err
	}
	defer read.close()
	type assignment struct {
		instance, routerID, planVersion, podUID, containerID string
		received                                             time.Time
		node                                                 *NodeReference
	}
	pending := map[string]assignment{}
	// Split snapshots can repeat a completed request in another source's rows
	// or a stale open report. Keep IDs only, never completed rows or latencies.
	var completed map[string]bool
	if l.config.RouterSources != nil {
		completed = map[string]bool{}
	}
	instances := map[string]bool{}
	keep := func(r *RequestRow) bool {
		return r.WorkloadUID == uid && observationInInterval(r.ReceivedAt, from, to) && (r.Node == nil || r.Node.UID == node)
	}
	err = l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var envelope requestEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if envelope.Kind == RequestRowNative || envelope.WorkloadUID != uid ||
			!observationInInterval(envelope.ReceivedAt, from, to) || completed[envelope.RequestID] {
			return nil
		}
		var row RequestRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		if !keep(&row) {
			return nil
		}
		instances[row.Instance] = true
		if row.Kind == requestRowFinal {
			delete(pending, row.RequestID)
			if completed != nil {
				completed[row.RequestID] = true
			}
			visit(row)
		} else {
			pending[row.RequestID] = assignment{
				instance: row.Instance, routerID: row.RouterID, planVersion: row.PlanVersion,
				podUID: row.PodUID, containerID: row.ContainerID, received: row.ReceivedAt, node: row.Node,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id, row := range read.open {
		if !completed[id] && keep(row) {
			copied := *row
			copied.Kind = requestRowAssigned
			delete(pending, id)
			visit(copied)
			instances[row.Instance] = true
		}
	}
	for id, row := range pending {
		visit(RequestRow{Kind: requestRowAssigned, RequestID: id, WorkloadUID: uid,
			Instance: row.instance, RouterID: row.routerID, PlanVersion: row.planVersion,
			PodUID: row.podUID, ContainerID: row.containerID, ReceivedAt: row.received, Node: row.node})
	}
	if err := read.rewindRequests(); err != nil {
		return nil, err
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
	coverage := read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}})
	return coverage.gapUntil(from), err
}
