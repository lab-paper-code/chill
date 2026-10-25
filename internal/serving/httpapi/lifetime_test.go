package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestAppliedRequestLifetime(t *testing.T) {
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close(context.Background())
	runtime, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{QueueLimit: 8, ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Shutdown(context.Background())
	lifetime := 120 * time.Second
	definition := &serving.Definition{Name: "workload", UID: "uid", Generation: 1, RequestLifetime: &lifetime}
	if err := runtime.PublishClosed(definition, "test"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{runtime: runtime, config: HandlerConfig{RequestTimeout: 30 * time.Second}, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /api/workloads/{id}/infer", h.infer)
	for _, tc := range []struct{ header, want string }{{"", "2m0s"}, {"125s", "2m0s"}, {"120s", "2m0s"}, {"5s", "5s"}} {
		request := httptest.NewRequest(http.MethodPost, "/api/workloads/workload/infer", nil)
		if tc.header != "" {
			request.Header.Set("X-Chill-Request-Timeout", tc.header)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if got := response.Header().Get("X-Chill-Request-Lifetime"); got != tc.want {
			t.Fatalf("header %q: lifetime %q, want %q", tc.header, got, tc.want)
		}
	}
}
