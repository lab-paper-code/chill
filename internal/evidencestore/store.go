package evidencestore

import (
	"context"
	"io"
)

// ImmutableWriter creates an object without replacing existing bytes. Before
// returning success, an implementation must verify that content exactly
// matches the expected size and digest. A mismatch returns ErrContentMismatch
// and must not leave a newly created object. An exact pre-existing object is
// reported as CreateOutcomeExistingExactMatch and a different one as
// ErrPrecondition.
type ImmutableWriter interface {
	PutImmutable(context.Context, ObjectIdentity, io.Reader) (Receipt, error)
}

// Reader opens an object for independent verification. The caller owns and
// must close the returned stream.
type Reader interface {
	Open(context.Context, ObjectIdentity) (io.ReadCloser, error)
}

// LocationReader opens bytes when only a deterministic object location is
// known, such as recovery after an immutable write but before status was
// persisted. The returned bytes are untrusted until the caller derives and
// validates their content identity.
type LocationReader interface {
	OpenLocation(context.Context, ObjectLocation) (io.ReadCloser, error)
}
