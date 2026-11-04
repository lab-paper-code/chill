package s3

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type multipartClient struct {
	fakeClient
	digest             hash.Hash
	sizes              []int64
	aborted, completed bool
	failPart           int
	existing           bool
	conflict           bool
}

func (c *multipartClient) CreateMultipartUpload(context.Context, *awss3.CreateMultipartUploadInput, ...func(*awss3.Options)) (*awss3.CreateMultipartUploadOutput, error) {
	return &awss3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
}
func (c *multipartClient) UploadPart(ctx context.Context, in *awss3.UploadPartInput, _ ...func(*awss3.Options)) (*awss3.UploadPartOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int(aws.ToInt32(in.PartNumber)) == c.failPart {
		return nil, errors.New("upload failed")
	}
	n, err := io.Copy(c.digest, in.Body)
	if err != nil {
		return nil, err
	}
	c.sizes = append(c.sizes, n)
	return &awss3.UploadPartOutput{ETag: aws.String(fmt.Sprint(in.PartNumber))}, nil
}
func (c *multipartClient) CompleteMultipartUpload(_ context.Context, in *awss3.CompleteMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error) {
	if aws.ToString(in.IfNoneMatch) != "*" {
		return nil, errors.New("missing immutable condition")
	}
	if c.existing {
		return nil, &smithy.GenericAPIError{Code: preconditionFailedCode}
	}
	if c.conflict {
		return nil, &smithy.GenericAPIError{Code: "ConditionalRequestConflict"}
	}
	c.completed = true
	return &awss3.CompleteMultipartUploadOutput{}, nil
}
func (c *multipartClient) AbortMultipartUpload(ctx context.Context, _ *awss3.AbortMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.AbortMultipartUploadOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.aborted = true
	return &awss3.AbortMultipartUploadOutput{}, nil
}

func TestMultipartStreaming(t *testing.T) {
	const created = "created"
	for _, mode := range []string{created, "existing", "conflict", "part failure", "producer failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &multipartClient{digest: sha256.New(), existing: mode == "existing", conflict: mode == "conflict"}
			if mode == "part failure" {
				c.failPart = 2
			}
			s := &Store{client: c, logicalStore: "evidence", bucket: "bucket", tempDir: "/does-not-exist"}
			data := bytes.Repeat([]byte("x"), multipartPartSize+17)
			receipt, err := s.StreamImmutable(ctx, evidencestore.ObjectLocation{Store: "evidence", Key: "hour.gz"}, func(w io.Writer) error {
				if _, err := w.Write(data); err != nil {
					return err
				}
				if mode == "producer failure" {
					return errors.New("producer failed")
				}
				if mode == "cancel" {
					cancel()
				}
				return nil
			})
			success := mode == created || mode == "existing"
			if (err == nil) != success {
				t.Fatalf("receipt=%+v error=%v", receipt, err)
			}
			if c.completed != (mode == created) || c.aborted != (mode != created) {
				t.Fatalf("completed=%v aborted=%v", c.completed, c.aborted)
			}
			if success {
				expected := sha256.Sum256(data)
				if !bytes.Equal(c.digest.Sum(nil), expected[:]) || receipt.Object.Size != int64(len(data)) {
					t.Fatal("uploaded content differs")
				}
				if len(c.sizes) != 2 || c.sizes[0] != multipartPartSize || c.sizes[1] != 17 {
					t.Fatalf("parts=%v", c.sizes)
				}
			}
		})
	}
}

func TestMultipartPartLimit(t *testing.T) {
	c := &multipartClient{digest: sha256.New()}
	w := &multipartWriter{ctx: context.Background(), store: &Store{client: c}, parts: make([]types.CompletedPart, 10000)}
	if err := w.flush(); !errors.Is(err, evidencestore.ErrContentMismatch) {
		t.Fatalf("part limit error = %v", err)
	}
	if len(c.sizes) != 0 {
		t.Fatal("sent part beyond protocol limit")
	}
}

// Opt-in, one-shot T04-scale replay re-gzips a downloaded real hour.
// CHILL_ARCHIVE_HOUR points outside the repo; no output copy is staged.
func TestMultipartRealHourScale(t *testing.T) {
	path := os.Getenv("CHILL_ARCHIVE_HOUR")
	if path == "" {
		t.Skip("set CHILL_ARCHIVE_HOUR to a real archived gzip")
	}
	produce := func(w io.Writer) (err error) {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		input, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, input.Close()) }()
		z := gzip.NewWriter(w)
		_, err = io.Copy(z, input)
		return errors.Join(err, z.Close())
	}
	baseline := sha256.New()
	if err := produce(baseline); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	c := &multipartClient{digest: sha256.New()}
	var peakHeap uint64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w, "<InitiateMultipartUploadResult><UploadId>upload</UploadId></InitiateMultipartUploadResult>")
		case r.Method == http.MethodPut:
			n, err := io.Copy(c.digest, r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			c.sizes = append(c.sizes, n)
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			peakHeap = max(peakHeap, memory.HeapAlloc)
			w.Header().Set("ETag", "\"part\"")
		case r.Method == http.MethodPost:
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing immutable condition")
			}
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><ETag>complete</ETag></CompleteMultipartUploadResult>")
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	s, err := New(Config{Endpoint: server.URL, Region: "us-east-1", LogicalStore: "evidence", Bucket: "bucket", AccessKeyID: "test", SecretAccessKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	receipt, err := s.StreamImmutable(context.Background(), evidencestore.ObjectLocation{Store: "evidence", Key: "hour.gz"}, produce)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(baseline.Sum(nil), c.digest.Sum(nil)) {
		t.Fatal("gzip differs from old gzip settings")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch entries=%v error=%v", entries, err)
	}
	if receipt.Object.Size < 487<<20 {
		t.Fatalf("replay too small: %d", receipt.Object.Size)
	}
	t.Logf("gzip=%d parts=%d payload-buffer=%d heap-growth=%d sampled-peak-heap-growth=%d total-alloc=%d scratch-bytes=0 sha256=%x", receipt.Object.Size, len(c.sizes), multipartPartSize, after.HeapAlloc-before.HeapAlloc, peakHeap-before.HeapAlloc, after.TotalAlloc-before.TotalAlloc, c.digest.Sum(nil))
}
