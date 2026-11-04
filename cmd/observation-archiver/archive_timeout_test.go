package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
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

func TestWorkerUploadExceedsSingleRequestTimeout(t *testing.T) {
	data := make([]byte, 16<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\"blob\":\"" + base64.StdEncoding.EncodeToString(data) + "\"}\n")
	exerciseWorkerUpload(t, bytes.NewReader(raw), 2*time.Second, 600*time.Millisecond)
}

// Opt-in real T04 hour through the worker composition root, not Store directly.
func TestWorkerUploadRealT04Hour(t *testing.T) {
	path := os.Getenv("CHILL_ARCHIVE_HOUR")
	if path == "" {
		t.Skip("set CHILL_ARCHIVE_HOUR to a downloaded real gzip")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	exerciseWorkerUpload(t, reader, time.Second, 30*time.Millisecond)
}

func exerciseWorkerUpload(t *testing.T, raw io.Reader, requestTimeout, partDelay time.Duration) {
	t.Helper()
	var aborted, completed, parts, uploaded atomic.Int64
	var longest atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w,
				"<InitiateMultipartUploadResult><UploadId>upload</UploadId></InitiateMultipartUploadResult>")
		case r.Method == http.MethodPut:
			started := time.Now()
			n, err := io.Copy(io.Discard, r.Body)
			if err != nil {
				return
			}
			select {
			case <-time.After(partDelay):
			case <-r.Context().Done():
				return
			}
			elapsed := time.Since(started).Nanoseconds()
			for old := longest.Load(); elapsed > old; old = longest.Load() {
				if longest.CompareAndSwap(old, elapsed) {
					break
				}
			}
			parts.Add(1)
			uploaded.Add(n)
			w.Header().Set("ETag", "\"part\"")
		case r.Method == http.MethodPost:
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("immutable condition missing")
			}
			_, _ = io.Copy(io.Discard, r.Body)
			completed.Add(1)
			_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><ETag>complete</ETag></CompleteMultipartUploadResult>")
		case r.Method == http.MethodDelete:
			aborted.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s", r.Method)
			w.WriteHeader(400)
		}
	}))
	defer receiver.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	config := stores3.Config{
		LogicalStore: "evidence", Bucket: "bucket", Region: "us-east-1",
		Endpoint: receiver.URL, RequestTimeout: requestTimeout,
		AccessKeyID: "test", SecretAccessKey: "test",
	}
	objects, err := stores3.New(config)
	if err != nil {
		t.Fatal(err)
	}
	directory, hourDir, log := prepareWorkerHour(t, raw, objects)
	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now()
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = run(ctx, options{
			directory: directory, controlDirectory: filepath.Join(directory, "archive-control"),
			store: config, shutdownTimeout: 2 * time.Minute,
		})
	}()
	defer func() {
		cancel()
		<-done
		_ = log.Close(context.Background())
	}()
	receipt := filepath.Join(directory, "archive-control", filepath.Base(hourDir)+".uploaded.json")
	poll := time.NewTicker(partDelay)
	defer poll.Stop()
	// This watchdog is a regression-test bound, never a whole-upload deadline
	// supplied by the production composition root.
	watchdog := time.NewTimer(2 * time.Minute)
	defer watchdog.Stop()
waitReceipt:
	for {
		select {
		case <-done:
			t.Fatalf("worker stopped before completion receipt: %v", runErr)
		case <-watchdog.C:
			cancel()
			<-done
			t.Fatalf("worker receipt missing: completed=%d aborted=%d error=%v",
				completed.Load(), aborted.Load(), runErr)
		case <-poll.C:
			if _, err := os.Stat(receipt); err == nil {
				break waitReceipt
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	elapsed := time.Since(started)
	cancel()
	<-done
	if runErr != nil {
		t.Fatal(runErr)
	}
	if completed.Load() != 2 || aborted.Load() != 0 {
		t.Fatalf("completed=%d aborted=%d", completed.Load(), aborted.Load())
	}
	if elapsed <= requestTimeout || time.Duration(longest.Load()) >= requestTimeout {
		t.Fatalf("wrong boundary: elapsed=%s longestPart=%s requestTimeout=%s",
			elapsed, time.Duration(longest.Load()), requestTimeout)
	}
	if _, err := os.Stat(hourDir); err != nil {
		t.Fatalf("worker retired serving-owned raw hour: %v", err)
	}
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hourDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed retained hour: %v", err)
	}
	t.Logf("worker sealed scan -> multipart -> receipt -> retirement: "+
		"bytes=%d parts=%d elapsed=%s timeout=%s longestPart=%s aborts=%d",
		uploaded.Load(), parts.Load(), elapsed, requestTimeout, time.Duration(longest.Load()), aborted.Load())
}

func prepareWorkerHour(t *testing.T, raw io.Reader,
	objects *stores3.Store,
) (string, string, *observation.ObservationLog) {
	t.Helper()
	directory := t.TempDir()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	hourDir := filepath.Join(directory, "v1alpha5", hour.Format("20060102T15"))
	if err := os.MkdirAll(hourDir, 0700); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(hourDir, "catalog.00.jsonl")
	if err := os.WriteFile(catalogPath, []byte("{\"catalog\":\"retained\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(hourDir, "scheduler.00.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(file, raw)
	if err = errors.Join(err, file.Close()); err != nil {
		t.Fatal(err)
	}
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: directory, Store: "evidence", Archive: objects, Retention: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	return directory, hourDir, log
}
