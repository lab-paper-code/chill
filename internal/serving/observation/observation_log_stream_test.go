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
				archive.cancel = cancel
				archive.cause = context.Canceled
			}
			log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
			seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
			clock.set(observationTestHour.Add(3 * time.Hour))
			if err := log.Flush(ctx); !errors.Is(err, archive.cause) {
				t.Fatalf("failure lost: %v", err)
			}
			if log.archived[observationTestHour] {
				t.Fatal("failed hour marked archived")
			}
			if _, err := os.Stat(filepath.Join(log.root, observationHour(observationTestHour))); err != nil {
				t.Fatalf("raw hour removed: %v", err)
			}
		})
	}
}
