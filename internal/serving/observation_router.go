package serving

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// RouterObservationSource names one current or retired process generation.
// Directory, when present, is a read-only mount of that router's private buffer
// root (with v1alpha5 beneath it), not a shared writable observation directory.
// Open is a report snapshot at ObservedAt; it does not replace unuploaded rows.
type RouterObservationSource struct {
	RouterID   string
	Directory  string
	JoinedAt   time.Time
	LeftAt     time.Time
	ObservedAt time.Time
	Open       []RequestRow
}

func validObservationRouterID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func observationRouterPrefix(id string) string {
	if id == "" {
		return observationArchivePrefix
	}
	return observationArchivePrefix + "routers/" + id + "/"
}

// AdvanceRouterObservation makes an idle router's two owned streams explicit for
// the current UTC hour without manufacturing a request or catalog record.
// Call before publishing the report whose ObservedAt covers that prefix.
func (l *ObservationLog) AdvanceRouterObservation() error {
	if l.config.RouterID == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.advanceRouterObservationLocked()
}

func (l *ObservationLog) advanceRouterObservationLocked() error {
	if l.config.RouterID == "" {
		return nil
	}
	if l.closed {
		return errors.New("observation log is closed")
	}
	now := l.now()
	hour := now.UTC().Truncate(time.Hour)
	if l.routerObservationHour.Equal(hour) {
		return nil
	}
	for _, stream := range []string{observationRequestStream, observationCatalogStream} {
		if err := l.writeLocked(stream, now, nil); err != nil {
			l.lastError = err
			return err
		}
	}
	l.routerObservationHour = hour
	return nil
}

// OpenRows returns detached values; callers cannot modify the writer's rows.
func (l *ObservationLog) OpenRows() []RequestRow {
	l.requestMu.Lock()
	defer l.requestMu.Unlock()
	rows := make([]RequestRow, 0, len(l.open))
	for _, row := range l.open {
		rows = append(rows, copyObservationRow(*row))
	}
	return rows
}

func copyObservationRow(row RequestRow) RequestRow {
	if row.Node != nil {
		node := *row.Node
		row.Node = &node
	}
	for _, field := range []**int64{&row.ElapsedNanos, &row.ValidatedNanos, &row.AcceptedNanos, &row.DispatchedNanos, &row.QueuedNanos, &row.RunningNanos, &row.ExecutedNanos, &row.ExchangeNanos} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	for _, field := range []**time.Time{&row.DispatchedAt, &row.NativeQueuedAt, &row.NativeRunningAt, &row.NativeFinishedAt} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	if row.NativeElapsedMS != nil {
		value := *row.NativeElapsedMS
		row.NativeElapsedMS = &value
	}
	row.profile, row.definition, row.finished = nil, nil, nil
	return row
}

func (r *observationRead) localStream(hour time.Time, stream string) bool {
	if r.routerID == "" {
		return r.localHours[hour]
	}
	for _, file := range r.files[stream] {
		if file.at.Truncate(time.Hour).Equal(hour) {
			return true
		}
	}
	return false
}

func (r *observationRead) routerRanges(ranges map[string][2]time.Time) map[string][2]time.Time {
	if r.router == nil {
		return ranges
	}
	bounds, ok := ranges[observationRequestStream]
	if !ok {
		return map[string][2]time.Time{}
	}
	if bounds[0].IsZero() || bounds[0].Before(r.router.JoinedAt) {
		bounds[0] = r.router.JoinedAt
	}
	if !r.router.LeftAt.IsZero() && (bounds[1].IsZero() || bounds[1].After(r.router.LeftAt)) {
		bounds[1] = r.router.LeftAt
	}
	if !bounds[1].IsZero() && !bounds[0].Before(bounds[1]) {
		return map[string][2]time.Time{}
	}
	return map[string][2]time.Time{observationRequestStream: bounds}
}

func (l *ObservationLog) prepareRouters(read *observationRead, ranges map[string][2]time.Time, routers []RouterObservationSource) error {
	if _, wanted := ranges[observationRequestStream]; !wanted {
		return nil
	}
	seen := map[string]bool{}
	for _, source := range routers {
		if !validObservationRouterID(source.RouterID) || seen[source.RouterID] {
			return failure("IncompleteObservation", "invalid or duplicate observation router ID %q", source.RouterID)
		}
		seen[source.RouterID] = true
		if source.JoinedAt.IsZero() || !source.LeftAt.IsZero() && source.LeftAt.Before(source.JoinedAt) {
			return failure("IncompleteObservation", "router %s has invalid membership interval", source.RouterID)
		}
		child := &observationRead{files: map[string][]observationOpenFile{}, open: map[string]*RequestRow{}, definitions: map[string]*Definition{}, localHours: map[time.Time]bool{}, start: source.JoinedAt, now: read.now, ctx: read.ctx, routerID: source.RouterID, router: &source}
		read.routerReads = append(read.routerReads, child)
		selected := child.routerRanges(ranges)
		if len(selected) == 0 {
			continue
		}
		// Finals can be appended after retirement; clip coverage, not their search.
		bounds := selected[observationRequestStream]
		bounds[1] = ranges[observationRequestStream][1]
		selected[observationRequestStream] = bounds
		config := l.config
		config.RouterID, config.RouterSources = source.RouterID, nil
		remote := &ObservationLog{config: config, root: filepath.Join(source.Directory, observationVersion)}
		if source.Directory != "" {
			if err := remote.captureRouterBuffer(child, selected); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		for _, row := range source.Open {
			if row.RouterID != source.RouterID {
				return failure("IncompleteObservation", "open row router identity differs from source %s", source.RouterID)
			}
			copied := copyObservationRow(row)
			child.open[row.RequestID] = &copied
			read.open[row.RequestID] = &copied
		}
		if err := remote.selectSources(child, selected); err != nil {
			return err
		}
		for stream, sources := range child.sources {
			if stream != observationRequestStream {
				continue
			}
			for _, item := range sources {
				item.owner = child
				read.sources[stream] = append(read.sources[stream], item)
			}
		}
	}
	return read.ctx.Err()
}

func (l *ObservationLog) captureRouterBuffer(read *observationRead, ranges map[string][2]time.Time) error {
	definitions := filepath.Join(l.root, observationDefinitionsFile)
	if err := scanObservationFile(definitions, -1, func(raw []byte) error {
		var row DefinitionObservation
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		read.definitions[row.Identity] = row.Definition
		return nil
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hours, err := l.hours()
	if err != nil {
		return err
	}
	for _, hour := range hours {
		read.localHours[hour] = true
		for _, stream := range []string{observationRequestStream, observationCatalogStream} {
			bounds := ranges[observationRequestStream]
			if !observationHourWanted(hour, bounds) {
				continue
			}
			files, err := l.hourFiles(hour, stream)
			if err != nil {
				return err
			}
			for _, segment := range files {
				// Capture the whole selected hour: final rows refer to earlier receipts.
				if err := l.openReadFile(read, stream, segment); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *observationRead) rewindRequests() error {
	for _, file := range r.files[observationRequestStream] {
		if _, err := file.file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	for _, child := range r.routerReads {
		if err := child.rewindRequests(); err != nil {
			return err
		}
	}
	return nil
}
