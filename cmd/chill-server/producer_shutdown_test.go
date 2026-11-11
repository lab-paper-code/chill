package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func TestPublicShutdownRejectsNewWorkAndJoinsCanceledHandler(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	handler := &joiningHandler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
		w.WriteHeader(http.StatusAccepted)
		close(completed)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := httptest.NewRecorder()
	requestDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx))
		close(requestDone)
	}()
	<-entered
	handler.stop()
	cancel()
	<-canceled
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("new work accepted during shutdown: %d", rejected.Code)
	}
	joined := make(chan struct{})
	go func() { handler.wait(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("canceled handler was not joined through its final response")
	default:
	}
	close(release)
	<-joined
	<-requestDone
	select {
	case <-completed:
	default:
		t.Fatal("producer join omitted the final response")
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("accepted response lost during shutdown: %d", response.Code)
	}
}

func TestArchiveJoinWaitsForCanceledUploadCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	running := make(chan struct{}, 1)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	archiveInBackground(ctx, running, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	})
	<-entered
	cancel()
	<-canceled
	joined := make(chan struct{})
	go func() { running <- struct{}{}; close(joined) }()
	select {
	case <-joined:
		t.Fatal("archive join returned before canceled upload cleanup")
	default:
	}
	close(release)
	<-joined
}

func TestLogCleanCloseDoesNotEraseHTTPDrainFailure(t *testing.T) {
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	defer server.Close()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, _ := server.Client().Get(server.URL)
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	drainErr := shutdown(ctx, server.Config, execution)
	close(release)
	<-clientDone
	execution.JoinExecutions()
	closeErr := log.Close(context.Background())
	if closeErr != nil {
		t.Fatalf("local recording close failed: %v", closeErr)
	}
	if err := errors.Join(drainErr, closeErr); !errors.Is(err, context.Canceled) {
		t.Fatalf("successful recording close erased failed HTTP drain: %v", err)
	}
}

func TestListenerExitClosesHijackedLiveSocketBeforeHandlerJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close(context.Background()) }()
	execution, err := serving.NewRuntime(ctx, serving.RuntimeConfig{ObservationLog: log})
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
	hub := live.New(time.Now().UTC())
	defer hub.Close()
	finished := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/close-listener" {
			_ = r.Context().Value(http.ServerContextKey).(*http.Server).Close()
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		defer close(finished)
		// Like the live API, this hijacked handler belongs to the live hub,
		// not to HTTP Shutdown's ordinary connection accounting.
		<-hub.Done()
	})
	initialized := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, options{
			bindAddress: address, tlsCert: cert, tlsKey: key,
			requestTimeout: time.Second, drainTimeout: time.Second,
			sourceInterval: time.Hour, profileInterval: time.Hour,
		}, &initializingHandler{initial: handler, ready: make(chan struct{}), live: hub},
			execution, nil, nil, log, nil, nil,
			func(context.Context) (http.Handler, error) {
				close(initialized)
				return handler, nil
			})
	}()
	<-initialized
	socketCtx, stopSocket := context.WithTimeout(ctx, 5*time.Second)
	defer stopSocket()
	conn, _, err := websocket.Dial(socketCtx, "wss://"+address+"/api/v1/live",
		&websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	response, _ := client.Get("https://" + address + "/close-listener")
	if response != nil {
		_ = response.Body.Close()
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-socketCtx.Done():
		t.Fatal("listener-error shutdown did not join the hijacked live handler")
	}
	select {
	case <-finished:
	default:
		t.Fatal("serve returned before the live handler finished")
	}
	if ctx.Err() != nil {
		t.Fatal("listener-error shutdown required parent cancellation")
	}
}
