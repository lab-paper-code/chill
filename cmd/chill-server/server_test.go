package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/application"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// A slow hourly upload runs beside the serve loop: each tick returns at once,
// and a tick during the upload starts no second one.
func TestArchiveInBackgroundRunsOneFlushAtATime(t *testing.T) {
	running := make(chan struct{}, 1)
	release := make(chan struct{})
	var started atomic.Int32
	flush := func(ctx context.Context) error {
		started.Add(1)
		<-release
		return ctx.Err()
	}
	for range 3 {
		ticked := make(chan struct{})
		go func() {
			archiveInBackground(context.Background(), running, flush)
			close(ticked)
		}()
		select {
		case <-ticked:
		case <-time.After(time.Second):
			t.Fatal("serve loop blocked on a running Flush")
		}
	}
	close(release)
	running <- struct{}{} // The serve loop waits here for the running Flush.
	if got := started.Load(); got != 1 {
		t.Fatalf("started %d Flushes, want 1", got)
	}
	<-running
	archiveInBackground(context.Background(), running, flush)
	running <- struct{}{}
	if got := started.Load(); got != 2 {
		t.Fatalf("Flush after the previous one finished: started %d, want 2", got)
	}
}

func TestServeDuringColdStart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "failed"}[fail], func(t *testing.T) {
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
			client, certFile, keyFile := coldStartTLS(t)
			reservation, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := reservation.Addr().String()
			_ = reservation.Close()
			initial := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/v1") {
					t.Error("partially initialized API was reached")
				}
				w.WriteHeader(http.StatusOK)
			})
			handler := &initializingHandler{initial: initial, ready: make(chan struct{})}
			release, started := make(chan struct{}), make(chan struct{})
			initializeFinished := make(chan struct{})
			initializeError := errors.New("fake cold-start failure")
			initialize := func(ctx context.Context) (http.Handler, error) {
				defer close(initializeFinished)
				close(started)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
				}
				if fail {
					return nil, initializeError
				}
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusAccepted)
				}), nil
			}
			done := make(chan error, 1)
			go func() {
				done <- serve(ctx, options{
					bindAddress: address, tlsCert: "/." + certFile, tlsKey: "/." + keyFile,
					requestTimeout: time.Second, drainTimeout: time.Second,
					sourceInterval: time.Hour, profileInterval: time.Hour,
				}, handler, execution, nil, nil, log, nil, nil, initialize)
			}()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if fail {
						if !errors.Is(err, initializeError) {
							t.Errorf("initialization failure did not terminate serve: %v", err)
						}
					} else if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("serve did not stop")
				}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("initialization did not start")
			}
			client.Timeout = time.Second
			t.Run("listener_open_while_initializing", func(t *testing.T) {
				for _, path := range []string{"/healthz", "/readyz", "/"} {
					response, err := client.Get("https://" + address + path)
					if err != nil {
						t.Fatal(err)
					}
					_ = response.Body.Close()
					if response.StatusCode != http.StatusOK {
						t.Fatalf("%s = %d", path, response.StatusCode)
					}
				}
			})
			t.Run("unprepared_requests_return_503", func(t *testing.T) {
				for _, route := range []string{
					"GET /api/v1/workloads", "POST /api/v1/workloads/a/infer",
					"PUT /api/v1/scheduler/settings", "POST /api/v1/scheduler/operations",
					"POST /api/v1/evaluations/preflight",
					"DELETE /api/v1/evaluations/a", "POST /api/v1/namespaces/ns/batch-energy-optimizations",
					"GET /api/v1/namespaces/ns/measurement-coverage", "POST /api/v1/internal/observations/resources",
				} {
					method, path, _ := strings.Cut(route, " ")
					request, _ := http.NewRequest(method, "https://"+address+path, nil)
					request.Header.Set("If-Match", `"startup-version"`)
					response, err := client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					var body struct{ Code string }
					err = json.NewDecoder(response.Body).Decode(&body)
					_ = response.Body.Close()
					if err != nil || response.StatusCode != http.StatusServiceUnavailable ||
						body.Code != "CoverageWarming" || response.Header.Get("Retry-After") != "2" {
						t.Fatalf("%s: status=%d body=%+v err=%v", route, response.StatusCode, body, err)
					}
				}
			})
			close(release)
			<-initializeFinished
			if fail {
				return
			}
			select {
			case <-handler.ready:
			case <-time.After(5 * time.Second):
				t.Fatal("initialization did not finish")
			}
			t.Run("prepared_requests_reach_handler", func(t *testing.T) {
				response, err := client.Post("https://"+address+"/api/v1/workloads/a/infer", "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					t.Fatalf("prepared request = %d", response.StatusCode)
				}
			})
		})
	}
}

func coldStartTLS(t *testing.T) (*http.Client, string, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := server.TLS.Certificates[0]
	certFile, keyFile := filepath.Join(t.TempDir(), "cert.pem"), filepath.Join(t.TempDir(), "key.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	if err := os.WriteFile(certFile, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	return server.Client(), certFile, keyFile
}

func TestEvaluationAssemblyResolvesLateSchedulerController(t *testing.T) {
	const warmingCode = "CoverageWarming"
	ctx := context.Background()
	owners := schedulerTestDependencies(t, schedulerTestConfiguration())
	scheduler := application.NewSchedulerService(ctx, owners)
	t.Cleanup(scheduler.Close)
	evaluations, request := evaluationFixture(t, evaluationSettings(scheduler))
	var config httpapi.HandlerConfig
	// Use the server's assembly before scheduler initialization, not a
	// separately injected ready handler or a captured controller method.
	httpapi.ConfigureEvaluations(&config, evaluations)
	if scheduler.Controller != nil {
		t.Fatal("controller was initialized before server assembly")
	}
	for _, patch := range []*apiextensionsv1.JSON{
		nil, {Raw: []byte(`{"controls":{"power":false}}`)},
	} {
		request.Spec.SettingsPatch = patch
		_, err := config.PreflightEvaluation(ctx, request)
		var warming *serving.Error
		if !errors.As(err, &warming) || warming.Code != warmingCode {
			t.Fatalf("uninitialized preflight error=%v, want CoverageWarming", err)
		}
		response := httptest.NewRecorder()
		httpapi.SendQueryError(response, err)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("uninitialized preflight HTTP status=%d", response.Code)
		}
	}
	// Defect 23: refresh and Tick hold scheduler.Lock for a whole capacity search;
	// preflight's settings read must answer without waiting behind them.
	scheduler.Lock.Lock()
	answered := make(chan struct{})
	go func() {
		_, _ = config.PreflightEvaluation(ctx, request)
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("preflight waited behind the scheduler refresh/control lock")
	}
	scheduler.Lock.Unlock()
	evaluations, request = evaluationFixture(t, evaluationSettings(nil))
	httpapi.ConfigureEvaluations(&config, evaluations)
	_, err := config.PreflightEvaluation(ctx, request)
	var warming *serving.Error
	if !errors.As(err, &warming) || warming.Code != warmingCode {
		t.Fatalf("absent scheduler error=%v, want CoverageWarming", err)
	}
}
