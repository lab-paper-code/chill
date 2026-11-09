package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func feedbackHTTPHandler(t *testing.T, allowed bool) (*Handler, *atomic.Int64, time.Time) {
	t.Helper()
	base := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	clock := &atomic.Int64{}
	clock.Store(base.Add(time.Second).UnixNano())
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour,
		Writer: &settingsArchive{directory: t.TempDir()}, Now: func() time.Time { return time.Unix(0, clock.Load()).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "client", UID: "actor-uid"}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		sar := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		attributes := sar.Spec.NonResourceAttributes
		if attributes == nil || !strings.HasPrefix(attributes.Path, "/chill/api/v1/") ||
			attributes.Verb != "post" && attributes.Verb != "get" {
			t.Errorf("wrong feedback SAR: %+v", sar.Spec)
		}
		if attributes != nil && attributes.Verb == "post" && attributes.Path != "/chill/api/v1/workloads/workload/feedback" && attributes.Path != "/chill/api/v1/workloads/other/feedback" {
			t.Errorf("feedback did not use path authorization: %+v", attributes)
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	h := &Handler{observationLog: log, authorizer: &servingkube.Authorizer{Client: kube}, mux: http.NewServeMux(), config: HandlerConfig{RequestTimeout: time.Second, OperationTimeout: time.Second, MaxRequestBytes: 1024, MaxQueryEvents: 100}}
	h.mux.HandleFunc("POST "+ClientFeedbackPath, h.clientFeedback)
	h.mux.HandleFunc("GET /api/v1/observations/clients", h.clientObservations)
	return h, clock, base
}

func feedbackPOST(t *testing.T, server *httptest.Server, workload, body, token string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/workloads/"+workload+"/feedback", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(data)
}

func TestClientFeedbackBoundaryAndDurableEvidence(t *testing.T) {
	h, clock, base := feedbackHTTPHandler(t, true)
	server := httptest.NewServer(h)
	defer server.Close()
	valid := `{"observationID":"opaque / not-a-uuid","elapsedNanos":120000000,"outcome":"completed","statusCode":200}`
	if status, _ := feedbackPOST(t, server, "workload", "broken", ""); status != http.StatusUnauthorized {
		t.Fatalf("authorization must precede decoding: %d", status)
	}
	for _, body := range []string{
		`{"observationID":"x","elapsedNanos":-1,"outcome":"timeout"}`,
		`{"observationID":"x","elapsedNanos":true,"outcome":"timeout"}`,
		`{"observationID":"x","elapsedNanos":1.5,"outcome":"timeout"}`,
		`{"observationID":"x","elapsedNanos":9223372036854775808,"outcome":"timeout"}`,
		`{"observationID":"x","elapsedNanos":null,"outcome":"timeout"}`,
		`{"observationID":"x","outcome":"timeout"}`,
		`{"observationID":"","elapsedNanos":0,"outcome":"timeout"}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"unknown"}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"completed"}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"http_error","statusCode":200}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"timeout","statusCode":600}`,
		`{"observationID":"x","requestID":"","elapsedNanos":0,"outcome":"timeout"}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"timeout","actorUsername":"forged"}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"timeout","reportedAt":"2026-11-09T00:00:00Z"}`,
		valid + `{}`,
		strings.Repeat(" ", 1024) + valid,
	} {
		if status, data := feedbackPOST(t, server, "workload", body, "token"); status != http.StatusBadRequest {
			t.Fatalf("invalid feedback accepted: %d %s body=%s", status, data, body)
		}
	}
	for _, body := range []string{valid, `{"observationID":"opaque / not-a-uuid","elapsedNanos":900000000,"outcome":"timeout"}`} {
		if status, data := feedbackPOST(t, server, "workload", body, "token"); status != http.StatusNoContent || data != "" {
			t.Fatalf("FIFO admission failed: %d %s", status, data)
		}
	}
	page, err := h.observationLog.ClientObservationPage(context.Background(), base, base.Add(time.Minute), 1, 0)
	if err != nil || len(page.Observations) != 1 || page.NextCursor != "1" {
		t.Fatalf("raw first page: %+v %v", page, err)
	}
	first := page.Observations[0]
	if first.ActorUsername != "client" || first.ActorUID != "actor-uid" || first.WorkloadName != "workload" || first.WorkloadUID != "" || !first.ReportedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("server-owned evidence: %+v", first)
	}
	page, err = h.observationLog.ClientObservationPage(context.Background(), base, base.Add(time.Minute), 1, 1)
	if err != nil || len(page.Observations) != 1 || page.Observations[0].Outcome != "timeout" {
		t.Fatalf("later raw report lost: %+v %v", page, err)
	}
	clock.Store(base.Add(2 * time.Minute).UnixNano())
	raw, err := h.observationLog.OpenRaw(context.Background(), base.Format("20060102T15"), "clients")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Reader.Close() }()
	decoder := json.NewDecoder(raw.Reader)
	for range 2 {
		var report serving.ClientObservation
		if err := decoder.Decode(&report); err != nil {
			t.Fatal(err)
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("raw clients stream changed: %v", err)
	}
	if err := h.observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, _ := feedbackPOST(t, server, "workload", valid, "token"); status != http.StatusServiceUnavailable {
		t.Fatalf("closed log admission: %d", status)
	}
}

func TestClientFeedbackExistingAuthorization(t *testing.T) {
	h, _, _ := feedbackHTTPHandler(t, false)
	server := httptest.NewServer(h)
	defer server.Close()
	if status, _ := feedbackPOST(t, server, "workload", "broken", "token"); status != http.StatusForbidden {
		t.Fatalf("feedback bypassed path SAR: %d", status)
	}
}
