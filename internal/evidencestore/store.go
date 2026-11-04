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

// StreamingImmutableWriter creates unknown-size content without staging a full
// object. produce must return all generation errors; success commits atomically.
type StreamingImmutableWriter interface {
	StreamImmutable(context.Context, ObjectLocation, func(io.Writer) error) (Receipt, error)
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

// Lister returns every object identity under a key prefix of one store.
type Lister interface {
	List(context.Context, string) ([]ObjectIdentity, error)
}

// Statter reads object metadata without downloading scientific input bytes.
type Statter interface {
	Stat(context.Context, ObjectLocation) (ObjectIdentity, error)
}
