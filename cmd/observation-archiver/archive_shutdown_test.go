package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func TestWorkerSignalCancelsUploadAndJoinsAbort(t *testing.T) {
	entered := make(chan struct{})
	var aborted atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w,
				"<InitiateMultipartUploadResult><UploadId>upload</UploadId></InitiateMultipartUploadResult>")
		case r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			<-r.Context().Done()
		case r.Method == http.MethodDelete:
			aborted.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected archive request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer receiver.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	directory := t.TempDir()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour).Format("20060102T15")
	hourDirectory := filepath.Join(directory, "v1alpha5", hour)
	if err := os.MkdirAll(hourDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(hourDirectory, "scheduler.00.jsonl")
	if err := os.WriteFile(rawPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: directory, Store: "evidence", Retention: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close(context.Background()) }()
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = run(ctx, options{
			directory: directory, controlDirectory: filepath.Join(directory, "archive-control"),
			store: stores3.Config{
				LogicalStore: "evidence", Bucket: "bucket", Region: "region", Endpoint: receiver.URL,
				RequestTimeout: time.Second,
			},
			shutdownTimeout: 2 * time.Minute,
		})
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("worker stopped before upload: %v", runErr)
	case <-time.After(2 * time.Second):
		t.Fatal("real worker wrapper did not start multipart upload")
	}
	cancel()
	select {
	case <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not cancel upload and join bounded abort")
	}
	if aborted.Load() != 1 {
		t.Fatalf("worker returned without joining multipart abort: %d", aborted.Load())
	}
	if _, err := os.Stat(rawPath); err != nil {
		t.Fatalf("cancellation lost raw input: %v", err)
	}
	receipt := filepath.Join(directory, "archive-control", hour+".uploaded.json")
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled upload published completion receipt: %v", err)
	}
}
