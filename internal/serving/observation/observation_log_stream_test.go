package observation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type interruptedArchive struct {
	cancel context.CancelFunc
	cause  error
}

func (a *interruptedArchive) Write([]byte) (int, error) {
	if a.cancel != nil {
		a.cancel()
	}
	return 0, a.cause
}
func (a *interruptedArchive) StreamImmutable(_ context.Context, _ evidencestore.ObjectLocation, produce func(io.Writer) error) (evidencestore.Receipt, error) {
	return evidencestore.Receipt{}, produce(a)
}

func TestObservationStreamingFailureRetainsHour(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "upload", true: "cancel"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			archive := &interruptedArchive{cause: errors.New("part upload failed")}
			if cancelled {
				archive.cancel, archive.cause = cancel, context.Canceled
			}
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
			clock.set(observationTestHour.Add(3 * time.Hour))
			if err := log.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			worker, err := observationTestWorker(log, archive)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Scan(ctx); !errors.Is(err, archive.cause) {
				t.Fatalf("failure lost: %v", err)
			}
			if _, err := os.Stat(observationReceiptPath(worker.config.ControlDirectory, observationTestHour)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed hour marked uploaded", err)
			}
			if err := log.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(log.root, observationHour(observationTestHour))); err != nil {
				t.Fatalf("raw hour removed: %v", err)
			}
			if !log.Health().Healthy {
				t.Fatal("worker outage poisoned serving health")
			}
		})
	}
}

func TestObservationCanceledCloseRetainsHourForNextProcess(t *testing.T) {
	archive := &observationArchive{directory: t.TempDir()}
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Archive: archive, Retention: 2 * time.Hour}
	log, clock := clockedObservationLog(t, config, observationTestHour)
	seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(3 * time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := log.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close did not propagate shutdown cancellation: %v", err)
	}
	hourDir := filepath.Join(log.root, observationHour(observationTestHour))
	if files, err := os.ReadDir(hourDir); err != nil || len(files) == 0 {
		t.Fatalf("shutdown removed raw hour: files=%v error=%v", files, err)
	}
	if len(archive.outcomes) != 0 {
		t.Fatal("serving shutdown published an archive")
	}
	restarted, _ := clockedObservationLog(t, config, observationTestHour.Add(3*time.Hour))
	if err := flushObservationArchive(restarted, context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(archive.outcomes) == 0 {
		t.Fatal("replacement worker did not commit the retained hour")
	}
	if _, err := os.Stat(hourDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed expired hour not removed: %v", err)
	}
}
