package serving

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireObservationOwnership holds exclusive process ownership of an owner's
// observation log directory. The owner retains the returned file until shutdown.
// While another process holds the directory, as a terminating Pod does during
// its drain, it blocks until that owner releases it.
func AcquireObservationOwnership(directory string) (*os.File, error) {
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(fmt.Errorf("observation directory lock unavailable: %w", err), file.Close())
	}
	return file, nil
}
