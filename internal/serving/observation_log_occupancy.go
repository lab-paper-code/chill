package serving

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

const (
	requestRowOpen              = "occupancy-open"
	requestRowOccupancySelected = "occupancy-selected"
)

// ScanOccupancyRows reads only the requested interval and its configured native
// delivery tail. A first streaming pass selects receipt/native-window RequestIDs;
// a second visits their raw rows, including earlier assignments and late native
// supplements. Selection markers are read-side state, not persisted raw rows.
func (l *ObservationLog) ScanOccupancyRows(ctx context.Context, uid, node string, from, to time.Time, visit func(RequestRow)) (*time.Time, error) {
	if !from.Before(to) {
		return nil, failure("InvalidQuery", "invalid occupancy observation interval")
	}
	tail := l.config.OccupancyTail
	read, err := l.prepare(ctx, map[string][2]time.Time{observationRequestStream: {from.Add(-tail), to.Add(tail)}})
	if err != nil {
		return nil, err
	}
	defer read.close()
	keep := func(r *RequestRow) bool {
		return (uid == "" || r.WorkloadUID == uid) && (node == "" || r.Node == nil || r.Node.UID == node)
	}
	selected := map[string]bool{}
	selectRow := func(r *RequestRow) {
		if keep(r) && occupancyRowInWindow(r, from, to) {
			selected[r.RequestID] = true
		}
	}
	if err := l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var row RequestRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		selectRow(&row)
		return nil
	}); err != nil {
		return nil, err
	}
	for _, row := range read.open {
		selectRow(row)
	}
	for id := range selected {
		visit(RequestRow{Kind: requestRowOccupancySelected, RequestID: id})
	}
	for _, file := range read.files[observationRequestStream] {
		if _, err := file.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	if err := l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var row RequestRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		if selected[row.RequestID] && keep(&row) {
			visit(row)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for id, row := range read.open {
		if selected[id] && keep(row) {
			copied := *row
			copied.Kind = requestRowOpen
			visit(copied)
		}
	}
	coverage := read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}})
	return coverage.gapUntil(from), nil
}

func occupancyRowInWindow(row *RequestRow, from, to time.Time) bool {
	if row.NativeRunningAt != nil && observationInInterval(*row.NativeRunningAt, from, to) {
		return true
	}
	return !row.ReceivedAt.IsZero() && observationInInterval(row.ReceivedAt, from, to)
}
