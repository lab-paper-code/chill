package observation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const observationSealSchema = "observation-seal.v1"
const observationUploadSchema = "observation-upload.v1"

type observationSeal struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Hour          string                    `json:"hour"`
	Streams       []observationSealedStream `json:"streams"`
}

type observationSealedStream struct {
	Kind     string                     `json:"kind"`
	Segments []observationSealedSegment `json:"segments"`
}

type observationSealedSegment struct {
	RelativePath string `json:"relativePath"`
	Bytes        int64  `json:"bytes"`
}

type observationUpload struct {
	SchemaVersion string                      `json:"schemaVersion"`
	Hour          string                      `json:"hour"`
	Objects       []observationUploadedObject `json:"objects"`
	AbsentStreams []string                    `json:"absentStreams"`
}

type observationUploadedObject struct {
	Stream  string                       `json:"stream"`
	Object  evidencestore.ObjectIdentity `json:"object"`
	Outcome evidencestore.CreateOutcome  `json:"outcome"`
}

// appendTimeLocked is shared by catalog and referring-row writers. Neither a
// late event timestamp nor wall-clock rollback can reopen a completed hour.
func (l *ObservationLog) appendTimeLocked() time.Time {
	at := l.now().UTC()
	if at.Before(l.appendFloor) {
		at = l.appendFloor
	}
	if at.Before(l.lastAppend) {
		at = l.lastAppend
	}
	l.lastAppend = at
	return at
}

func observationLocation(store, hour, stream string) evidencestore.ObjectLocation {
	return evidencestore.ObjectLocation{Store: store, Key: observationArchivePrefix + hour + "/" + stream + ".jsonl.gz"}
}

func observationReceiptPath(control string, hour time.Time) string {
	return filepath.Join(control, observationHour(hour)+".uploaded.json")
}

// readControlJSON rejects symlinks and nonregular metadata, and accepts exactly
// one JSON value. Incomplete temp files are never read as committed handoffs.
func readControlJSON(path string, value any) (err error) {
	file, err := openObservationRegular(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("observation handoff contains trailing data")
	}
	return nil
}

func openObservationRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(err, errors.New("observation input must be a regular file"), file.Close())
	}
	return file, nil
}

func readObservationSeal(root string, hour time.Time) (observationSeal, error) {
	var seal observationSeal
	if err := checkObservationHourDirectory(root, hour); err != nil {
		return seal, err
	}
	if err := readControlJSON(filepath.Join(root, observationHour(hour), "seal.json"), &seal); err != nil {
		return seal, err
	}
	if seal.SchemaVersion != observationSealSchema || seal.Hour != observationHour(hour) || len(seal.Streams) != len(observationStreams) {
		return seal, errors.New("invalid observation seal header")
	}
	seen := map[string]bool{}
	for _, stream := range seal.Streams {
		if !slices.Contains(observationStreams, stream.Kind) || seen[stream.Kind] || stream.Segments == nil {
			return seal, errors.New("invalid observation seal streams")
		}
		seen[stream.Kind] = true
		previous := -1
		for _, segment := range stream.Segments {
			name := segment.RelativePath
			// Only fixed stream.MM.jsonl basenames are allowed, never arbitrary
			// relative paths, directories or links from a manifest.
			prefix := stream.Kind + "."
			if len(name) != len(prefix)+len("00.jsonl") || name[:len(prefix)] != prefix || name[len(name)-6:] != ".jsonl" {
				return seal, errors.New("invalid sealed segment path")
			}
			minute, err := strconv.Atoi(name[len(prefix) : len(prefix)+2])
			if err != nil || minute < 0 || minute > 59 || name[len(prefix)] < '0' || name[len(prefix)] > '9' || name[len(prefix)+1] < '0' || name[len(prefix)+1] > '9' || minute <= previous || segment.Bytes < 0 {
				return seal, errors.New("invalid sealed segment order or size")
			}
			previous = minute
		}
	}
	return seal, nil
}

func checkObservationHourDirectory(root string, hour time.Time) error {
	info, err := os.Lstat(filepath.Join(root, observationHour(hour)))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("observation hour must be a directory, not a symlink")
	}
	return nil
}

func openSealedSegment(root string, seal observationSeal, segment observationSealedSegment) (*os.File, error) {
	file, err := openObservationRegular(filepath.Join(root, seal.Hour, segment.RelativePath))
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && info.Size() != segment.Bytes {
		err = errors.New("sealed observation input size changed")
	}
	if err == nil && segment.Bytes > 0 {
		var last [1]byte
		_, err = file.ReadAt(last[:], segment.Bytes-1)
		if err == nil && last[0] != '\n' {
			err = io.ErrUnexpectedEOF
		}
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func checkSealedInputs(root string, seal observationSeal, syncInputs bool) error {
	for _, stream := range seal.Streams {
		for _, segment := range stream.Segments {
			file, err := openSealedSegment(root, seal, segment)
			if err != nil {
				return err
			}
			if syncInputs {
				err = file.Sync()
			}
			if err = errors.Join(err, file.Close()); err != nil {
				return err
			}
		}
	}
	return nil
}

// atomicObservationJSON commits only after the complete marker and its rename
// are durable. Temp files have no protocol meaning after a process crash.
func atomicObservationJSON(path string, value any) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".handoff-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err = json.NewEncoder(file).Encode(value); err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// maintain is serialized with Flush/Close. Only the append frontier and handle
// detachment use append locks; syncing, sealing and receipt I/O do not.
func (l *ObservationLog) maintain(ctx context.Context) error {
	l.writerMu.Lock()
	l.mu.Lock()
	now := l.appendTimeLocked()
	current := now.Truncate(time.Hour)
	l.appendFloor = current
	var closed []*os.File
	for stream, file := range l.files {
		if filepath.Base(filepath.Dir(file.Name())) < observationHour(current) {
			closed = append(closed, file)
			delete(l.files, stream)
			delete(l.offsets, file)
		}
	}
	l.mu.Unlock()
	l.writerMu.Unlock()
	var result error
	for _, file := range closed {
		result = errors.Join(result, l.syncFileOutsideLock(file), file.Close())
	}
	if result != nil {
		l.recordError(result)
		return result
	}
	hours, err := l.hours()
	if err != nil {
		return err
	}
	deleting := true
	for _, hour := range hours {
		if !hour.Before(current) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		seal, err := l.sealHour(hour)
		if err != nil {
			l.recordError(err)
			result = errors.Join(result, err)
			deleting = false
			continue
		}
		var receipt observationUpload
		path := observationReceiptPath(filepath.Join(l.config.Directory, "archive-control"), hour)
		err = readControlJSON(path, &receipt)
		if errors.Is(err, os.ErrNotExist) {
			deleting = false
			continue
		}
		if err == nil {
			err = validateObservationUpload(receipt, seal, l.config.Store)
		}
		if err != nil {
			result = errors.Join(result, err)
			deleting = false
			continue
		}
		deleting = deleting && !now.Before(hour.Add(l.config.Retention))
		if deleting {
			deleting, err = l.removeHour(hour)
			if err == nil && deleting {
				err = os.Remove(path)
				if errors.Is(err, os.ErrNotExist) {
					err = nil
				}
			}
			result = errors.Join(result, err)
		}
	}
	return result
}

func (l *ObservationLog) sealHour(hour time.Time) (observationSeal, error) {
	seal, err := readObservationSeal(l.root, hour)
	if err == nil {
		return seal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return seal, err
	}
	seal = observationSeal{SchemaVersion: observationSealSchema, Hour: observationHour(hour), Streams: make([]observationSealedStream, 0, len(observationStreams))}
	for _, kind := range observationStreams {
		stream := observationSealedStream{Kind: kind, Segments: []observationSealedSegment{}}
		files, err := l.hourFiles(hour, kind)
		if err != nil {
			return seal, err
		}
		for _, segment := range files {
			file, err := openObservationRegular(segment.path)
			if err != nil {
				return seal, err
			}
			info, err := file.Stat()
			if err = errors.Join(err, file.Close()); err != nil {
				return seal, err
			}
			stream.Segments = append(stream.Segments, observationSealedSegment{RelativePath: filepath.Base(segment.path), Bytes: info.Size()})
		}
		seal.Streams = append(seal.Streams, stream)
	}
	if err := checkSealedInputs(l.root, seal, true); err != nil {
		return seal, err
	}
	return seal, atomicObservationJSON(filepath.Join(l.root, seal.Hour, "seal.json"), seal)
}

func validateObservationUpload(receipt observationUpload, seal observationSeal, store string) error {
	if receipt.SchemaVersion != observationUploadSchema || receipt.Hour != seal.Hour || receipt.Objects == nil || receipt.AbsentStreams == nil {
		return errors.New("invalid observation upload receipt header")
	}
	present := map[string]bool{}
	for _, stream := range seal.Streams {
		present[stream.Kind] = len(stream.Segments) != 0
	}
	seen := map[string]bool{}
	for _, object := range receipt.Objects {
		if !present[object.Stream] || seen[object.Stream] {
			return errors.New("unexpected or duplicate uploaded stream")
		}
		if err := object.Object.Validate(); err != nil {
			return err
		}
		if object.Object.Location() != observationLocation(store, seal.Hour, object.Stream) {
			return errors.New("uploaded object is outside observation ownership")
		}
		if object.Outcome != evidencestore.CreateOutcomeCreated && object.Outcome != evidencestore.CreateOutcomeExisting {
			return errors.New("invalid observation upload outcome")
		}
		seen[object.Stream] = true
	}
	for _, stream := range receipt.AbsentStreams {
		if _, ok := present[stream]; !ok || present[stream] || seen[stream] {
			return errors.New("unexpected or duplicate absent stream")
		}
		seen[stream] = true
	}
	if len(seen) != len(observationStreams) {
		return fmt.Errorf("incomplete observation upload receipt for %s", seal.Hour)
	}
	return nil
}
