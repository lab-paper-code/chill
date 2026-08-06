package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const testPhysicalBucket = "profiling-evidence-bucket"

func TestPutImmutableCreatesVerifiedCandidate(t *testing.T) {
	content := []byte("immutable profiling evidence")
	expected := identityFor(content)
	client := &fakeClient{
		put: func(_ context.Context, input *awss3.PutObjectInput) (*awss3.PutObjectOutput, error) {
			if got := stringValue(input.Bucket); got != testPhysicalBucket {
				t.Fatalf("bucket = %q, want %q", got, testPhysicalBucket)
			}
			if got := stringValue(input.Key); got != expected.Key {
				t.Fatalf("key = %q, want %q", got, expected.Key)
			}
			if got := stringValue(input.IfNoneMatch); got != "*" {
				t.Fatalf("IfNoneMatch = %q, want *", got)
			}
			if input.ContentLength == nil || *input.ContentLength != expected.Size {
				t.Fatalf("ContentLength = %v, want %d", input.ContentLength, expected.Size)
			}
			actual, err := io.ReadAll(input.Body)
			if err != nil {
				t.Fatalf("read PutObject body: %v", err)
			}
			if int64(len(actual)) != expected.Size {
				t.Fatalf("PutObject body size = %d, want %d", len(actual), expected.Size)
			}
			return &awss3.PutObjectOutput{}, nil
		},
	}
	store := testObjectStore(client, t.TempDir())

	receipt, err := store.PutImmutable(context.Background(), expected, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("PutImmutable() error = %v", err)
	}
	if receipt.Object != expected || receipt.Outcome != evidencestore.CreateOutcomeCreated {
		t.Fatalf("PutImmutable() receipt = %+v", receipt)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("PutImmutable() returned invalid receipt: %v", err)
	}
	assertDirectoryEmpty(t, store.tempDir)
}

func TestPutImmutableRejectsInvalidCandidateBeforeS3(t *testing.T) {
	content := []byte("immutable profiling evidence")
	expected := identityFor(content)
	sourceFailure := errors.New("candidate source failed")

	tests := []struct {
		name    string
		content io.Reader
		wantErr error
	}{
		{name: "nil", content: nil, wantErr: evidencestore.ErrContentMismatch},
		{name: "short", content: bytes.NewReader(content[:len(content)-1]), wantErr: evidencestore.ErrContentMismatch},
		{
			name: "long", content: bytes.NewReader(append(append([]byte(nil), content...), '!')),
			wantErr: evidencestore.ErrContentMismatch,
		},
		{
			name: "source failure", content: io.MultiReader(bytes.NewReader(content[:4]), errorReader{sourceFailure}),
			wantErr: sourceFailure,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			putCalls := 0
			tempDir := t.TempDir()
			store := testObjectStore(
				&fakeClient{put: func(context.Context, *awss3.PutObjectInput) (*awss3.PutObjectOutput, error) {
					putCalls++
					return &awss3.PutObjectOutput{}, nil
				}},
				tempDir,
			)

			_, err := store.PutImmutable(context.Background(), expected, test.content)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("PutImmutable() error = %v, want %v", err, test.wantErr)
			}
			if putCalls != 0 {
				t.Fatalf("PutObject called %d times for invalid candidate", putCalls)
			}
			assertDirectoryEmpty(t, tempDir)
		})
	}
}

func TestPutImmutableClassifiesExistingObject(t *testing.T) {
	content := []byte("immutable profiling evidence")
	expected := identityFor(content)
	precondition := &smithy.GenericAPIError{
		Code:    "PreconditionFailed",
		Message: "object exists",
		Fault:   smithy.FaultClient,
	}

	tests := []struct {
		name        string
		existing    []byte
		wantOutcome evidencestore.CreateOutcome
		wantErr     error
	}{
		{name: "existing object", existing: content, wantOutcome: evidencestore.CreateOutcomeExistingExactMatch},
		{name: "same-size object", existing: append([]byte{'X'}, content[1:]...), wantOutcome: evidencestore.CreateOutcomeExistingExactMatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := testObjectStore(
				&fakeClient{
					put: func(context.Context, *awss3.PutObjectInput) (*awss3.PutObjectOutput, error) {
						return nil, precondition
					},
					get: func(context.Context, *awss3.GetObjectInput) (*awss3.GetObjectOutput, error) {
						return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(test.existing))}, nil
					},
				},
				t.TempDir(),
			)

			receipt, err := store.PutImmutable(context.Background(), expected, bytes.NewReader(content))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("PutImmutable() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && (receipt.Object != expected || receipt.Outcome != test.wantOutcome) {
				t.Fatalf("PutImmutable() receipt = %+v", receipt)
			}
		})
	}
}

func TestOpenMapsOperationAndStreamErrors(t *testing.T) {
	expected := identityFor([]byte("immutable profiling evidence"))

	t.Run("location only", func(t *testing.T) {
		backend := &fakeClient{get: func(
			_ context.Context,
			input *awss3.GetObjectInput,
		) (*awss3.GetObjectOutput, error) {
			if stringValue(input.Bucket) != testPhysicalBucket || stringValue(input.Key) != expected.Key {
				t.Fatalf("GetObject location = %s/%s", stringValue(input.Bucket), stringValue(input.Key))
			}
			return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte("existing")))}, nil
		}}
		store := testObjectStore(backend, t.TempDir())
		body, err := store.OpenLocation(context.Background(), expected.Location())
		if err != nil {
			t.Fatal(err)
		}
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		backend := &fakeClient{get: func(
			context.Context,
			*awss3.GetObjectInput,
		) (*awss3.GetObjectOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing", Fault: smithy.FaultClient}
		}}
		store := testObjectStore(backend, t.TempDir())
		_, err := store.Open(context.Background(), expected)
		if !errors.Is(err, evidencestore.ErrNotFound) {
			t.Fatalf("Open() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("stream timeout", func(t *testing.T) {
		backend := &fakeClient{get: func(
			context.Context,
			*awss3.GetObjectInput,
		) (*awss3.GetObjectOutput, error) {
			return &awss3.GetObjectOutput{Body: &errorReadCloser{readErr: context.DeadlineExceeded}}, nil
		}}
		store := testObjectStore(backend, t.TempDir())
		body, err := store.Open(context.Background(), expected)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		_, err = body.Read(make([]byte, 1))
		if !errors.Is(err, evidencestore.ErrTimeout) {
			t.Fatalf("body.Read() error = %v, want ErrTimeout", err)
		}
	})

	t.Run("stream transport", func(t *testing.T) {
		transportFailure := errors.New("connection reset")
		backend := &fakeClient{get: func(
			context.Context,
			*awss3.GetObjectInput,
		) (*awss3.GetObjectOutput, error) {
			return &awss3.GetObjectOutput{Body: &errorReadCloser{readErr: transportFailure}}, nil
		}}
		store := testObjectStore(backend, t.TempDir())
		body, err := store.Open(context.Background(), expected)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		_, err = body.Read(make([]byte, 1))
		if !errors.Is(err, evidencestore.ErrTransport) {
			t.Fatalf("body.Read() error = %v, want ErrTransport", err)
		}
	})
}

func TestObjectOperationsRejectAnotherLogicalStoreBeforeS3(t *testing.T) {
	calls := 0
	client := &fakeClient{
		put: func(context.Context, *awss3.PutObjectInput) (*awss3.PutObjectOutput, error) {
			calls++
			return &awss3.PutObjectOutput{}, nil
		},
		get: func(context.Context, *awss3.GetObjectInput) (*awss3.GetObjectOutput, error) {
			calls++
			return &awss3.GetObjectOutput{}, nil
		},
	}
	store := testObjectStore(client, t.TempDir())
	payload := []byte("immutable profiling evidence")
	identity := identityFor(payload)
	identity.Store = "another-store"

	_, err := store.PutImmutable(context.Background(), identity, bytes.NewReader(payload))
	if !errors.Is(err, evidencestore.ErrInvalidIdentity) {
		t.Fatalf("PutImmutable() error = %v, want ErrInvalidIdentity", err)
	}
	_, err = store.OpenLocation(context.Background(), identity.Location())
	if !errors.Is(err, evidencestore.ErrInvalidIdentity) {
		t.Fatalf("OpenLocation() error = %v, want ErrInvalidIdentity", err)
	}
	if calls != 0 {
		t.Fatalf("S3 called %d times for a mismatched logical store", calls)
	}
}

func identityFor(content []byte) evidencestore.ObjectIdentity {
	sum := sha256.Sum256(content)
	return evidencestore.ObjectIdentity{
		Store:  "chill-evidence-store",
		Key:    "runs/run-1.json",
		Digest: fmt.Sprintf("sha256:%x", sum),
		Size:   int64(len(content)),
	}
}

func testObjectStore(backend client, tempDir string) *Store {
	return &Store{
		client:       backend,
		logicalStore: "chill-evidence-store",
		bucket:       testPhysicalBucket,
		tempDir:      tempDir,
	}
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary directory contains %d leaked files", len(entries))
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type fakeClient struct {
	put          func(context.Context, *awss3.PutObjectInput) (*awss3.PutObjectOutput, error)
	get          func(context.Context, *awss3.GetObjectInput) (*awss3.GetObjectOutput, error)
	headBucket   func(context.Context, *awss3.HeadBucketInput) (*awss3.HeadBucketOutput, error)
	createBucket func(context.Context, *awss3.CreateBucketInput) (*awss3.CreateBucketOutput, error)
}

func (c *fakeClient) HeadBucket(
	ctx context.Context,
	input *awss3.HeadBucketInput,
	_ ...func(*awss3.Options),
) (*awss3.HeadBucketOutput, error) {
	if c.headBucket == nil {
		return nil, errors.New("unexpected HeadBucket call")
	}
	return c.headBucket(ctx, input)
}

func (c *fakeClient) CreateBucket(
	ctx context.Context,
	input *awss3.CreateBucketInput,
	_ ...func(*awss3.Options),
) (*awss3.CreateBucketOutput, error) {
	if c.createBucket == nil {
		return nil, errors.New("unexpected CreateBucket call")
	}
	return c.createBucket(ctx, input)
}

func (c *fakeClient) PutObject(
	ctx context.Context,
	input *awss3.PutObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	if c.put == nil {
		return nil, errors.New("unexpected PutObject call")
	}
	return c.put(ctx, input)
}

func (c *fakeClient) GetObject(
	ctx context.Context,
	input *awss3.GetObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.GetObjectOutput, error) {
	if c.get == nil {
		return nil, errors.New("unexpected GetObject call")
	}
	return c.get(ctx, input)
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type errorReadCloser struct {
	readErr  error
	closeErr error
}

func (r *errorReadCloser) Read([]byte) (int, error) {
	return 0, r.readErr
}

func (r *errorReadCloser) Close() error {
	return r.closeErr
}
