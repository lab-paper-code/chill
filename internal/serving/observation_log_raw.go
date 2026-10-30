package serving

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type RawSegment struct {
	Minute string `json:"minute"`
	Bytes int64 `json:"bytes"`
}

type RawArchive struct {
	Store string `json:"store"`
	Key string `json:"key"`
	Bytes int64 `json:"bytes"`
}

type RawKind struct {
	Kind string `json:"kind"`
	Available bool `json:"available"`
	Segments []RawSegment `json:"segments"`
	Archive *RawArchive `json:"archive"`
}

type RawHourIndex struct {
	Hour string `json:"hour"`
	At time.Time `json:"at"`
	Status string `json:"status"`
	Kinds []RawKind `json:"kinds"`
}

// RawObservation owns a fixed byte range. The caller must close Reader.
type RawObservation struct {
	Reader io.ReadCloser
	ContentType string
	Bytes int64
}

func rawHour(hour string) (time.Time, error) {
	at, err := time.Parse(observationHourLayout, hour)
	if err != nil || at.Year() < 1 || at.Format(observationHourLayout) != hour {
		return time.Time{}, failure("InvalidQuery", "hour must be a UTC YYYYMMDDTHH")
	}
	return at, nil
}

// captureRaw freezes the local-hour choice, closed-minute descriptors and lengths
// together under the writer lock. Archive I/O and all copying run outside it.
func (l *ObservationLog) captureRaw(ctx context.Context, hour time.Time, streams []string) (*observationRead, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil { return nil, err }
	read := &observationRead{ctx: ctx, now: l.now().UTC(), files: map[string][]observationOpenFile{}, localHours: map[time.Time]bool{}}
	info, err := os.Stat(filepath.Join(l.root, observationHour(hour)))
	if err != nil && !errors.Is(err, os.ErrNotExist) { return nil, err }
	if hour.Equal(read.now.Truncate(time.Hour)) || err == nil && info.IsDir() {
		read.localHours[hour] = true
		for _, stream := range streams {
			segments, err := l.hourFiles(hour, stream)
			if err != nil { read.close(); return nil, err }
			for _, segment := range segments {
				if !segment.at.Before(read.now.Truncate(time.Minute)) { continue }
				if err := ctx.Err(); err != nil { read.close(); return nil, err }
				if err := l.openReadFile(read, stream, segment); err != nil { read.close(); return nil, err }
			}
		}
	}
	if err := ctx.Err(); err != nil { read.close(); return nil, err }
	return read, nil
}

func (l *ObservationLog) rawObjects(read *observationRead, hour time.Time) (map[string]map[time.Time]evidencestore.ObjectIdentity, error) {
	read.archive, _ = l.config.Writer.(ObservationArchive)
	objects, err := l.archiveSources(read, map[string][2]time.Time{observationRequestStream: {hour, hour}})
	if err == nil { err = read.ctx.Err() }
	return objects, err
}

// RawHour lists original closed segments, or archive locations, never both.
func (l *ObservationLog) RawHour(ctx context.Context, hour string) (RawHourIndex, error) {
	at, err := rawHour(hour)
	if err != nil { return RawHourIndex{}, err }
	streams := slices.Clone(observationStreams)
	slices.Sort(streams)
	read, err := l.captureRaw(ctx, at, streams)
	if err != nil { return RawHourIndex{}, err }
	defer read.close()
	result := RawHourIndex{Hour: hour, At: read.now, Status: "archived", Kinds: make([]RawKind, 0, len(streams))}
	var objects map[string]map[time.Time]evidencestore.ObjectIdentity
	local := read.localHours[at]
	if local {
		result.Status = "closed-local"
		if at.Equal(read.now.Truncate(time.Hour)) { result.Status = "open" }
	} else {
		objects, err = l.rawObjects(read, at)
		if err != nil { return RawHourIndex{}, err }
	}
	available := false
	for _, stream := range streams {
		kind := RawKind{Kind: stream, Segments: []RawSegment{}}
		for _, file := range read.files[stream] {
			kind.Segments = append(kind.Segments, RawSegment{Minute: file.at.Format("04"), Bytes: file.size})
		}
		kind.Available = len(kind.Segments) > 0
		if object, ok := objects[stream][at]; ok {
			kind.Archive = &RawArchive{Store: object.Store, Key: object.Key, Bytes: object.Size}
			kind.Available = true
		}
		available = available || kind.Available
		result.Kinds = append(result.Kinds, kind)
	}
	if !local && !available { return RawHourIndex{}, failure("ObservationUnavailable", "observation hour is unavailable") }
	if err := ctx.Err(); err != nil { return RawHourIndex{}, err }
	return result, nil
}

// OpenRaw returns unchanged JSONL bytes locally, or unchanged stored gzip bytes.
func (l *ObservationLog) OpenRaw(ctx context.Context, hour, kind string) (RawObservation, error) {
	at, err := rawHour(hour)
	if err != nil { return RawObservation{}, err }
	if !slices.Contains(observationStreams, kind) { return RawObservation{}, failure("InvalidQuery", "unsupported observation kind") }
	read, err := l.captureRaw(ctx, at, []string{kind})
	if err != nil { return RawObservation{}, err }
	if read.localHours[at] {
		files := read.files[kind]
		if len(files) == 0 {
			read.close()
			if at.Equal(read.now.Truncate(time.Hour)) { return RawObservation{}, failure("ObservationNotReady", "no closed minute is ready") }
			return RawObservation{}, failure("ObservationUnavailable", "observation stream is unavailable")
		}
		readers := make([]io.Reader, 0, len(files))
		closers := make([]io.Closer, 0, len(files))
		var size int64
		for _, file := range files {
			readers = append(readers, io.NewSectionReader(file.file, 0, file.size))
			closers = append(closers, file.file)
			size += file.size
		}
		return newRawObservation(ctx, io.MultiReader(readers...), closers, "application/x-ndjson", size), nil
	}
	objects, err := l.rawObjects(read, at)
	if err != nil { return RawObservation{}, err }
	object, ok := objects[kind][at]
	if !ok { return RawObservation{}, failure("ObservationUnavailable", "observation stream is unavailable") }
	body, err := read.archive.Open(ctx, object)
	if err != nil { return RawObservation{}, err }
	return newRawObservation(ctx, io.LimitReader(body, object.Size), []io.Closer{body}, "application/gzip", object.Size), nil
}

// OpenRawDefinitions freezes the last complete line while append is excluded.
func (l *ObservationLog) OpenRawDefinitions(ctx context.Context) (RawObservation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil { return RawObservation{}, err }
	file, err := os.Open(filepath.Join(l.root, observationDefinitionsFile))
	if err != nil { return RawObservation{}, err }
	info, err := file.Stat()
	if err != nil { return RawObservation{}, errors.Join(err, file.Close()) }
	end := info.Size()
	var buffer [4096]byte
	for end > 0 {
		if err := ctx.Err(); err != nil { return RawObservation{}, errors.Join(err, file.Close()) }
		start := max(int64(0), end-int64(len(buffer)))
		n, err := file.ReadAt(buffer[:end-start], start)
		if err != nil { return RawObservation{}, errors.Join(err, file.Close()) }
		if last := bytes.LastIndexByte(buffer[:n], '\n'); last >= 0 { end = start+int64(last)+1; break }
		end = start
	}
	return newRawObservation(ctx, io.NewSectionReader(file, 0, end), []io.Closer{file}, "application/x-ndjson", end), nil
}

type rawObservationReader struct {
	io.Reader
	closers []io.Closer
	stop func() bool
	once sync.Once
	err error
}

func newRawObservation(ctx context.Context, source io.Reader, closers []io.Closer, contentType string, size int64) RawObservation {
	reader := &rawObservationReader{Reader: observationContextReader{ctx: ctx, source: source}, closers: closers}
	reader.stop = context.AfterFunc(ctx, func() { reader.close() })
	return RawObservation{Reader: reader, ContentType: contentType, Bytes: size}
}

func (r *rawObservationReader) close() error {
	r.once.Do(func() { for _, closer := range r.closers { r.err = errors.Join(r.err, closer.Close()) } })
	return r.err
}

func (r *rawObservationReader) Close() error {
	r.stop()
	return r.close()
}
