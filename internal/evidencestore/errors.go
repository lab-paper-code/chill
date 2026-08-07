package evidencestore

import "errors"

var (
	// ErrInvalidIdentity indicates that an object identity is incomplete.
	ErrInvalidIdentity = errors.New("invalid evidence object identity")
	// ErrNotFound indicates that the referenced evidence object does not exist.
	ErrNotFound = errors.New("evidence object not found")
	// ErrAuthentication indicates that the store rejected the configured
	// credentials.
	ErrAuthentication = errors.New("evidence store authentication failed")
	// ErrPrecondition indicates that an immutable-create condition was not met.
	ErrPrecondition = errors.New("evidence store precondition failed")
	// ErrTimeout indicates that a store operation exceeded its deadline.
	ErrTimeout = errors.New("evidence store operation timed out")
	// ErrTransport indicates that the store could not be reached or returned an
	// unusable transport response.
	ErrTransport = errors.New("evidence store transport failed")
	// ErrContentMismatch indicates that content violates a declared size boundary.
	ErrContentMismatch = errors.New("evidence content mismatch")
)
