package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

func TestRouterOriginIdentityAndTLSHostname(t *testing.T) {
	runtime := testRuntime(t, testRouterID)
	for _, origin := range []string{"https://control", "ws://control", "wss://token@control", "wss://control/path", "wss://control?token=secret", "wss://control#fragment"} {
		if _, err := NewRouter(RouterConfig{ControlURL: origin, Runtime: runtime, Native: &firstfloor.Client{}, RouterID: testRouterID, CAFile: "unused", TokenFile: "unused"}); err == nil {
			t.Fatalf("accepted untrusted origin %q", origin)
		}
	}
	for _, id := range []string{".", "..", "a/b", "a\\b", "a\x00b", "a b"} {
		control, _ := NewControl(1)
		if err := control.RestoreMembership([]RouterState{{RouterID: id}}); err == nil {
			t.Errorf("restored unsafe router identity %q", id)
		}
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	}
	control, _ := NewControl(1)
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewTLSServer(control.Handler(testAuthorizer(t)))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("router-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	origin := "wss://wrong-host.invalid:" + strings.Split(server.Listener.Addr().String(), ":")[1]
	router, err := NewRouter(RouterConfig{ControlURL: origin, Runtime: runtime, Native: &firstfloor.Client{}, RouterID: testRouterID, CAFile: testCA(t, server), TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	router.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = router.session(ctx); err == nil {
		t.Fatal("TLS hostname mismatch accepted")
	}
	if runtime.GatewayReady() {
		t.Fatal("failed TLS authentication opened readiness")
	}
}
func TestHeartbeatLossClosesReadiness(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		channel := newChannel(r.Context(), conn)
		defer channel.close()
		go channel.writeLoop()
		if _, err = channel.read(); err != nil {
			return
		}
		plan := testPlan("heartbeat-plan")
		if err = channel.send("plan", message{Generation: 1, Plan: &plan}); err != nil {
			return
		}
		applied, err := channel.read()
		if err != nil || applied.Type != "applied" {
			return
		}
		if err = channel.send("ready", message{Generation: 1, Plan: &plan}); err != nil {
			return
		}
		// Drain reports but deliberately send no heartbeat: a live TCP connection
		// alone must not keep admission open after the freshness bound.
		for {
			if _, err = channel.read(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	runtime := testRuntime(t, "heartbeat-router")
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("router-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterConfig{ControlURL: "wss" + strings.TrimPrefix(server.URL, "https"), Runtime: runtime, Native: &firstfloor.Client{}, RouterID: "heartbeat-router", CAFile: testCA(t, server), TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- router.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, runtime.GatewayReady)
	deadline := time.Now().Add(FreshnessTimeout + 2*time.Second)
	for runtime.GatewayReady() {
		if time.Now().After(deadline) {
			t.Fatal("heartbeat loss left admission and readiness open")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestObservationSourceSnapshotIsDetached(t *testing.T) {
	control, _ := NewControl(1)
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	elapsed := int64(10)
	control.members[testRouterID] = &member{RouterState: RouterState{RouterID: testRouterID, Report: serving.GatewayReport{Open: []serving.RequestRow{{RequestID: "request", Node: &serving.NodeReference{UID: "original"}, ElapsedNanos: &elapsed}}}}}
	sources := control.ObservationSources()
	sources[0].Open[0].Node.UID = "mutated"
	*sources[0].Open[0].ElapsedNanos = 100
	original := control.ObservationSources()
	if original[0].Open[0].Node.UID != "original" || *original[0].Open[0].ElapsedNanos != 10 {
		t.Fatal("observation consumer mutated coordinator report")
	}
}

func TestFreshnessWatchdogClosesDuringStalledFrameHandler(t *testing.T) {
	runtime := testRuntime(t, "stalled-router")
	if err := runtime.ApplyGatewayPlan(testPlan("watchdog"), nil); err != nil {
		t.Fatal(err)
	}
	runtime.SetGatewayConnected(true)
	runtime.SetGatewayFreshUntil(time.Now().Add(time.Hour))
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		channel := newChannel(r.Context(), conn)
		defer channel.close()
		channel.lastRead.Store(time.Now().Add(-FreshnessTimeout - time.Second).UnixNano())
		go channel.watchFreshness(runtime)
		// No further Reader call can enforce its per-read timeout while a frame
		// handler is stuck here. The independent watchdog must close this socket.
		<-release
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err = conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("watchdog failed to close stalled channel: %v", err)
	}
	if runtime.GatewayReady() {
		t.Fatal("stalled frame kept readiness open")
	}
}
