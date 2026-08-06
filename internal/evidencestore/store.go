package evidencestore

import (
	"context"
	"io"
)

// ImmutableWriter creates an object without replacing existing content. Before
// returning success, an implementation must enforce the declared size. A size
// mismatch returns ErrContentMismatch and must not leave a newly created
// object.
type ImmutableWriter interface {
	PutImmutable(context.Context, ObjectIdentity, io.Reader) (Receipt, error)
}

// Reader opens an object for independent verification. The caller owns and
// must close the returned stream.
type Reader interface {
	Open(context.Context, ObjectIdentity) (io.ReadCloser, error)
}

// LocationReader opens content when only a deterministic object location is
// known, such as recovery after an immutable write but before status was
// persisted. Callers remain responsible for bounded structural decoding.
type LocationReader interface {
	OpenLocation(context.Context, ObjectLocation) (io.ReadCloser, error)
}
