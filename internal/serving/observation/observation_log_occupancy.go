package observation

import (
	"context"
	"encoding/json"
	"io"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// ScanOccupancyRows reads only the requested interval and its configured native
// delivery tail. A first streaming pass selects receipt/native-window RequestIDs;
// a second visits their raw rows, including earlier assignments and late native
// supplements. Selection markers are read-side state, not persisted raw rows.
func (l *ObservationLog) ScanOccupancyRows(ctx context.Context, uid, node string, from, to time.Time, visit func(serving.RequestRow)) (*time.Time, error) {
	return l.ScanOccupancyRowsBounded(ctx, uid, node, from, to, 0, visit)
}

// ScanOccupancyRowsBounded additionally caps selected request state before any
// callback receives rows; an exceeded cap never publishes partial estimates.
func (l *ObservationLog) ScanOccupancyRowsBounded(ctx context.Context, uid, node string, from, to time.Time, maximumRequests int, visit func(serving.RequestRow)) (*time.Time, error) {
	if !from.Before(to) || maximumRequests < 0 {
		return nil, &serving.Error{Code: "InvalidQuery", Message: "invalid occupancy observation interval"}
	}
	tail := l.config.OccupancyTail
	read, err := l.prepare(ctx, map[string][2]time.Time{observationRequestStream: {from.Add(-tail), to.Add(tail)}})
	if err != nil {
		return nil, err
	}
	defer read.close()
	keep := func(r *serving.RequestRow) bool {
		return (uid == "" || r.WorkloadUID == uid) && (node == "" || r.Node == nil || r.Node.UID == node)
	}
	selected := map[string]bool{}
	selectRow := func(r *serving.RequestRow) error {
		if keep(r) && serving.OccupancyRowInWindow(r, from, to) {
			if !selected[r.RequestID] && maximumRequests > 0 && len(selected) >= maximumRequests {
				return &serving.Error{Code: "OccupancySelectionLimit", Message: "occupancy request selection exceeds configured maximumRequests"}
			}
			selected[r.RequestID] = true
		}
		return nil
	}
	if err := l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var row serving.RequestRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		return selectRow(&row)
	}); err != nil {
		return nil, err
	}
	for _, row := range read.open {
		if err := selectRow(row); err != nil {
			return nil, err
		}
	}
	for id := range selected {
		visit(serving.RequestRow{Kind: serving.RequestRowOccupancySelected, RequestID: id})
	}
	for _, file := range read.files[observationRequestStream] {
		if _, err := file.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	if err := l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var row serving.RequestRow
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
			copied.Kind = serving.RequestRowOpen
			visit(copied)
		}
	}
	coverage := read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}})
	return coverage.GapUntil(from), nil
}
