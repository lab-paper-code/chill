package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

type shutdownArchive struct {
	entered chan struct{}
	once    sync.Once
}

func (a *shutdownArchive) StreamImmutable(
	ctx context.Context, _ evidencestore.ObjectLocation, _ func(io.Writer) error,
) (evidencestore.Receipt, error) {
	a.once.Do(func() { close(a.entered) })
	<-ctx.Done()
	return evidencestore.Receipt{}, ctx.Err()
}

// Uses the actual one-minute serve ticker and closes the HTTP server from a
// request, so serverErrors fires while its parent/signal context is still live.
func TestServeListenerErrorCancelsArchiveBeforeJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := t.TempDir()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	hourDir := filepath.Join(directory, "v1alpha5", hour.Format("20060102T15"))
	if err := os.MkdirAll(hourDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hourDir, "scheduler.00.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := &shutdownArchive{entered: make(chan struct{})}
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: directory, Store: "evidence", Writer: archive, Retention: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = log.Close(ctx) }()
	execution, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	client, cert, key := coldStartTLS(t)
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server := r.Context().Value(http.ServerContextKey).(*http.Server)
		_ = server.Close()
	})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, options{
			bindAddress: address, tlsCert: cert, tlsKey: key,
			requestTimeout: time.Second, drainTimeout: time.Second,
			sourceInterval: time.Hour, profileInterval: time.Hour,
		}, &initializingHandler{initial: handler, ready: make(chan struct{})},
			execution, nil, nil, log, nil, nil,
			func(context.Context) (http.Handler, error) { return handler, nil })
	}()
	select {
	case <-archive.entered:
	case <-time.After(70 * time.Second):
		t.Fatal("actual serve archive ticker did not start Flush")
	}
	// Server.Close deliberately drops this request; that transport error is expected.
	response, _ := client.Get("https://" + address + "/close-listener")
	if response != nil {
		_ = response.Body.Close()
	}
	started := time.Now()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serverErrors exit waited forever for Flush before canceling it")
	}
	if ctx.Err() != nil {
		t.Fatal("test canceled the signal parent instead of exercising serverErrors")
	}
	if _, err := os.Stat(hourDir); err != nil {
		t.Fatalf("shutdown lost raw hour: %v", err)
	}
	t.Logf("serverErrors -> archive cancel -> Flush join -> drain: %s; parent live; raw retained",
		time.Since(started))
}

func TestArchiveSignalCancellationUnblocksJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan struct{}, 1)
	entered := make(chan struct{})
	archiveInBackground(ctx, running, func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() })
	<-entered
	cancel()
	joined := make(chan struct{})
	go func() { running <- struct{}{}; close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("signal cancellation did not unblock archive join")
	}
}
