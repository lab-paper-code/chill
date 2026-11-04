package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func TestMultipartRequestTimeoutIncludesSDKRetries(t *testing.T) {
	const create = "create"
	backoff := retryBackoff
	retryBackoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 50 * time.Millisecond, nil })
	defer func() { retryBackoff = backoff }()
	for _, operation := range []string{create, "part", "complete"} {
		t.Run(operation, func(t *testing.T) {
			var attempts, aborts atomic.Int64
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := "complete"
				if r.URL.Query().Has("uploads") {
					current = create
				} else if r.Method == http.MethodPut {
					current = "part"
				}
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Method == http.MethodDelete {
					aborts.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if current == operation {
					attempts.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, "<Error><Code>InternalError</Code></Error>")
					return
				}
				if current == create {
					_, _ = io.WriteString(w, "<InitiateMultipartUploadResult><UploadId>upload</UploadId></InitiateMultipartUploadResult>")
					return
				}
				w.Header().Set("ETag", "\"part\"")
			}))
			defer receiver.Close()
			const timeout = 250 * time.Millisecond
			store, err := New(Config{LogicalStore: "evidence", Bucket: "bucket", Endpoint: receiver.URL, Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test", RequestTimeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			_, err = store.StreamImmutable(context.Background(), evidencestore.ObjectLocation{Store: "evidence", Key: "hour.gz"}, func(w io.Writer) error { _, err := io.WriteString(w, "archive bytes"); return err })
			elapsed := time.Since(started)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline not propagated: %v", err)
			}
			if attempts.Load() < 2 || attempts.Load() >= storeRetryAttempts || elapsed > time.Second {
				t.Fatalf("timeout reset per HTTP attempt: attempts=%d elapsed=%s", attempts.Load(), elapsed)
			}
			wantAborts := int64(1)
			if operation == create {
				wantAborts = 0
			}
			if aborts.Load() != wantAborts {
				t.Fatalf("aborts=%d want=%d", aborts.Load(), wantAborts)
			}
			t.Logf("%s SDK call including %d HTTP attempts stopped after %s (timeout %s)", operation, attempts.Load(), elapsed, timeout)
		})
	}
}
