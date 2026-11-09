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
			attributes.Verb != strings.ToLower(http.MethodPost) && attributes.Verb != measurementGetVerb {
			t.Errorf("wrong feedback SAR: %+v", sar.Spec)
		}
		if attributes != nil && attributes.Verb == strings.ToLower(http.MethodPost) && attributes.Path != "/chill/api/v1/workloads/workload/feedback" && attributes.Path != "/chill/api/v1/workloads/other/feedback" {
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
		`{"observationID":"x","elapsedNanos":0,"outcome":"timeout","actorUsername":"forged"}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"timeout","reportedAt":"2026-11-09T00:00:00Z"}`,
		`{"observationID":"x","elapsedNanos":0,"outcome":"timeout","duplicate":false}`,
		valid + `{}`,
		strings.Repeat(" ", 1024) + valid,
	} {
		if status, data := feedbackPOST(t, server, "workload", body, "token"); status != http.StatusBadRequest {
			t.Fatalf("invalid feedback accepted: %d %s body=%s", status, data, body)
		}
	}
	for _, body := range []string{
		valid,
		`{"observationID":"opaque / not-a-uuid","elapsedNanos":900000000,"outcome":"timeout"}`,
		`{"observationID":"empty-optional-identities","requestID":"","workloadUID":"","elapsedNanos":0,"outcome":"timeout"}`,
	} {
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
	if err != nil || len(page.Observations) != 1 || page.Observations[0].Outcome != timingOutcomeTimeout {
		t.Fatalf("later raw report lost: %+v %v", page, err)
	}
	verifyPublicClientTimeout(t, server, base)
	clock.Store(base.Add(2 * time.Minute).UnixNano())
	raw, err := h.observationLog.OpenRaw(context.Background(), base.Format("20060102T15"), "clients")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Reader.Close() }()
	decoder := json.NewDecoder(raw.Reader)
	for range 3 {
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

func verifyPublicClientTimeout(t *testing.T, server *httptest.Server, base time.Time) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/observations/clients?from="+base.Format(time.RFC3339Nano)+"&to="+base.Add(time.Minute).Format(time.RFC3339Nano)+"&limit=1&cursor=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer token")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var public observation.ClientObservationPage
	decodeErr := json.NewDecoder(response.Body).Decode(&public)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil ||
		len(public.Observations) != 1 || public.Observations[0].Outcome != timingOutcomeTimeout {
		t.Fatalf("public clients query: status=%d page=%+v decode=%v close=%v", response.StatusCode, public, decodeErr, closeErr)
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

func feedbackServerRequest(t *testing.T, log *observation.ObservationLog, id string, received time.Time, definition *serving.Definition) {
	t.Helper()
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	var timing serving.RequestTimings
	timing.Record(serving.ServerAssigned, 28*time.Millisecond)
	timing.Record(serving.ServerSenderElapsedFrozen, 30*time.Millisecond)
	assigned := received.Add(28 * time.Millisecond)
	events := []serving.RequestEvent{
		{Kind: "received", At: received},
		{Kind: "accepted", At: received.Add(8 * time.Millisecond)},
		{Kind: "assigned", At: assigned, Assignment: &serving.Assignment{Instance: "instance", DispatchedAt: assigned}, ServerTiming: timing},
		{Kind: "backend_finished", At: received.Add(90 * time.Millisecond), NativeInstance: "instance", NativeProgress: &serving.BackendProgress{
			Phase: "finished", At: received.Add(90 * time.Millisecond), NativeElapsedMS: 60, ExecutionComplete: true, Outcome: "completed",
		}},
		{Kind: "backend_exchange_finished", At: received.Add(95 * time.Millisecond), ExecutionComplete: true},
		{Kind: "response_finished", At: received.Add(101 * time.Millisecond), Outcome: "completed"},
	}
	for _, event := range events {
		event.RequestID, event.WorkloadUID, event.Definition = id, definition.UID, definition.Identity()
		if err := log.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.RequestClosed(id, serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
}

func TestClientFeedbackHTTPToLoadWindow(t *testing.T) {
	h, clock, base := feedbackHTTPHandler(t, true)
	server := httptest.NewServer(h)
	defer server.Close()
	post := func(body string) {
		t.Helper()
		if status, data := feedbackPOST(t, server, "workload", body, "token"); status != http.StatusNoContent {
			t.Fatalf("feedback admission: %d %s", status, data)
		}
	}
	from, to := base, base.Add(2*time.Second)
	post(`{"observationID":"client-first","requestID":"request/opaque","elapsedNanos":120000000,"outcome":"completed","statusCode":200}`)
	pending, err := h.observationLog.LoadWindow(context.Background(), from, to)
	if err != nil || len(pending.PathSamples) != 0 || len(pending.ClientObservations) != 1 ||
		pending.ClientObservations[0].WorkloadUID != "" {
		t.Fatalf("client-first evidence: %+v %v", pending, err)
	}
	definition := &serving.Definition{Name: "workload", UID: "server-uid", Generation: 1}
	feedbackServerRequest(t, h.observationLog, "request/opaque", base.Add(100*time.Millisecond), definition)
	joined := verifyServerJoinedFeedback(t, h.observationLog, from, to, definition.UID)
	post(`{"observationID":"client-first","requestID":"request/opaque","elapsedNanos":999000000,"outcome":"timeout"}`)
	post(`{"observationID":"another-id","requestID":"request/opaque","elapsedNanos":500000000,"outcome":"completed","statusCode":200}`)
	post(`{"observationID":"timeout/no-id","elapsedNanos":80000000,"outcome":"timeout"}`)
	// A duplicate can connect an observation ID and request ID without adding a
	// population member. Its later alias must not count as another inference.
	post(`{"observationID":"timeout/no-id","requestID":"alias-request","elapsedNanos":90000000,"outcome":"timeout"}`)
	post(`{"observationID":"alias-observation","requestID":"alias-request","elapsedNanos":100000000,"outcome":"timeout"}`)
	feedbackServerRequest(t, h.observationLog, "late", base.Add(-30*time.Second), definition)
	post(`{"observationID":"late-report","requestID":"late","elapsedNanos":120000000,"outcome":"completed","statusCode":200}`)
	after, err := h.observationLog.LoadWindow(context.Background(), from, to)
	if err != nil || len(after.PathSamples) != 1 || len(after.ClientObservations) != 2 {
		t.Fatalf("duplicates, failure or late receipt cohort changed: %+v %v", after, err)
	}
	if after.PathSamples[0] != joined.PathSamples[0] || after.ClientObservations[0].Outcome != "completed" ||
		after.ClientObservations[1].Outcome != timingOutcomeTimeout || after.ClientObservations[1].WorkloadUID != "" ||
		after.ClientObservations[1].RequestID != "" {
		t.Fatalf("first evidence or unknown timeout identity changed: %+v", after)
	}
	// Identity assertions are retained without a per-report history lookup.
	// The captured join rejects attribution to a different server identity.
	post(`{"observationID":"wrong-uid","requestID":"request/opaque","workloadUID":"new-uid","elapsedNanos":1,"outcome":"timeout"}`)
	if status, _ := feedbackPOST(t, server, "other", `{"observationID":"wrong-name","requestID":"request/opaque","elapsedNanos":1,"outcome":"timeout"}`, "token"); status != http.StatusNoContent {
		t.Fatalf("raw identity assertion was not retained: %d", status)
	}
	// A report accepted exactly at the cutoff belongs only to the next capture,
	// and only while its server receipt still belongs to that capture's window.
	clock.Store(to.UnixNano())
	feedbackServerRequest(t, h.observationLog, "cutoff", base.Add(200*time.Millisecond), definition)
	post(`{"observationID":"cutoff-report","requestID":"cutoff","elapsedNanos":120000000,"outcome":"completed","statusCode":200}`)
	cutoff, err := h.observationLog.LoadWindow(context.Background(), from, to)
	if err != nil || len(cutoff.PathSamples) != 1 {
		t.Fatalf("cutoff included a new report: %+v %v", cutoff, err)
	}
	next, err := h.observationLog.LoadWindow(context.Background(), from, to.Add(time.Second))
	if err != nil || len(next.PathSamples) != 2 {
		t.Fatalf("next capture failed to join a retained receipt: %+v %v", next, err)
	}
	clock.Store(base.Add(3 * time.Minute).UnixNano())
	expired, err := h.observationLog.LoadWindow(context.Background(), base.Add(2*time.Minute), base.Add(3*time.Minute))
	if err != nil || len(expired.PathSamples) != 0 || len(expired.ClientObservations) != 0 {
		t.Fatalf("old receipts resurrected: %+v %v", expired, err)
	}
	raw, err := h.observationLog.ClientObservationPage(context.Background(), from, to.Add(time.Second), 100, 0)
	if err != nil || len(raw.Observations) != 10 || raw.Observations[0].WorkloadUID != "" {
		t.Fatalf("raw reports mutated or lost: %+v %v", raw, err)
	}
}

func verifyServerJoinedFeedback(t *testing.T, log *observation.ObservationLog, from, to time.Time, uid string) observation.LoadWindow {
	t.Helper()
	joined, err := log.LoadWindow(context.Background(), from, to)
	if err != nil || len(joined.PathSamples) != 1 || len(joined.ClientObservations) != 1 {
		t.Fatalf("server-later join needs no new report: %+v %v", joined, err)
	}
	if sample := joined.PathSamples[0]; sample.TotalNanos != 40e6 || sample.PostNanos != 30e6 || sample.WorkloadUID != uid {
		t.Fatalf("TOTAL/POST or server identity changed: %+v", sample)
	}
	if joined.ClientObservations[0].WorkloadUID != uid {
		t.Fatalf("joined identity did not use server row: %+v", joined.ClientObservations)
	}
	return joined
}
