package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

// A listener failure must stop local maintenance without publishing or deleting
// raw hours, even while the parent/signal context is still live.
func TestServeListenerErrorRetainsUnuploadedHours(t *testing.T) {
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
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: directory, Store: "evidence", Retention: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close(context.Background()) }()
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
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
	initialized := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, options{
			bindAddress: address, tlsCert: cert, tlsKey: key,
			requestTimeout: time.Second, drainTimeout: time.Second,
			sourceInterval: time.Hour, profileInterval: time.Hour,
		}, &initializingHandler{initial: handler, ready: make(chan struct{})},
			execution, nil, nil, log, nil, nil,
			func(context.Context) (http.Handler, error) {
				close(initialized)
				return handler, nil
			})
	}()
	select {
	case <-initialized:
	case err := <-done:
		t.Fatalf("serve exited before initialization: %v", err)
	case <-time.After(time.Second):
		t.Fatal("serve did not initialize")
	}
	// Server.Close deliberately drops this request; that transport error is expected.
	response, _ := client.Get("https://" + address + "/close-listener")
	if response != nil {
		_ = response.Body.Close()
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serverErrors exit did not stop local maintenance and drain")
	}
	if ctx.Err() != nil {
		t.Fatal("test canceled the signal parent instead of exercising serverErrors")
	}
	if _, err := os.Stat(filepath.Join(hourDir, "seal.json")); err != nil {
		t.Fatalf("local maintenance did not seal the completed hour: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hourDir, "scheduler.00.jsonl")); err != nil {
		t.Fatalf("shutdown lost unuploaded raw hour: %v", err)
	}
}

func TestMaintenanceSignalCancellationUnblocksJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan struct{}, 1)
	entered := make(chan struct{})
	maintainInBackground(ctx, running, func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() })
	<-entered
	cancel()
	joined := make(chan struct{})
	go func() { running <- struct{}{}; close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("signal cancellation did not unblock maintenance join")
	}
}
