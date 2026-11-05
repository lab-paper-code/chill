package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
)

func TestCoordinatorRejectsMalformedNativeResultAsIntegrityFailure(t *testing.T) {
	t.Setenv("CHILL_FIRSTFLOOR_HELPER", "unknown-field")
	config := testRunConfig(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "termination-log")
	ctx := WithTerminationMessagePath(context.Background(), path)
	if err := RecordTerminationStage(ctx, evidence.StageExecution); err != nil { t.Fatal(err) }
	metricCalls := 0
	store := &fakeStore{}
	coordinator := mustCoordinator(t,
		newTestFirstfloorRuntime(t, filepath.Join(directory, "runtime-result.json")),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
			metricCalls++
			return metricobserver.Result{}, nil
		}),
		testWorkloadReader(t, config), store)
	_, err := coordinator.Execute(ctx, Request{RunConfig: testVerifiedRunConfig(t, config), PodUID: testPodUID,
		ScheduledNodeName: config.TargetNode.Name, RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"}})
	if !errors.Is(err, ErrExecutionContract) || errors.Is(err, ErrRuntimeMeasurement) || metricCalls != 0 || store.calls != 0 {
		t.Fatalf("malformed result err=%v metrics=%d commits=%d", err, metricCalls, store.calls)
	}
	if err := RecordTerminationFailure(ctx, err); err != nil { t.Fatal(err) }
	payload, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	record, ok := evidence.DecodeTerminationMessage(string(payload))
	if !ok || record.Stage != evidence.StageResultValidation || record.FailureReason != evidence.FailureIntegrity {
		t.Fatalf("malformed runtime evidence retained an execution boundary: %#v", record)
	}
}
