package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRouterHandlerOnlyInferenceAndLocalHealth(t *testing.T) {
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: t.TempDir(), RouterID: "router-test", Store: "evidence", Retention: time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	runtime, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{QueueLimit: 4, ObservationLog: log, RouterID: "router-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	runtime.SetGatewayConnected(false)
	handler, err := NewRouterHandler(runtime, &servingkube.Authorizer{Client: fake.NewSimpleClientset()}, log, HandlerConfig{MaxRequestBytes: 1024, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200}, {"GET", "/readyz", 503},
		{"GET", "/api/v1/workloads", 404}, {"GET", "/api/v1/scheduler", 404}, {"GET", "/api/v1/observations/load", 404}, {"GET", "/api/v1/live", 404}, {"GET", "/internal/gateway", 404},
		{"POST", "/api/v1/workloads/a/extra/infer", 404}, {"POST", "/api/v1/workloads/a%2fb/infer", 404}, {"POST", "/api/v1/workloads/../infer", 404}, {"POST", "/api/v1/workloads/a/infer?x=1", 404},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.status {
			t.Fatalf("%s %s: status=%d want=%d", tc.method, tc.path, response.Code, tc.status)
		}
	}
	if err = runtime.ApplyGatewayPlan(serving.GatewayPlan{PlanVersion: "v1", CreatedAt: time.Now(), Participants: 1, ObservationWindow: time.Minute, Routing: map[string]map[string]int32{}}, nil); err != nil {
		t.Fatal(err)
	}
	runtime.SetGatewayConnected(true)
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("applied readiness=%d", ready.Code)
	}
	runtime.SetGatewayConnected(false)
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if stale.Code != http.StatusServiceUnavailable {
		t.Fatal("lost control heartbeat left readiness open")
	}
}
