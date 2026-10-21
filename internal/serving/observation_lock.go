package serving

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// AcquireObservationOwnership holds exclusive process ownership of an owner's
// observation log directory. The owner retains the returned file until shutdown.
// While another process holds the directory, as a terminating Pod does during
// its drain, it blocks until that owner releases it and logs the wait start and
// the acquisition.
func AcquireObservationOwnership(directory string, log func(msg string, keysAndValues ...any)) (*os.File, error) {
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
	path := file.Name()
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		log("waiting for observation lock", "path", path)
		started := time.Now()
		if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err == nil {
			log("observation lock acquired", "path", path, "wait", time.Since(started).String())
		}
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("observation directory lock unavailable: %w", err), file.Close())
	}
	return file, nil
}
