package gateway

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const testRouterID = "router"

type diskArchive string

func (d diskArchive) PutImmutable(_ context.Context, id evidencestore.ObjectIdentity, r io.Reader) (evidencestore.Receipt, error) {
	path := filepath.Join(string(d), id.Key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	_, err = io.Copy(f, r)
	return evidencestore.Receipt{}, errors.Join(err, f.Close())
}
func testRuntime(t *testing.T, id string) *serving.Runtime {
	t.Helper()
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: t.TempDir(), RouterID: id, Store: "test", Retention: time.Hour, Writer: diskArchive(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	r, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{RouterID: id, QueueLimit: 8, ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return r
}
func testAuthorizer(t *testing.T) func(*http.Request) error {
	t.Helper()
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		if review.Spec.Token == "review-failure" {
			return true, nil, errors.New("test review outage")
		}
		username := testRouterID
		if review.Spec.Token == "dashboard-secret" {
			username = "dashboard"
		}
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: review.Spec.Token == "router-secret" || review.Spec.Token == "dashboard-secret", User: authenticationv1.UserInfo{Username: username}}}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		allowed := review.Spec.User == testRouterID && review.Spec.NonResourceAttributes.Path == "/chill"+Path && review.Spec.NonResourceAttributes.Verb == "get"
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	auth := &servingkube.Authorizer{Client: client}
	return func(r *http.Request) error {
		_, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok {
			return &AuthorizationError{Status: 401}
		}
		_, err := auth.Authorize(r.Context(), token, r.Method, r.URL.Path)
		if errors.Is(err, servingkube.ErrUnauthenticated) {
			return &AuthorizationError{Status: 401}
		}
		if errors.Is(err, servingkube.ErrForbidden) {
			return &AuthorizationError{Status: 403}
		}
		return err
	}
}
func testCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	der := server.TLS.Certificates[0].Certificate[0]
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func testPlan(version string) serving.GatewayPlan {
	return serving.GatewayPlan{PlanVersion: version, CreatedAt: time.Now().UTC(), Participants: 1, ObservationWindow: time.Minute, Routing: map[string]map[string]int32{}}
}
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestRealTLSPlanAppliedReportAndDisconnect(t *testing.T) {
	c, _ := NewControl(1)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := c.SetMembershipFile(filepath.Join(t.TempDir(), "membership.json")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(c.Handler(testAuthorizer(t)))
	defer server.Close()
	if err := c.Publish(testPlan("v1")); err != nil {
		t.Fatal(err)
	}
	runtime := testRuntime(t, "router-one")
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("router-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterConfig{ControlURL: "wss" + strings.TrimPrefix(server.URL, "https"), CAFile: testCA(t, server), TokenFile: token, RouterID: "router-one", Runtime: runtime, Native: &firstfloor.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- router.Run(ctx) }()
	applied, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if err := c.WaitApplied(applied, "v1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, runtime.GatewayReady)
	reports := c.Reports()
	if len(reports) != 1 || reports[0].Report.PlanVersion != "v1" || reports[0].Generation == 0 {
		t.Fatalf("reports: %+v", reports)
	}
	if err := c.Publish(testPlan("v2")); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitApplied(applied, "v2"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if runtime.GatewayReady() {
		t.Fatal("disconnected router remained ready")
	}
	waitFor(t, func() bool { return !c.Reports()[0].Connected })
	restored, _ := NewControl(1)
	t.Cleanup(func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := restored.RestoreMembership(c.Reports()); err != nil {
		t.Fatal(err)
	}
	if restored.CanRetire("unknown/container") {
		t.Fatal("restart fabricated applied barrier")
	}
}
func TestExactAuthenticatedInternalEndpoint(t *testing.T) {
	c, _ := NewControl(1)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewTLSServer(c.Handler(testAuthorizer(t)))
	defer server.Close()
	for _, tc := range []struct {
		path, token string
		status      int
	}{{Path, "", 401}, {Path, "dashboard-secret", 403}, {Path, "review-failure", 503}, {Path + "?token=router-secret", "router-secret", 400}, {"/api/v1/live", "router-secret", 400}, {Path + "/", "router-secret", 400}} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
		if resp.StatusCode != tc.status {
			t.Fatalf("%s status %d", tc.path, resp.StatusCode)
		}
	}
}
func TestConnectionGenerationStaleACKAndReservedDrain(t *testing.T) {
	c, _ := NewControl(1)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	plan := testPlan("v1")
	plan.Targets = []serving.GatewayTarget{{InstanceID: "pod/container", PodUID: "pod", ContainerID: "container", DecisionID: "native-ack", Capacity: 1}}
	if err := c.Publish(plan); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := newChannel(ctx, nil)
	first, err := c.join(testRouterID, old)
	if err != nil {
		t.Fatal(err)
	}
	replacement := newChannel(ctx, nil)
	current, err := c.join(testRouterID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	report := serving.GatewayReport{RouterID: testRouterID, PlanVersion: "v1", AppliedAt: time.Now(), ObservedAt: time.Now(), Outstanding: []serving.GatewayOutstanding{{InstanceID: "pod/container", PodUID: "pod", ContainerID: "container", Count: 1, RequestIDs: []string{"reserved-not-sent"}}}}
	if err := c.accept(first, old, message{Type: "applied", Generation: first.Generation, Report: &report}); !errors.Is(err, live.ErrProtocol) {
		t.Fatalf("old connection ACK: %v", err)
	}
	if err := c.accept(current, replacement, message{Type: "applied", Generation: current.Generation, Report: &report}); err != nil {
		t.Fatal(err)
	}
	next := testPlan("v2")
	if err := c.Publish(next); err != nil {
		t.Fatal(err)
	}
	if c.CanRetire("pod/container") {
		t.Fatal("sent plan passed applied barrier")
	}
	if err := c.accept(current, replacement, message{Type: "report", Generation: current.Generation, Report: &report}); !errors.Is(err, live.ErrProtocol) {
		t.Fatalf("old plan report: %v", err)
	}
	report.PlanVersion = "v2"
	if err := c.accept(current, replacement, message{Type: "applied", Generation: current.Generation, Report: &report}); err != nil {
		t.Fatal(err)
	}
	if c.CanRetire("pod/container") {
		t.Fatal("reserved but unsent call did not block drain")
	}
	c.leave(current, replacement)
	if c.CanRetire("pod/container") {
		t.Fatal("disconnect fabricated physical zero")
	}
	if err := c.ForwardProgress("other/container", serving.BackendProgress{RequestID: "reserved-not-sent", Phase: "finished", At: time.Now(), ExecutionComplete: true}); err != nil {
		t.Fatal(err)
	}
	if c.Reports()[0].Report.Outstanding[0].Count != 1 {
		t.Fatal("wrong physical source resolved ownership")
	}
	if err := c.ForwardProgress("pod/container", serving.BackendProgress{RequestID: "reserved-not-sent", Phase: "finished", At: time.Now(), ExecutionComplete: true}); err != nil {
		t.Fatal(err)
	}
	if c.Reports()[0].Report.Outstanding[0].Count != 0 {
		t.Fatal("native completion failed to resolve old owner")
	}
}
func TestCapacityAndLosslessQueueBound(t *testing.T) {
	c, _ := NewControl(2)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	plan := testPlan("v1")
	plan.Participants = 2
	plan.Targets = []serving.GatewayTarget{{InstanceID: "p/c", PodUID: "p", ContainerID: "c", DecisionID: "ack", Capacity: 1}}
	if err := c.Publish(plan); err != nil {
		t.Fatal(err)
	}
	if c.plan.Targets[0].RouterLimit != 0 {
		t.Fatal("zero capacity share rounded upward")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newChannel(ctx, nil)
	for range live.QueueMessages {
		if err := s.send("heartbeat", message{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.send("progress", message{Progress: &serving.BackendProgress{RequestID: "physical"}}); err == nil {
		t.Fatal("full queue discarded evidence without disconnect")
	}
	if s.ctx.Err() == nil {
		t.Fatal("overflow did not close channel")
	}
}
func TestSequenceGapClosesChannel(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		s := newChannel(r.Context(), conn)
		if _, err = s.read(); !errors.Is(err, live.ErrProtocol) {
			t.Errorf("gap: %v", err)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	data, err := live.Encode("resume", 2, time.Now(), struct {
		Checkpoint uint64 `json:"checkpoint"`
	}{0})
	if err != nil {
		t.Fatal(err)
	}
	if err = live.Write(ctx, conn, data); err != nil {
		t.Fatal(err)
	}
	_, _, _ = conn.Read(ctx)
}

func TestExpiredReviewContextCannotUpgrade(t *testing.T) {
	control, _ := NewControl(1)
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "https://control"+Path, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	control.Handler(func(*http.Request) error { return nil }).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Upgrade") != "" {
		t.Fatal("expired review context was upgraded")
	}
}
