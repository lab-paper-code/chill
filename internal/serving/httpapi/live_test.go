package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func liveAuthorizer(t *testing.T, authenticated, allowed, unavailable bool) *servingkube.Authorizer {
	t.Helper()
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		if unavailable {
			return true, nil, errors.New("review unavailable")
		}
		review := action.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		if review.Spec.Token != "test-bearer" {
			t.Error("wrong token at review boundary")
		}
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: authenticated, User: authenticationv1.UserInfo{Username: "live-reader", UID: "reader-uid"}}}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		if review.Spec.User != "live-reader" || review.Spec.NonResourceAttributes.Path != "/chill/api/v1/live" || review.Spec.NonResourceAttributes.Verb != "get" {
			t.Errorf("incorrect live SAR: %+v", review.Spec)
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	return &servingkube.Authorizer{Client: client}
}

func readLiveMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]json.RawMessage {
	t.Helper()
	kind, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageText {
		t.Fatal("non-text server frame")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestLiveAuthenticatedWebSocketSmoke(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 123, time.UTC)
	hub := live.New(at)
	defer hub.Close()
	handler := &Handler{authorizer: liveAuthorizer(t, true, true, false), config: HandlerConfig{Live: hub, RequestTimeout: time.Millisecond}}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+livePath, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-bearer"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	hello := readLiveMessage(t, ctx, conn)
	if string(hello["type"]) != `"hello"` || string(hello["protocol"]) != `"chill-live/1"` || string(hello["seq"]) != "0" {
		t.Fatalf("hello: %v", hello)
	}
	workloads := []live.Workload{{UID: "uid-one", Name: "one", Model: "model"}}
	if err := hub.Publish(at.Add(time.Second), live.Delta{Workloads: &workloads}, nil); err != nil {
		t.Fatal(err)
	}
	// Remain connected beyond the ordinary Handler timeout. State published
	// before resume belongs in the atomic baseline, not a preceding delta.
	time.Sleep(20 * time.Millisecond)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resume","from":0}`)); err != nil {
		t.Fatal(err)
	}
	baseline := readLiveMessage(t, ctx, conn)
	if string(baseline["type"]) != `"snapshot"` || string(baseline["seq"]) != "1" || string(baseline["at"]) != `"2026-10-25T12:00:01.000000123Z"` {
		t.Fatalf("baseline after resume: %v", baseline)
	}
	decision := &live.Decision{At: "2026-10-25T12:00:02Z", State: "keep", Reason: "observed"}
	if err := hub.Publish(at.Add(2*time.Second), live.Delta{Decision: &decision}, &live.Event{Kind: "decision", Decision: decision}); err != nil {
		t.Fatal(err)
	}
	delta := readLiveMessage(t, ctx, conn)
	event := readLiveMessage(t, ctx, conn)
	if string(delta["type"]) != `"delta"` || string(delta["seq"]) != "2" || string(event["type"]) != `"event"` || string(event["seq"]) != "3" {
		t.Fatalf("delta/event ordering: %v / %v", delta, event)
	}
	t.Logf("authenticated WebSocket receive: hello seq=%s; snapshot seq=%s at=%s; delta seq=%s; decision event seq=%s", hello["seq"], baseline["seq"], baseline["at"], delta["seq"], event["seq"])
}

func TestLiveDeniesBeforeUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		authenticated, allowed, unavailable bool
		status                              int
	}{
		{"unauthenticated", false, false, false, http.StatusUnauthorized},
		{"forbidden", true, false, false, http.StatusForbidden},
		{"unavailable", false, false, true, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := live.New(time.Now())
			defer hub.Close()
			handler := &Handler{authorizer: liveAuthorizer(t, tc.authenticated, tc.allowed, tc.unavailable), config: HandlerConfig{Live: hub}}
			server := httptest.NewServer(handler)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+livePath, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-bearer"}}})
			if conn != nil {
				conn.CloseNow()
				t.Fatal("unauthorized socket upgraded")
			}
			if err == nil || response == nil || response.StatusCode != tc.status {
				t.Fatalf("preupgrade status: response=%v err=%v", response, err)
			}
		})
	}
}

func TestLiveRejectsQueryEscapedPathAndSubprotocol(t *testing.T) {
	for _, tc := range []struct{ target, protocol string }{
		{livePath + "?token=not-accepted", ""},
		{livePath + "?", ""},
		{"/api/v1/%6cive", ""},
		{livePath, "chill-live/1"},
	} {
		handler := &Handler{}
		request := httptest.NewRequest(http.MethodGet, tc.target, nil)
		if tc.protocol != "" {
			request.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid live upgrade accepted: %s / %s: %d", tc.target, tc.protocol, response.Code)
		}
	}
}

func TestLiveResumeIsTheOnlyClientMessage(t *testing.T) {
	for _, input := range []string{
		`{"type":"auth","token":"not-forwarded"}`,
		`{"type":"resume"}`, `{"type":"resume","from":-1}`, `{"type":"resume","from":1.1}`,
		`{"type":"resume","from":9007199254740992,"at":"2026-10-25T12:00:00Z"}`,
		`{"type":"resume","from":1}`, `{"type":"resume","from":1,"at":null}`,
		`{"type":"resume","from":0,"at":"2026-10-25T12:00:00Z"}`,
		`{"type":"resume","from":0,"at":null}`,
		`{"type":"resume","from":1,"at":"2026-10-25T12:00:00+00:00"}`,
		`{"type":"resume","from":1,"at":"invalid"}`,
		`{"type":"resume","from":0,"extra":true}`, `{"type":"resume","from":0} {}`,
	} {
		if _, err := decodeResume([]byte(input)); !errors.Is(err, live.ErrProtocol) {
			t.Fatalf("invalid contract accepted: %s / %v", input, err)
		}
	}
	for _, input := range []string{`{"type":"resume","from":0}`, `{"type":"resume","from":1,"at":"2026-10-25T12:00:00.123456789Z"}`} {
		if _, err := decodeResume([]byte(input)); err != nil {
			t.Fatalf("valid resume rejected: %s / %v", input, err)
		}
	}
}

func TestLiveInvalidAuthenticatedMessageCloses1002(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind websocket.MessageType
		data []byte
	}{
		{"second auth", websocket.MessageText, []byte(`{"type":"auth","token":"second-auth"}`)},
		{"binary", websocket.MessageBinary, []byte(`{"type":"resume","from":0}`)},
		{"oversized resume", websocket.MessageText, []byte(strings.Repeat("x", int(live.ClientMessageBytes)+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := live.New(time.Now())
			defer hub.Close()
			server := httptest.NewServer(&Handler{authorizer: liveAuthorizer(t, true, true, false), config: HandlerConfig{Live: hub}})
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+livePath, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-bearer"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			_ = readLiveMessage(t, ctx, conn)
			if err := conn.Write(ctx, tc.kind, tc.data); err != nil {
				t.Fatal(err)
			}
			_, _, err = conn.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusProtocolError {
				t.Fatalf("invalid authenticated contract close: %v", err)
			}
		})
	}
}

func TestLiveOversizedBaselineCloses1009(t *testing.T) {
	hub := live.New(time.Now())
	defer hub.Close()
	rows := []live.Instance{{ID: "one", ProfileID: strings.Repeat("x", 600<<10)}}
	if err := hub.Publish(time.Now(), live.Delta{Instances: &rows}, nil); err != nil {
		t.Fatal(err)
	}
	rows[0].ID = "two"
	if err := hub.Publish(time.Now(), live.Delta{Instances: &rows}, nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(&Handler{authorizer: liveAuthorizer(t, true, true, false), config: HandlerConfig{Live: hub}})
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+livePath, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-bearer"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_ = readLiveMessage(t, ctx, conn)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resume","from":0}`)); err != nil {
		t.Fatal(err)
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("oversized snapshot close: %v", err)
	}
}
