package gateway

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

type smokeEvent struct {
	Role    string `json:"role"`
	PID     int    `json:"pid"`
	Version string `json:"version,omitempty"`
	Origin  string `json:"origin,omitempty"`
	CA      string `json:"ca,omitempty"`
}

func emitSmoke(event smokeEvent) {
	raw, _ := json.Marshal(event)
	fmt.Println("GATEWAY_SMOKE " + string(raw))
}

// Runnable without a cluster or external API:
// go test ./internal/serving/gateway -run '^TestTwoProcessSmoke$' -v
// Both helpers are OS processes using real TCP/TLS WebSockets and Runtime apply.
func TestTwoProcessSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("router-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	control, controlIn, controlEvents := startSmoke(t, ctx, "control", root, nil)
	baseline := nextSmoke(t, ctx, controlEvents, "listening")
	router, routerIn, routerEvents := startSmoke(t, ctx, "router", root, []string{"CHILL_SMOKE_ORIGIN=" + baseline.Origin, "CHILL_SMOKE_CA=" + baseline.CA, "CHILL_SMOKE_TOKEN=" + token})
	routerApplied := nextSmoke(t, ctx, routerEvents, "router")
	controlApplied := nextSmoke(t, ctx, controlEvents, "control")
	if routerApplied.PID == controlApplied.PID || routerApplied.PID == os.Getpid() || controlApplied.PID == os.Getpid() || routerApplied.Version != "smoke-plan" || controlApplied.Version != routerApplied.Version {
		t.Fatalf("not two real applied processes: %+v %+v", controlApplied, routerApplied)
	}
	t.Logf("control PID=%d applied=%s; router PID=%d applied=%s", controlApplied.PID, controlApplied.Version, routerApplied.PID, routerApplied.Version)
	_ = routerIn.Close()
	if err := router.Wait(); err != nil {
		t.Fatal(err)
	}
	_ = controlIn.Close()
	if err := control.Wait(); err != nil {
		t.Fatal(err)
	}
}
func startSmoke(t *testing.T, ctx context.Context, role, root string, extra []string) (*exec.Cmd, io.WriteCloser, <-chan smokeEvent) {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGatewaySubprocessRole$", "-test.v")
	command.Env = append(os.Environ(), "CHILL_SMOKE_ROLE="+role, "CHILL_SMOKE_ROOT="+root)
	command.Env = append(command.Env, extra...)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})
	events := make(chan smokeEvent, 4)
	go func() {
		defer close(events)
		scan := bufio.NewScanner(output)
		for scan.Scan() {
			line := scan.Text()
			if !strings.HasPrefix(line, "GATEWAY_SMOKE ") {
				continue
			}
			var event smokeEvent
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "GATEWAY_SMOKE ")), &event) == nil {
				events <- event
			}
		}
	}()
	return command, input, events
}
func nextSmoke(t *testing.T, ctx context.Context, events <-chan smokeEvent, role string) smokeEvent {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", role, ctx.Err())
		case event, ok := <-events:
			if !ok {
				t.Fatalf("%s helper exited before evidence", role)
			}
			if event.Role == role {
				return event
			}
		}
	}
}
func TestGatewaySubprocessRole(t *testing.T) {
	role := os.Getenv("CHILL_SMOKE_ROLE")
	if role == "" {
		t.Skip("subprocess helper")
	}
	switch role {
	case "control":
		c, err := NewControl(1)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err = c.SetMembershipFile(filepath.Join(os.Getenv("CHILL_SMOKE_ROOT"), "membership.json")); err != nil {
			t.Fatal(err)
		}
		if err = c.Publish(testPlan("smoke-plan")); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewTLSServer(c.Handler(testAuthorizer(t)))
		defer server.Close()
		cert := server.TLS.Certificates[0].Certificate[0]
		if _, err = x509.ParseCertificate(cert); err != nil {
			t.Fatal(err)
		}
		ca := filepath.Join(os.Getenv("CHILL_SMOKE_ROOT"), "control-ca.pem")
		if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0600); err != nil {
			t.Fatal(err)
		}
		emitSmoke(smokeEvent{Role: "listening", PID: os.Getpid(), Origin: "wss" + strings.TrimPrefix(server.URL, "https"), CA: ca})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = c.WaitApplied(ctx, "smoke-plan"); err != nil {
			t.Fatal(err)
		}
		reports := c.Reports()
		if len(reports) != 1 || reports[0].Report.ObservedAt.IsZero() {
			t.Fatalf("missing actual router report: %+v", reports)
		}
		emitSmoke(smokeEvent{Role: "control", PID: os.Getpid(), Version: reports[0].Report.PlanVersion})
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "router":
		runtime := testRuntime(t, "smoke-router")
		client, err := NewRouter(RouterConfig{ControlURL: os.Getenv("CHILL_SMOKE_ORIGIN"), CAFile: os.Getenv("CHILL_SMOKE_CA"), TokenFile: os.Getenv("CHILL_SMOKE_TOKEN"), RouterID: "smoke-router", Runtime: runtime, Native: &firstfloor.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- client.Run(ctx) }()
		waitFor(t, runtime.GatewayReady)
		report := runtime.GatewayReport("smoke-router", time.Now())
		if report.PlanVersion != "smoke-plan" || report.AppliedAt.IsZero() {
			t.Fatalf("missing real apply: %+v", report)
		}
		emitSmoke(smokeEvent{Role: "router", PID: os.Getpid(), Version: report.PlanVersion})
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper role %q", role)
	}
}
