package observation

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// ArchiveWorkerConfig separates read-only raw inputs from the worker's only
// writable mount. Directory is the serving owner directory, not its version root.
type ArchiveWorkerConfig struct {
	Directory        string
	ControlDirectory string
	Store            string
	Writer           evidencestore.StreamingImmutableWriter
}

// ArchiveWorker never constructs a recorder, repairs raw files, acquires the
// serving owner lock or retires an hour. Its scan and upload are synchronous.
type ArchiveWorker struct {
	config ArchiveWorkerConfig
	mu     sync.Mutex
}

func NewArchiveWorker(config ArchiveWorkerConfig) (*ArchiveWorker, error) {
	if config.Directory == "" || config.ControlDirectory == "" || config.Store == "" || config.Writer == nil {
		return nil, errors.New("archive raw directory, control directory, store and writer are required")
	}
	for _, path := range []string{filepath.Join(config.Directory, observationVersion), config.ControlDirectory} {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("archive directory required: %s", path)
		}
	}
	return &ArchiveWorker{config: config}, nil
}

// Run holds the distinct worker flock through cancellation and every joined
// upload/producer call. Scan errors are reported without stopping serving or
// reserving any append hour; the next scan retries immutable publication.
func (w *ArchiveWorker) Run(ctx context.Context, scanInterval time.Duration, report func(error)) error {
	if scanInterval <= 0 {
		return errors.New("archive scan interval must be positive")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	lock, err := w.acquireLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.scan(ctx); err != nil && report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Scan performs one complete pass with the same process ownership as Run.
func (w *ArchiveWorker) Scan(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	lock, err := w.acquireLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return w.scan(ctx)
}

func (w *ArchiveWorker) acquireLock() (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(w.config.ControlDirectory, ".archiver.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("observation archive worker lock unavailable: %w", err), file.Close())
	}
	return file, nil
}

func (w *ArchiveWorker) scan(ctx context.Context) error {
	root := filepath.Join(w.config.Directory, observationVersion)
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var hours []time.Time
	for _, entry := range entries {
		if hour, err := time.Parse(observationHourLayout, entry.Name()); err == nil && entry.IsDir() {
			hours = append(hours, hour)
		}
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Before(hours[j]) })
	var result error
	for _, hour := range hours {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		seal, err := readObservationSeal(root, hour)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			err = w.publish(ctx, root, hour, seal)
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("archive hour %s: %w", observationHour(hour), err))
		}
	}
	return result
}

func (w *ArchiveWorker) publish(ctx context.Context, root string, hour time.Time, seal observationSeal) error {
	path := observationReceiptPath(w.config.ControlDirectory, hour)
	var existing observationUpload
	if err := readControlJSON(path, &existing); err == nil {
		return validateObservationUpload(existing, seal, w.config.Store)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := checkSealedInputs(root, seal, false); err != nil {
		return err
	}
	receipt := observationUpload{SchemaVersion: observationUploadSchema, Hour: seal.Hour, Objects: []observationUploadedObject{}, AbsentStreams: []string{}}
	// References must be published before referring scheduler rows. Remaining
	// streams retain one deterministic, serial order; no upload backlog queue.
	for _, kind := range []string{observationCatalogStream, observationSchedulerStream, observationRequestStream, observationManagementStream, observationResourcesStream, observationClusterStream} {
		var stream observationSealedStream
		for _, candidate := range seal.Streams {
			if candidate.Kind == kind {
				stream = candidate
				break
			}
		}
		if len(stream.Segments) == 0 {
			receipt.AbsentStreams = append(receipt.AbsentStreams, kind)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		uploaded, err := w.upload(ctx, root, seal, stream)
		if err != nil {
			return err
		}
		receipt.Objects = append(receipt.Objects, observationUploadedObject{Stream: kind, Object: uploaded.Object, Outcome: uploaded.Outcome})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return atomicObservationJSON(path, receipt)
}

func (w *ArchiveWorker) upload(ctx context.Context, root string, seal observationSeal, stream observationSealedStream) (evidencestore.Receipt, error) {
	location := observationLocation(w.config.Store, seal.Hour, stream.Kind)
	receipt, err := w.config.Writer.StreamImmutable(ctx, location, func(output io.Writer) error {
		writer := gzip.NewWriter(output) // Zero header time preserves immutable bytes.
		for _, segment := range stream.Segments {
			if err := ctx.Err(); err != nil {
				return err
			}
			file, err := openSealedSegment(root, seal, segment)
			if err != nil {
				return err
			}
			_, err = io.CopyN(writer, file, segment.Bytes)
			// A changed size is a failed input, never a shorter committed object.
			info, statErr := file.Stat()
			if statErr == nil && info.Size() != segment.Bytes {
				statErr = errors.New("sealed observation input changed during upload")
			}
			if err = errors.Join(err, statErr, file.Close()); err != nil {
				return err
			}
		}
		return writer.Close()
	})
	if err != nil {
		return receipt, err
	}
	if err := receipt.Validate(); err != nil {
		return receipt, err
	}
	if receipt.Object.Location() != location {
		return receipt, errors.New("archive writer returned a different object location")
	}
	return receipt, nil
}
