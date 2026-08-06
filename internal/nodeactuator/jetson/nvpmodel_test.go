package jetson

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
)

type recordingRunner struct {
	commands [][]string
	output   []byte
	err      error
}

func (r *recordingRunner) CombinedOutput(_ context.Context, command []string) ([]byte, error) {
	r.commands = append(r.commands, append([]string(nil), command...))
	return append([]byte(nil), r.output...), r.err
}

func TestNVPModelObserveParsesIndependentNameAndID(t *testing.T) {
	t.Parallel()
	adapter, runner, now := testAdapter(t)
	runner.output = []byte("NVPM WARN: fan mode is not set!\nNV Power Mode: MODE_15W_6CORE\n2\n")

	observation, err := adapter.Observe(context.Background(), testRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	if observation.PowerMode != "MODE_15W_6CORE" || observation.Target.JetsonNVPModel == nil ||
		observation.Target.JetsonNVPModel.ID != 2 || !observation.ObservedAt.Equal(now) {
		t.Fatalf("observation = %#v", observation)
	}
	wantCommand := []string{"/host/ld.so", "--library-path", "/host/lib", "/host/nvpmodel", "-q"}
	if !reflect.DeepEqual(runner.commands, [][]string{wantCommand}) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, wantCommand)
	}
}

func TestNVPModelApplyPassesOnlyTypedIDAndDoesNotSelfVerify(t *testing.T) {
	t.Parallel()
	adapter, runner, now := testAdapter(t)
	target := nodeactuator.Target{
		Kind: nodeactuator.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{
			ID: 8,
		},
	}
	if err := adapter.Apply(context.Background(), testRequest(now), target); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"/host/ld.so", "--library-path", "/host/lib", "/host/nvpmodel", "-m", "8"}}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestNVPModelFailsClosedOnMalformedOrAmbiguousQuery(t *testing.T) {
	t.Parallel()
	for name, output := range map[string]string{
		"missing id":     "NV Power Mode: MAXN\n",
		"missing name":   "0\n",
		"duplicate id":   "NV Power Mode: MAXN\n0\n1\n",
		"duplicate name": "NV Power Mode: MAXN\nNV Power Mode: 5W\n0\n",
	} {
		t.Run(name, func(t *testing.T) {
			adapter, runner, now := testAdapter(t)
			runner.output = []byte(output)
			if _, err := adapter.Observe(context.Background(), testRequest(now)); err == nil {
				t.Fatalf("query %q was accepted", output)
			}
		})
	}
}

func TestNVPModelPropagatesBoundedCommandFailure(t *testing.T) {
	t.Parallel()
	adapter, runner, now := testAdapter(t)
	runner.output = []byte(strings.Repeat("x", maximumDiagnosticOutput+100))
	runner.err = errors.New("exit status 1")
	_, err := adapter.Observe(context.Background(), testRequest(now))
	if err == nil || !strings.Contains(err.Error(), "exit status 1") ||
		!strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("command error = %v", err)
	}
}

func TestNVPModelRejectsElapsedHandoffDeadline(t *testing.T) {
	t.Parallel()
	adapter, runner, now := testAdapter(t)
	request := testRequest(now)
	request.Deadline = now
	if _, err := adapter.Observe(context.Background(), request); err == nil || len(runner.commands) != 0 {
		t.Fatalf("elapsed request error=%v commands=%#v", err, runner.commands)
	}
}

func testAdapter(t *testing.T) (*NVPModel, *recordingRunner, time.Time) {
	t.Helper()
	adapter, err := NewNVPModel(NVPModelOptions{
		Command: []string{"/host/ld.so", "--library-path", "/host/lib", "/host/nvpmodel"},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	runner := &recordingRunner{}
	adapter.runner, adapter.now = runner, func() time.Time { return now }
	return adapter, runner, now
}

func testRequest(now time.Time) nodeactuator.Request {
	return nodeactuator.Request{
		Target: nodeactuator.Target{
			Kind: nodeactuator.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{
				ID: 2,
			},
		},
		Deadline: now.Add(time.Minute),
	}
}
