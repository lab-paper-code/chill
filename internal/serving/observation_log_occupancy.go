package serving

import (
	"context"
	"encoding/json"
	"time"
)

const requestRowOpen = "occupancy-open"

// ScanOccupancyRows streams assigned/final rows and sparse native supplements.
// Empty workload and node filters select every cell. The caller joins RequestID
// before counting members and uses worker-native starts for its half-open window;
// ReceivedAt is not a worker timestamp. The first receipt hour is included whole
// so already-queued boundary requests are retained. Later files retain supplements.
func (l *ObservationLog) ScanOccupancyRows(ctx context.Context, uid, node string, from, to time.Time, visit func(RequestRow)) (*time.Time, error) {
	if !from.Before(to) {
		return nil, failure("InvalidQuery", "invalid occupancy observation interval")
	}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationRequestStream: {from.Truncate(time.Hour), {}}})
	if err != nil {
		return nil, err
	}
	defer read.close()
	keep := func(r *RequestRow) bool {
		return (uid == "" || r.WorkloadUID == uid) && (node == "" || r.Node == nil || r.Node.UID == node)
	}
	err = l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var r RequestRow
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if !keep(&r) {
			return nil
		}
		visit(r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, r := range read.open {
		if keep(r) {
			copied := *r
			copied.Kind = requestRowOpen
			visit(copied)
		}
	}
	coverage := read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}})
	return coverage.gapUntil(from), err
}
