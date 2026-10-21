package serving

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// observationLockRetry is how often a waiting owner retries the lock.
const observationLockRetry = time.Second

// AcquireObservationOwnership holds exclusive process ownership of an owner's
// observation log directory. The owner retains the returned file until shutdown.
// While another process holds the directory, as a terminating Pod does during
// its drain, it retries until that owner releases it or ctx ends; waiting is
// called once when the wait starts.
func AcquireObservationOwnership(ctx context.Context, directory string, waiting func()) (*os.File, error) {
	if directory == "" {
		return nil, errors.New("observation directory is required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, ".owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	retry := time.NewTicker(observationLockRetry)
	defer retry.Stop()
	for reported := false; ; reported = true {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(fmt.Errorf("observation directory lock unavailable: %w", err), file.Close())
		}
		if !reported && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(fmt.Errorf("observation directory still owned: %w", ctx.Err()), file.Close())
		case <-retry.C:
		}
	}
}
