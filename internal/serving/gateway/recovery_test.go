package gateway

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

func TestRecoveredGenerationFinalizesOriginalRowsOverTLS(t *testing.T) {
	const oldID = "pod-uid-00000000000000000000000000000001"
	const newID = "pod-uid-00000000000000000000000000000002"
	const instance = "native-pod/containerd://native"
	directory := t.TempDir()
	archive := diskArchive(t.TempDir())
	cfg := serving.ObservationLogConfig{Directory: directory, RouterID: oldID, Store: "test", Retention: time.Hour, Writer: archive}
	oldLog, err := serving.NewObservationLog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	definition := &serving.Definition{UID: "workload", Name: "workload", Generation: 1}
	if err = oldLog.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assignment := &serving.Assignment{RouterID: oldID, PlanVersion: "old-plan", PodUID: "native-pod", ContainerID: "containerd://native", Instance: instance, Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Profile: "profile", DispatchedAt: now}
	if err = oldLog.RequestEvent(serving.RequestEvent{RouterID: oldID, PlanVersion: "old-plan", RequestID: "old-request", WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "assigned", At: now, ReceivedAt: now, Assignment: assignment}); err != nil {
		t.Fatal(err)
	}
	if err = oldLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recoveredLog, err := serving.NewObservationLog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredLog.Close(context.Background())
	recovered, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{RouterID: oldID, QueueLimit: 4, ObservationLog: recoveredLog})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = recovered.Shutdown(ctx)
	}()
	recovered.SetGatewayConnected(false)
	original := recovered.GatewayReport(oldID, time.Time{})
	if len(original.Outstanding) != 1 || original.Outstanding[0].Count != 1 {
		t.Fatalf("lost restored physical owner: %+v", original)
	}
	control, _ := NewControl(1)
	defer control.Close()
	if err = control.RestoreMembership([]RouterState{{RouterID: oldID, Generation: 1, JoinedAt: now, Report: original, Exposed: map[string]bool{instance: true}}}); err != nil {
		t.Fatal(err)
	}
	if err = control.Publish(testPlan("retired-plan")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(control.Handler(testAuthorizer(t)))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err = os.WriteFile(token, []byte("router-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	current := testRuntime(t, newID)
	router, err := NewRouter(RouterConfig{ControlURL: "wss" + strings.TrimPrefix(server.URL, "https"), CAFile: testCA(t, server), TokenFile: token, RouterID: newID, Runtime: current, Native: &firstfloor.Client{}, Recovery: []Recovery{{RouterID: oldID, Runtime: recovered}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- router.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, current.GatewayReady)
	if control.CanRetire(instance) {
		t.Fatal("recovery registration released physical ownership")
	}
	if recovered.GatewayReady() {
		t.Fatal("recovery source opened new inference admission")
	}
	progress := serving.BackendProgress{RequestID: "old-request", WorkloadUID: definition.UID, Definition: definition.Identity(), Phase: "finished", ExecutionComplete: true, At: time.Now().UTC()}
	if err = control.ForwardProgress(instance, progress); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(recoveredLog.OpenRows()) == 0 })
	waitFor(t, func() bool { return control.CanRetire(instance) })
	rows := 0
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.Contains(entry.Name(), "requests") || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var row serving.RequestRow
			if err = json.Unmarshal([]byte(line), &row); err != nil {
				return err
			}
			if row.Kind == "final" && row.RequestID == "old-request" {
				rows++
				if row.RouterID != oldID || row.PlanVersion != "old-plan" || !row.ExecutionComplete {
					t.Errorf("incorrect recovered final: %+v", row)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("recovery wrote %d final rows, want1", rows)
	}
}
func TestRequestsAtAgesDetachedSnapshots(t *testing.T) {
	control, _ := NewControl(1)
	defer control.Close()
	at := time.Now()
	remaining := 3 * time.Second
	control.members["router"] = &member{RouterState: RouterState{RouterID: "router", Report: serving.GatewayReport{ObservedAt: at, Requests: []serving.SchedulerRequest{{ID: "pending", Phase: "pending", Elapsed: time.Second, RemainingLifetime: &remaining}, {ID: "running", Phase: "running"}, {ID: "returning", Phase: "returning"}}}}}
	requests := control.RequestsAt(at.Add(2 * time.Second))
	if requests[0].Elapsed != 3*time.Second || requests[0].QueueAge != 2*time.Second || *requests[0].RemainingLifetime != time.Second || requests[1].RunningElapsed != 2*time.Second || requests[2].ReturnElapsed != 2*time.Second {
		t.Fatalf("incorrect request aging: %+v", requests)
	}
	*requests[0].RemainingLifetime = 0
	if remaining != 3*time.Second {
		t.Fatal("snapshot mutated stored remaining lifetime")
	}
}
