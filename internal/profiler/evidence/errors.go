package evidence

import (
	"errors"
	"fmt"
)

// ErrPermanentValidation marks evidence whose immutable semantic inputs cannot
// become valid by retrying finalization for the same ProfilingRun.
var ErrPermanentValidation = errors.New("permanent evidence validation failure")

func permanentValidationError(err error) error {
	if err == nil {
		panic("permanent validation error is required")
	}
	return fmt.Errorf("%w: %w", ErrPermanentValidation, err)
}

func permanentValidationf(format string, args ...any) error {
	return permanentValidationError(fmt.Errorf(format, args...))
}
