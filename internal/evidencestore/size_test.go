package evidencestore

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestValidateSize(t *testing.T) {
	content := []byte("immutable profiling evidence")
	for _, test := range []struct {
		name    string
		content []byte
		wantErr error
	}{
		{name: "exact", content: content},
		{name: "short", content: content[:len(content)-1], wantErr: ErrContentMismatch},
		{name: "long", content: append(append([]byte(nil), content...), '!'), wantErr: ErrContentMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSize(bytes.NewReader(test.content), int64(len(content)))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateSize() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateSizePreservesReadFailure(t *testing.T) {
	readFailure := errors.New("source read failed")
	content := []byte("immutable profiling evidence")
	reader := io.MultiReader(bytes.NewReader(content[:4]), errorReader{err: readFailure})

	err := ValidateSize(reader, int64(len(content)))
	if !errors.Is(err, readFailure) {
		t.Fatalf("ValidateSize() error = %v, want wrapped source error", err)
	}
	if errors.Is(err, ErrContentMismatch) {
		t.Fatalf("source read failure was misclassified as content mismatch: %v", err)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
