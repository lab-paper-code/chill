package serving

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// ObservationCoverage reports the available extent and unavailable stream hours.
// A missing archive stream is unknown, not an observed zero.
type ObservationCoverage struct {
	From    *time.Time          `json:"from"`
	To      *time.Time          `json:"to"`
	Sources []ObservationSource `json:"sources"`
	Gaps    []ObservationGap    `json:"gaps"`
}

type ObservationSource struct {
	From       time.Time  `json:"from"`
	To         time.Time  `json:"to"`
	Stream     string     `json:"stream"`
	Source     string     `json:"source"`
	Store      string     `json:"store,omitempty"`
	Key        string     `json:"key,omitempty"`
	RouterID   string     `json:"routerID,omitempty"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
}

type ObservationGap struct {
	From       time.Time  `json:"from"`
	To         time.Time  `json:"to"`
	Stream     string     `json:"stream"`
	Reason     string     `json:"reason"`
	RouterID   string     `json:"routerID,omitempty"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
}

type observationSource struct {
	hour   time.Time
	object *evidencestore.ObjectIdentity
	owner  *observationRead
}

// prepare freezes local hours before listing objects. An hour remains local even
// when it has no file for a requested stream, or is removed after capture.
func (l *ObservationLog) prepare(ctx context.Context, ranges map[string][2]time.Time) (*observationRead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The provider may hold control.mu while membership recording takes log.mu.
	// Snapshot it before capture, outside the writer lock.
	var routers []RouterObservationSource
	if l.config.RouterSources != nil {
		routers = l.config.RouterSources()
	}
	read, err := l.capture(ranges)
	if err != nil {
		return nil, err
	}
	read.ctx = ctx
	if err := l.selectSources(read, ranges); err != nil {
		read.close()
		return nil, err
	}
	if l.config.RouterSources != nil {
		if err := l.prepareRouters(read, ranges, routers); err != nil {
			read.close()
			return nil, err
		}
	}
	return read, nil
}

func observationObjectHour(object evidencestore.ObjectIdentity) (time.Time, string, bool) {
	return observationObjectHourAt(object, observationArchivePrefix)
}

func observationObjectHourAt(object evidencestore.ObjectIdentity, prefix string) (time.Time, string, bool) {
	if !strings.HasPrefix(object.Key, prefix) {
		return time.Time{}, "", false
	}
	parts := strings.Split(strings.TrimPrefix(object.Key, prefix), "/")
	if len(parts) != 2 {
		return time.Time{}, "", false
	}
	hour, err := time.Parse(observationHourLayout, parts[0])
	stream := strings.TrimSuffix(parts[1], ".jsonl.gz")
	return hour, stream, err == nil && slices.Contains(observationStreams, stream) && parts[1] == stream+".jsonl.gz"
}

func observationHourWanted(hour time.Time, bounds [2]time.Time) bool {
	return (bounds[0].IsZero() || hour.Add(time.Hour).After(bounds[0])) && (bounds[1].IsZero() || !hour.After(bounds[1]))
}

func (l *ObservationLog) selectSources(read *observationRead, ranges map[string][2]time.Time) error {
	read.sources = map[string][]observationSource{}
	read.archive, _ = l.config.Writer.(ObservationArchive)
	needArchive := false
	for stream, bounds := range ranges {
		if bounds[0].IsZero() {
			needArchive = true
			break
		}
		end := bounds[1]
		if end.IsZero() || end.After(read.now) {
			end = read.now
		}
		for hour := bounds[0].UTC().Truncate(time.Hour); !hour.After(end); hour = hour.Add(time.Hour) {
			if !read.localStream(hour, stream) || read.routerID != "" && !read.localStream(hour, observationCatalogStream) {
				needArchive = true
				break
			}
		}
	}
	objects := map[string]map[time.Time]evidencestore.ObjectIdentity{}
	if needArchive && read.archive != nil {
		listed, err := read.archive.List(read.ctx, observationRouterPrefix(l.config.RouterID))
		if err != nil {
			return err
		}
		for _, object := range listed {
			if err := read.ctx.Err(); err != nil {
				return err
			}
			hour, stream, valid := observationObjectHourAt(object, observationRouterPrefix(l.config.RouterID))
			if !valid || read.localStream(hour, stream) {
				continue
			}
			if read.router != nil && hour.Equal(read.now.Truncate(time.Hour)) && !read.localStream(hour, stream) {
				continue // An archive cannot prove the tail of an unmounted live buffer.
			}
			if objects[stream] == nil {
				objects[stream] = map[time.Time]evidencestore.ObjectIdentity{}
			}
			if previous, exists := objects[stream][hour]; exists && previous.Store != object.Store {
				return failure("IncompleteObservation", "multiple archive objects for %s %s", observationHour(hour), stream)
			}
			objects[stream][hour] = object
		}
	}
	catalogHours := map[time.Time]bool{}
	for stream, bounds := range ranges {
		if stream == observationCatalogStream {
			continue
		}
		for hour := range read.localHours {
			if observationHourWanted(hour, bounds) && read.localStream(hour, stream) {
				read.sources[stream] = append(read.sources[stream], observationSource{hour: hour})
				catalogHours[hour] = true
			}
		}
		for hour, object := range objects[stream] {
			if observationHourWanted(hour, bounds) {
				object := object
				read.sources[stream] = append(read.sources[stream], observationSource{hour: hour, object: &object})
				catalogHours[hour] = true
			}
		}
	}
	for hour := range catalogHours {
		if read.localStream(hour, observationCatalogStream) {
			read.sources[observationCatalogStream] = append(read.sources[observationCatalogStream], observationSource{hour: hour})
		} else if object, exists := objects[observationCatalogStream][hour]; exists {
			object := object
			read.sources[observationCatalogStream] = append(read.sources[observationCatalogStream], observationSource{hour: hour, object: &object})
		}
	}
	for stream := range read.sources {
		slices.SortFunc(read.sources[stream], func(a, b observationSource) int { return a.hour.Compare(b.hour) })
	}
	return read.ctx.Err()
}

func (r *observationRead) coverage(ranges map[string][2]time.Time) ObservationCoverage {
	coverage := ObservationCoverage{Sources: []ObservationSource{}, Gaps: []ObservationGap{}}
	streams := make([]string, 0, len(ranges))
	for stream := range ranges {
		streams = append(streams, stream)
	}
	slices.Sort(streams)
	for _, stream := range streams {
		bounds := ranges[stream]
		from, to := bounds[0].UTC(), bounds[1].UTC()
		if to.IsZero() {
			to = r.now
		}
		if from.IsZero() {
			from = r.start
			for _, source := range r.sources[stream] {
				if source.owner != nil {
					continue
				}
				if source.hour.Before(from) {
					from = source.hour
				}
			}
		}
		available := map[time.Time]observationSource{}
		for _, source := range r.sources[stream] {
			if source.owner != nil {
				continue
			}
			available[source.hour] = source
		}
		for hour := from.Truncate(time.Hour); hour.Before(to); hour = hour.Add(time.Hour) {
			start, end := hour, hour.Add(time.Hour)
			if start.Before(from) {
				start = from
			}
			if end.After(to) {
				end = to
			}
			source, exists := available[hour]
			if exists && source.object == nil && start.Before(r.start) && !hour.Before(r.start.Truncate(time.Hour)) {
				until := r.start
				if until.After(end) {
					until = end
				}
				coverage.Gaps = append(coverage.Gaps, ObservationGap{From: start, To: until, Stream: stream, Reason: "StreamUnavailable"})
				start = until
			}
			if !start.Before(end) {
				continue
			}
			if !exists {
				reason := "StreamUnavailable"
				if r.router != nil && hour.Equal(r.now.Truncate(time.Hour)) {
					reason = "LiveBufferUnavailable"
				}
				coverage.Gaps = append(coverage.Gaps, ObservationGap{From: start, To: end, Stream: stream, Reason: reason})
				continue
			}
			// Receipts and unassigned requests exist only in report Open rows.
			// Even a mounted request file cannot prove arrivals after that snapshot.
			if r.router != nil && stream == observationRequestStream && r.router.ObservedAt.Before(end) {
				knownUntil := r.router.ObservedAt
				if knownUntil.Before(start) {
					knownUntil = start
				}
				coverage.Gaps = append(coverage.Gaps, ObservationGap{
					From: knownUntil, To: end, Stream: stream, Reason: "RouterReportUnavailable",
				})
				end = knownUntil
				if !start.Before(end) {
					continue
				}
			}
			entry := ObservationSource{From: start, To: end, Stream: stream, Source: "local"}
			entry.RouterID = r.routerID
			if r.router != nil && !r.router.ObservedAt.IsZero() {
				value := r.router.ObservedAt
				entry.ObservedAt = &value
			}
			if source.object != nil {
				entry.Source, entry.Store, entry.Key = observationArchiveSource, source.object.Store, source.object.Key
			}
			if r.router != nil {
				catalogAvailable := false
				for _, catalog := range r.sources[observationCatalogStream] {
					catalogAvailable = catalogAvailable || catalog.hour.Equal(hour)
				}
				if !catalogAvailable {
					coverage.Gaps = append(coverage.Gaps, ObservationGap{From: start, To: end, Stream: observationCatalogStream, Reason: "StreamUnavailable"})
				}
			}
			coverage.Sources = append(coverage.Sources, entry)
			if coverage.From == nil || start.Before(*coverage.From) {
				value := start
				coverage.From = &value
			}
			if coverage.To == nil || end.After(*coverage.To) {
				value := end
				coverage.To = &value
			}
		}
	}
	for i := range coverage.Gaps {
		coverage.Gaps[i].RouterID = r.routerID
		if r.router != nil && !r.router.ObservedAt.IsZero() {
			value := r.router.ObservedAt
			coverage.Gaps[i].ObservedAt = &value
		}
	}
	for _, child := range r.routerReads {
		childCoverage := child.coverage(child.routerRanges(ranges))
		coverage.Sources = append(coverage.Sources, childCoverage.Sources...)
		coverage.Gaps = append(coverage.Gaps, childCoverage.Gaps...)
		if childCoverage.From != nil && (coverage.From == nil || childCoverage.From.Before(*coverage.From)) {
			coverage.From = childCoverage.From
		}
		if childCoverage.To != nil && (coverage.To == nil || childCoverage.To.After(*coverage.To)) {
			coverage.To = childCoverage.To
		}
	}
	return coverage
}

func (c ObservationCoverage) gapUntil(from time.Time) *time.Time {
	if from.IsZero() {
		return nil
	}
	ends := map[string]time.Time{}
	for _, gap := range c.Gaps {
		at, exists := ends[gap.Stream]
		if !exists {
			at = from
		}
		if !gap.From.After(at) && gap.To.After(at) {
			ends[gap.Stream] = gap.To
		}
	}
	var until *time.Time
	for _, at := range ends {
		if until == nil || at.After(*until) {
			value := at
			until = &value
		}
	}
	return until
}

func (r *observationRead) scanSource(stream string, source observationSource, scanner *observationScanner, visit func([]byte) error) error {
	r.scanningHour = source.hour
	r.scanningRead = source.owner
	if source.owner != nil {
		return source.owner.scanSource(stream, observationSource{hour: source.hour, object: source.object}, scanner, visit)
	}
	stopped := false
	checked := func(raw []byte) error {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		err := visit(raw)
		stopped = errors.Is(err, errObservationStop)
		return err
	}
	if source.object != nil {
		err := readArchivedStream(r.ctx, r.archive, *source.object, checked)
		if err == nil && stopped {
			return errObservationStop
		}
		return err
	}
	for _, file := range r.files[stream] {
		if !file.at.Truncate(time.Hour).Equal(source.hour) {
			continue
		}
		if err := scanner.scan(observationContextReader{ctx: r.ctx, source: file.file}, file.size, checked); err != nil {
			return fmt.Errorf("read %s: %w", file.file.Name(), err)
		}
	}
	return r.ctx.Err()
}

const observationArchiveSource = "archive"

func (l *ObservationLog) readCatalogs(read *observationRead) error {
	for _, child := range read.routerReads {
		if err := l.readCatalogs(child); err != nil {
			return err
		}
		for id, definition := range child.definitions {
			read.definitions[id] = definition
		}
	}
	for stream, sources := range read.sources {
		if stream == observationCatalogStream {
			continue
		}
		for _, source := range sources {
			if source.owner != nil {
				continue
			}
			if err := l.readCatalog(read, source.hour); err != nil {
				return err
			}
		}
	}
	return read.ctx.Err()
}

func (l *ObservationLog) readCatalog(read *observationRead, hour time.Time) error {
	if read.catalogs == nil {
		read.catalogs = map[time.Time]*ObservationCatalog{}
	}
	if read.catalogs[hour] != nil {
		return nil
	}
	catalog := &ObservationCatalog{routerID: read.routerID}
	read.catalogs[hour] = catalog
	visit := func(raw []byte) error {
		var row CatalogRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		catalog.Add(row)
		if row.Kind == "definition" {
			var definition Definition
			if err := json.Unmarshal(row.Body, &definition); err != nil {
				return err
			}
			read.definitions[row.Key] = &definition
		}
		return nil
	}
	var scanner observationScanner
	for _, source := range read.sources[observationCatalogStream] {
		if source.hour.Equal(hour) {
			return read.scanSource(observationCatalogStream, source, &scanner, visit)
		}
	}
	return read.ctx.Err()
}

func (r *observationRead) catalog() *ObservationCatalog {
	if r.scanningRead != nil {
		return r.scanningRead.catalog()
	}
	if catalog := r.catalogs[r.scanningHour]; catalog != nil {
		return catalog
	}
	return &ObservationCatalog{}
}

// A context-aware reader also checks cancellation between buffers of a long line.
type observationContextReader struct {
	ctx    context.Context
	source interface{ Read([]byte) (int, error) }
}

func (r observationContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func observationScanError(err error) error {
	if errors.Is(err, errObservationStop) {
		return nil
	}
	return err
}
