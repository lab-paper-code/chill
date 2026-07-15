package evidencestore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestVerify(t *testing.T) {
	content := []byte("immutable profiling evidence")
	identity := identityFor(content)

	tests := []struct {
		name    string
		content []byte
		mutate  func(ObjectIdentity) ObjectIdentity
		wantErr error
	}{
		{name: "exact", content: content},
		{name: "short", content: content[:len(content)-1], wantErr: ErrContentMismatch},
		{name: "long", content: append(append([]byte(nil), content...), '!'), wantErr: ErrContentMismatch},
		{
			name:    "same size different digest",
			content: append([]byte{'X'}, content[1:]...),
			wantErr: ErrContentMismatch,
		},
		{
			name:    "invalid identity",
			content: content,
			mutate: func(identity ObjectIdentity) ObjectIdentity {
				identity.Digest = "not-a-digest"
				return identity
			},
			wantErr: ErrInvalidIdentity,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := identity
			if test.mutate != nil {
				expected = test.mutate(expected)
			}
			err := Verify(bytes.NewReader(test.content), expected)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Verify() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestVerifyPreservesReadFailure(t *testing.T) {
	readFailure := errors.New("source read failed")
	content := []byte("immutable profiling evidence")
	reader := io.MultiReader(bytes.NewReader(content[:4]), errorReader{err: readFailure})

	err := Verify(reader, identityFor(content))
	if !errors.Is(err, readFailure) {
		t.Fatalf("Verify() error = %v, want wrapped source error", err)
	}
	if errors.Is(err, ErrContentMismatch) {
		t.Fatalf("source read failure was misclassified as content mismatch: %v", err)
	}
}

func identityFor(content []byte) ObjectIdentity {
	sum := sha256.Sum256(content)
	return ObjectIdentity{
		Store:  "profiling-evidence",
		Key:    "runs/run-1.json",
		Digest: fmt.Sprintf("sha256:%x", sum),
		Size:   int64(len(content)),
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
