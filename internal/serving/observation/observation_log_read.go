package observation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	serving "github.com/lab-paper-code/chill/internal/serving"
)

type observationSource struct {
	hour   time.Time
	object *evidencestore.ObjectIdentity
}

// prepare freezes local hours before listing objects. An hour remains local even
// when it has no file for a requested stream, or is removed after capture.
func (l *ObservationLog) prepare(ctx context.Context, ranges map[string][2]time.Time) (*observationRead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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
	return read, nil
}

func observationObjectHour(object evidencestore.ObjectIdentity) (time.Time, string, bool) {
	if !strings.HasPrefix(object.Key, observationArchivePrefix) {
		return time.Time{}, "", false
	}
	parts := strings.Split(strings.TrimPrefix(object.Key, observationArchivePrefix), "/")
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
	objects, err := l.archiveSources(read, ranges)
	if err != nil {
		return err
	}
	read.selectStreamSources(ranges, objects)
	return read.ctx.Err()
}

func (r *observationRead) needsArchive(ranges map[string][2]time.Time) bool {
	for _, bounds := range ranges {
		if bounds[0].IsZero() {
			return true
		}
		end := bounds[1]
		if end.IsZero() || end.After(r.now) {
			end = r.now
		}
		for hour := bounds[0].UTC().Truncate(time.Hour); !hour.After(end); hour = hour.Add(time.Hour) {
			if !r.localHours[hour] {
				return true
			}
		}
	}
	return false
}

func (l *ObservationLog) archiveSources(read *observationRead, ranges map[string][2]time.Time) (map[string]map[time.Time]evidencestore.ObjectIdentity, error) {
	objects := map[string]map[time.Time]evidencestore.ObjectIdentity{}
	if read.needsArchive(ranges) && read.archive != nil {
		listed, err := read.archive.List(read.ctx, observationArchivePrefix)
		if err != nil {
			return nil, err
		}
		for _, object := range listed {
			if err := read.ctx.Err(); err != nil {
				return nil, err
			}
			hour, stream, valid := observationObjectHour(object)
			if !valid || read.localHours[hour] {
				continue
			}
			if objects[stream] == nil {
				objects[stream] = map[time.Time]evidencestore.ObjectIdentity{}
			}
			if previous, exists := objects[stream][hour]; exists && previous.Store != object.Store {
				return nil, &serving.Error{Code: "IncompleteObservation", Message: fmt.Sprintf("multiple archive objects for %s %s", observationHour(hour), stream)}
			}
			objects[stream][hour] = object
		}
	}
	return objects, nil
}

func (read *observationRead) selectStreamSources(ranges map[string][2]time.Time, objects map[string]map[time.Time]evidencestore.ObjectIdentity) {
	catalogHours := map[time.Time]bool{}
	for stream, bounds := range ranges {
		if stream == observationCatalogStream {
			continue
		}
		for hour := range read.localHours {
			if observationHourWanted(hour, bounds) {
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
		if read.localHours[hour] {
			read.sources[observationCatalogStream] = append(read.sources[observationCatalogStream], observationSource{hour: hour})
		} else if object, exists := objects[observationCatalogStream][hour]; exists {
			object := object
			read.sources[observationCatalogStream] = append(read.sources[observationCatalogStream], observationSource{hour: hour, object: &object})
		}
	}
	for stream := range read.sources {
		slices.SortFunc(read.sources[stream], func(a, b observationSource) int { return a.hour.Compare(b.hour) })
	}
}

func (r *observationRead) coverage(ranges map[string][2]time.Time) serving.ObservationCoverage {
	coverage := serving.ObservationCoverage{Sources: []serving.ObservationSource{}, Gaps: []serving.ObservationGap{}}
	streams := make([]string, 0, len(ranges))
	for stream := range ranges {
		streams = append(streams, stream)
	}
	slices.Sort(streams)
	for _, stream := range streams {
		r.streamCoverage(&coverage, stream, ranges[stream])
	}
	return coverage
}

func (r *observationRead) streamCoverage(coverage *serving.ObservationCoverage, stream string, bounds [2]time.Time) {
	from, to := bounds[0].UTC(), bounds[1].UTC()
	if to.IsZero() {
		to = r.now
	}
	if from.IsZero() {
		from = r.start
		for _, source := range r.sources[stream] {
			if source.hour.Before(from) {
				from = source.hour
			}
		}
	}
	available := map[time.Time]observationSource{}
	for _, source := range r.sources[stream] {
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
		r.hourCoverage(coverage, stream, hour, start, end, available)
	}
}

func (r *observationRead) hourCoverage(coverage *serving.ObservationCoverage, stream string, hour, start, end time.Time, available map[time.Time]observationSource) {
	source, exists := available[hour]
	if exists && source.object == nil && start.Before(r.start) && !hour.Before(r.start.Truncate(time.Hour)) {
		until := r.start
		if until.After(end) {
			until = end
		}
		coverage.Gaps = append(coverage.Gaps, serving.ObservationGap{From: start, To: until, Stream: stream, Reason: "StreamUnavailable"})
		start = until
	}
	if !start.Before(end) {
		return
	}
	if !exists {
		coverage.Gaps = append(coverage.Gaps, serving.ObservationGap{From: start, To: end, Stream: stream, Reason: "StreamUnavailable"})
		return
	}
	r.addSourceCoverage(coverage, stream, start, end, source)
}

func (r *observationRead) addSourceCoverage(coverage *serving.ObservationCoverage, stream string, start, end time.Time, source observationSource) {
	entry := serving.ObservationSource{From: start, To: end, Stream: stream, Source: "local"}
	if source.object != nil {
		entry.Source, entry.Store, entry.Key = observationArchiveSource, source.object.Store, source.object.Key
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

func (r *observationRead) scanSource(stream string, source observationSource, scanner *observationScanner, visit func([]byte) error) error {
	r.scanningHour = source.hour
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

func (l *ObservationLog) readCatalog(read *observationRead, hour time.Time) error {
	if read.catalogs == nil {
		read.catalogs = map[time.Time]*ObservationCatalog{}
	}
	if read.catalogs[hour] != nil {
		return nil
	}
	catalog := &ObservationCatalog{}
	read.catalogs[hour] = catalog
	visit := func(raw []byte) error {
		if read.definitionsOnly {
			var header struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(raw, &header); err != nil {
				return err
			}
			if header.Kind != "definition" {
				return nil
			}
		}
		var row CatalogRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		catalog.Add(row)
		if row.Kind == "definition" {
			var definition serving.Definition
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

// observationRead holds files opened outside the writer lock at frozen complete
// append offsets. Deleting an hour does not affect an open file.
type observationRead struct {
	files           map[string][]observationOpenFile
	open            map[string]*serving.RequestRow
	definitions     map[string]*serving.Definition
	start           time.Time
	ctx             context.Context
	now             time.Time
	localHours      map[time.Time]bool
	sources         map[string][]observationSource
	archive         ObservationArchive
	catalogs        map[time.Time]*ObservationCatalog
	scanningHour    time.Time
	definitionsOnly bool
}

type observationOpenFile struct {
	file *os.File
	at   time.Time
	size int64
}

func (r *observationRead) close() {
	for _, files := range r.files {
		for _, file := range files {
			_ = file.file.Close()
		}
	}
}

// capture snapshots stream paths and complete lengths under the writer lock;
// directory listing and file opens run outside it. A zero to reads the latest
// captured file. captureMu excludes retention until selected paths are open,
// without waiting for archive uploads or delaying appends.
func (l *ObservationLog) capture(ranges map[string][2]time.Time) (*observationRead, error) {
	l.captureMu.RLock()
	defer l.captureMu.RUnlock()
	hours, err := l.hours()
	if err != nil {
		return nil, err
	}
	read, segments := l.captureSnapshot(ranges, hours)
	for stream, files := range segments {
		sort.Slice(files, func(a, b int) bool { return files[a].at.Before(files[b].at) })
		for _, segment := range files {
			if err := l.openReadFile(read, stream, segment); err != nil {
				read.close()
				return nil, err
			}
		}
	}
	return read, nil
}

func (l *ObservationLog) captureSnapshot(ranges map[string][2]time.Time, hours []time.Time) (*observationRead, map[string][]observationSegment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mu.Recorder.Active() {
		l.mu.Mark(serving.DiagnosticCapture, serving.DiagnosticBegin)
		defer l.mu.Mark(serving.DiagnosticCapture, serving.DiagnosticEnd)
	}
	read := &observationRead{
		files: map[string][]observationOpenFile{}, open: map[string]*serving.RequestRow{}, definitions: map[string]*serving.Definition{},
		start: l.floor, now: l.now(), ctx: context.Background(), localHours: map[time.Time]bool{},
	}
	l.requestMu.Lock()
	for id, row := range l.open {
		copied := *row
		read.open[id] = &copied
	}
	l.requestMu.Unlock()
	for id, d := range l.retained {
		read.definitions[id] = d
	}
	read.localHours[read.now.Truncate(time.Hour)] = true
	for _, hour := range hours {
		read.localHours[hour] = true
	}
	segments := map[string][]observationSegment{}
	for _, segment := range l.segments {
		hour := segment.at.Truncate(time.Hour)
		read.localHours[hour] = true
		bounds, wanted := ranges[segment.stream]
		if segment.stream == observationCatalogStream {
			for stream, requested := range ranges {
				if stream != observationCatalogStream && observationHourWanted(hour, requested) {
					bounds, wanted = [2]time.Time{hour, hour.Add(time.Hour)}, true
					break
				}
			}
		}
		if wanted && !segment.at.Before(bounds[0].Truncate(time.Minute)) && (bounds[1].IsZero() || !segment.at.After(bounds[1])) {
			segments[segment.stream] = append(segments[segment.stream], segment)
		}
	}
	return read, segments
}

func (l *ObservationLog) openReadFile(r *observationRead, stream string, segment observationSegment) error {
	l.config.Diagnostics.Mark(serving.DiagnosticFileOpen, serving.DiagnosticBegin, "", 0)
	file, err := os.Open(segment.path)
	l.config.Diagnostics.Mark(serving.DiagnosticFileOpen, serving.DiagnosticEnd, "", 0)
	if err != nil {
		return err
	}
	r.files[stream] = append(r.files[stream], observationOpenFile{file: file, at: segment.at, size: segment.size})
	return nil
}

// errObservationStop ends a scan early without an error.
var errObservationStop = errors.New("observation scan stopped")

func (l *ObservationLog) scanStream(read *observationRead, stream string, visit func([]byte) error) error {
	if l.config.Diagnostics.Active() {
		l.config.Diagnostics.Mark(serving.DiagnosticQuery, serving.DiagnosticBegin, "", 0)
		defer l.config.Diagnostics.Mark(serving.DiagnosticQuery, serving.DiagnosticEnd, "", 0)
	}
	var scanner observationScanner
	if read.sources != nil {
		for _, source := range read.sources[stream] {
			if err := read.scanSource(stream, source, &scanner, visit); err != nil {
				return observationScanError(err)
			}
		}
		return read.ctx.Err()
	}
	for _, file := range read.files[stream] {
		read.scanningHour = file.at.Truncate(time.Hour)
		err := scanner.scan(observationContextReader{ctx: read.ctx, source: file.file}, file.size, visit)
		if err != nil {
			return observationScanError(fmt.Errorf("read %s: %w", file.file.Name(), err))
		}
	}
	return read.ctx.Err()
}

func scanObservationFile(name string, size int64, visit func([]byte) error) (result error) {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	var scanner observationScanner
	return scanner.scan(file, size, visit)
}

// observationScanner keeps its read and long-line buffers across files.
type observationScanner struct {
	reader         *bufio.Reader
	long           []byte
	requireNewline bool
}

// scan visits newline-terminated lines of a captured prefix; a negative size
// reads to the end. A line still being written has no newline and is skipped.
// Visitor bytes are borrowed for that call.
func (s *observationScanner) scan(source io.Reader, size int64, visit func([]byte) error) error {
	if size >= 0 {
		source = io.LimitReader(source, size)
	}
	if s.reader == nil {
		s.reader = bufio.NewReaderSize(source, 1<<16)
	} else {
		s.reader.Reset(source)
	}
	for {
		line, err := s.reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			s.long = append(s.long[:0], line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = s.reader.ReadSlice('\n')
				s.long = append(s.long, line...)
			}
			line = s.long
		}
		if err == io.EOF {
			if s.requireNewline && len(line) != 0 {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if err != nil {
			return err
		}
		if len(line) > 1 {
			if err := visit(line[:len(line)-1]); err != nil {
				return err
			}
		}
	}
}
