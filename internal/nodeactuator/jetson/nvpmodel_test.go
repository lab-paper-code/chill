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
	wantCommand := []string{"/host/ld.so", "--library-path", "/host/lib", "/host/nvpmodel", "-q", "--verbose"}
	if !reflect.DeepEqual(runner.commands, [][]string{wantCommand}) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, wantCommand)
	}
}

// Verbose query excerpts captured from the testbed boards (2026-10-16).
const (
	orinNanoJP6Header = "NVPM VERB: Config file: /etc/nvpmodel.conf\n" +
		"NVPM VERB: parsing done for /etc/nvpmodel.conf\n" +
		"NVPM VERB: Current mode: NV Power Mode: 25W\n1\n" +
		"NVPM VERB: PARAM CPU_ONLINE: ARG CORE_0: PATH /sys/devices/system/cpu/cpu0/online: REAL_VAL: 1 CONF_VAL: 1\n"
	orinNanoJP6Established = orinNanoJP6Header +
		"NVPM VERB: PARAM FBP_POWER_GATING: ARG FBP_PG_MASK: PATH /sys/devices/platform/gpu.0/fbp_pg_mask: REAL_VAL: 2 CONF_VAL: 2\n" +
		"NVPM VERB: PARAM TPC_POWER_GATING: ARG TPC_PG_MASK: PATH /sys/devices/platform/gpu.0/tpc_pg_mask: REAL_VAL: 240 CONF_VAL: 240\n" +
		"NVPM VERB: PARAM GPU_POWER_CONTROL_ENABLE: ARG GPU_PWR_CNTL_EN: PATH /sys/devices/platform/gpu.0/power/control: REAL_VAL: auto CONF_VAL: on\n" +
		"NVPM VERB: PARAM CPU_A78_0: ARG MAX_FREQ: PATH /sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq: REAL_VAL: 1344000 CONF_VAL: 1344000\n"
	// The boot-time nvpmodel.service found the GPU golden context already
	// created and applied nothing; the status file still names 25W.
	orinNanoJP6Nominal = orinNanoJP6Header +
		"NVPM VERB: PARAM FBP_POWER_GATING: ARG FBP_PG_MASK: PATH /sys/devices/platform/gpu.0/fbp_pg_mask: REAL_VAL: 0 CONF_VAL: 2\n" +
		"NVPM VERB: PARAM TPC_POWER_GATING: ARG TPC_PG_MASK: PATH /sys/devices/platform/gpu.0/tpc_pg_mask: REAL_VAL: 0 CONF_VAL: 240\n" +
		"NVPM VERB: PARAM CPU_A78_0: ARG MAX_FREQ: PATH /sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq: REAL_VAL: 1728000 CONF_VAL: 1344000\n"
	agxOrinJP5 = "NVPM VERB: Config file: /etc/nvpmodel.conf\n" +
		"NVPM VERB: parsing done for /etc/nvpmodel.conf\n" +
		"NVPM VERB: Current mode: NV Power Mode: MAXN\n0\n" +
		"NVPM VERB: PARAM TPC_POWER_GATING: ARG TPC_PG_MASK: PATH /sys/devices/gpu.0/tpc_pg_mask: REAL_VAL: 0 CONF_VAL: 0\n" +
		"NVPM VERB: PARAM GPU: ARG MAX_FREQ: PATH /sys/devices/17000000.ga10b/devfreq/17000000.ga10b/max_freq: REAL_VAL: 1300500000 CONF_VAL: 9223372036854775807\n"
	// gv11b reports its fuse-adjusted mask; the kernel logs a write of 1 as
	// "no value change, same mask already set".
	xavierNXJP5 = "NVPM VERB: Config file: /etc/nvpmodel.conf\n" +
		"NVPM VERB: parsing done for /etc/nvpmodel.conf\n" +
		"NVPM VERB: Current mode: NV Power Mode: MODE_15W_6CORE\n2\n" +
		"NVPM VERB: PARAM TPC_POWER_GATING: ARG TPC_PG_MASK: PATH /sys/devices/gpu.0/tpc_pg_mask: REAL_VAL: 4 CONF_VAL: 1\n" +
		"NVPM VERB: PARAM CPU_DENVER_0: ARG MAX_FREQ: PATH /sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq: REAL_VAL: 1420800 CONF_VAL: 1420800\n"
	// R32 prints no power-gating lines.
	nanoJP4 = "NVPM VERB: Config file: /etc/nvpmodel.conf\n" +
		"NVPM VERB: parsing done for /etc/nvpmodel.conf\n" +
		"NVPM WARN: fan mode is not set!\n" +
		"NVPM VERB: Current mode: NV Power Mode: MAXN\n0\n" +
		"NVPM VERB: PARAM CPU_ONLINE: ARG CORE_0: PATH /sys/devices/system/cpu/cpu0/online: REAL_VAL: 1 CONF_VAL: 1\n"
)

func TestNVPModelObserveAcceptsEstablishedModesInEveryBoardFormat(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		output string
		mode   string
		id     int32
	}{
		"orin nano jetpack 6": {orinNanoJP6Established, "25W", 1},
		"agx orin jetpack 5":  {agxOrinJP5, "MAXN", 0},
		"xavier nx jetpack 5": {xavierNXJP5, "MODE_15W_6CORE", 2},
		"nano jetpack 4":      {nanoJP4, "MAXN", 0},
	} {
		t.Run(name, func(t *testing.T) {
			adapter, runner, now := testAdapter(t)
			runner.output = []byte(tc.output)
			observation, err := adapter.Observe(context.Background(), testRequest(now))
			if err != nil {
				t.Fatal(err)
			}
			if observation.PowerMode != tc.mode || observation.Target.JetsonNVPModel.ID != tc.id {
				t.Fatalf("observation = %#v, want %s/%d", observation, tc.mode, tc.id)
			}
		})
	}
}

func TestNVPModelObserveRejectsAModeRecordedWithoutItsGating(t *testing.T) {
	t.Parallel()
	adapter, runner, now := testAdapter(t)
	runner.output = []byte(orinNanoJP6Nominal)
	_, err := adapter.Observe(context.Background(), testRequest(now))
	if err == nil || !strings.Contains(err.Error(), "FBP_POWER_GATING reads 0 where the mode configures 2") ||
		!strings.Contains(err.Error(), "recorded, not established") {
		t.Fatalf("err = %v, want the nominal 25W rejected", err)
	}
}

func TestNVPModelObserveFailsClosedOnUnreadableGating(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"missing conf":    "NVPM VERB: PARAM TPC_POWER_GATING: ARG TPC_PG_MASK: PATH /x: REAL_VAL: 0\n",
		"non-numeric val": "NVPM VERB: PARAM TPC_POWER_GATING: ARG TPC_PG_MASK: PATH /x: REAL_VAL: ? CONF_VAL: 240\n",
	} {
		t.Run(name, func(t *testing.T) {
			adapter, runner, now := testAdapter(t)
			runner.output = []byte(orinNanoJP6Header + line)
			if _, err := adapter.Observe(context.Background(), testRequest(now)); err == nil {
				t.Fatalf("gating line %q was accepted", line)
			}
		})
	}
}

func TestNVPModelApplyFailsWithoutRestartWhenTheBootLeftTheTargetNominal(t *testing.T) {
	t.Parallel()
	adapter, runner, now := scriptedAdapter(t, scriptedResponse{output: orinNanoJP6Nominal})
	err := adapter.Apply(context.Background(), modeRequest(now, "25W", 1), applyTarget(1))
	if err == nil || !strings.Contains(err.Error(), "recorded, not established") {
		t.Fatalf("err = %v, want the nominal target refused", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v, want the query only and no restart", runner.commands)
	}
}

// scriptedRunner answers each call with the next queued response, so a test can
// describe an Apply that queries, is refused, and records the target in turn.
type scriptedRunner struct {
	commands  [][]string
	responses []scriptedResponse
}

type scriptedResponse struct {
	output string
	err    error
}

func (r *scriptedRunner) CombinedOutput(_ context.Context, command []string) ([]byte, error) {
	r.commands = append(r.commands, append([]string(nil), command...))
	if len(r.responses) == 0 {
		return nil, errors.New("no scripted response")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return []byte(response.output), response.err
}

func scriptedAdapter(t *testing.T, responses ...scriptedResponse) (*NVPModel, *scriptedRunner, time.Time) {
	t.Helper()
	adapter, _, now := testAdapter(t)
	scripted := &scriptedRunner{responses: responses}
	adapter.runner = scripted
	adapter.restart = func() error { return errors.New("restart was not expected") }
	return adapter, scripted, now
}

func applyTarget(id int32) nodeactuator.Target {
	return nodeactuator.Target{
		Kind:           nodeactuator.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{ID: id},
	}
}

func modeRequest(now time.Time, mode string, id int32) nodeactuator.Request {
	request := testRequest(now)
	request.PowerMode = mode
	request.Target = applyTarget(id)
	return request
}

func commandArguments(command []string) []string {
	return command[4:]
}

func TestNVPModelApplyPassesOnlyTypedIDAndDoesNotSelfVerify(t *testing.T) {
	t.Parallel()
	adapter, runner, now := scriptedAdapter(t,
		scriptedResponse{output: "NV Power Mode: MAXN\n0\n"},
		scriptedResponse{output: ""},
	)
	if err := adapter.Apply(context.Background(), modeRequest(now, "MODE_50W", 3), applyTarget(3)); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"-q", "--verbose"}, {"-m", "3"}}
	got := [][]string{commandArguments(runner.commands[0]), commandArguments(runner.commands[1])}
	if len(runner.commands) != 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestNVPModelApplyLeavesANodeAlreadyHoldingTheTargetAlone(t *testing.T) {
	t.Parallel()
	adapter, runner, now := scriptedAdapter(t,
		scriptedResponse{output: "NV Power Mode: MODE_30W\n2\n"},
	)
	if err := adapter.Apply(context.Background(), modeRequest(now, "MODE_30W", 2), applyTarget(2)); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 || !reflect.DeepEqual(commandArguments(runner.commands[0]), []string{"-q", "--verbose"}) {
		t.Fatalf("commands = %#v, want a query only", runner.commands)
	}
}

func TestNVPModelApplyRecordsTheTargetAndRestartsWhenTheModeNeedsABoot(t *testing.T) {
	t.Parallel()
	adapter, runner, now := scriptedAdapter(t,
		scriptedResponse{output: "NV Power Mode: MAXN\n0\n"},
		scriptedResponse{output: "NVPM WARN: " + rebootRequiredMarker + ": 2\n", err: errors.New("exit status 234")},
		scriptedResponse{output: "NVPM WARN: rebooting..\n"},
	)
	restarted := false
	adapter.restart = func() error { restarted = true; return nil }

	err := adapter.Apply(context.Background(), modeRequest(now, "MODE_30W", 2), applyTarget(2))
	if err == nil || !strings.Contains(err.Error(), "without restarting the Node") {
		t.Fatalf("err = %v, want a refusal to report success after a restart that returned", err)
	}
	if !restarted {
		t.Fatal("the Node was not restarted")
	}
	want := [][]string{{"-q", "--verbose"}, {"-m", "2"}, {"--force", "-m", "2"}}
	got := make([][]string, 0, len(runner.commands))
	for _, command := range runner.commands {
		got = append(got, commandArguments(command))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}

func TestNVPModelApplyFailsWhenTheNodeCannotBeRestarted(t *testing.T) {
	t.Parallel()
	adapter, _, now := scriptedAdapter(t,
		scriptedResponse{output: "NV Power Mode: MAXN\n0\n"},
		scriptedResponse{output: rebootRequiredMarker + ": 2\n", err: errors.New("exit status 234")},
		scriptedResponse{output: ""},
	)
	adapter.restart = func() error { return errors.New("operation not permitted") }

	err := adapter.Apply(context.Background(), modeRequest(now, "MODE_30W", 2), applyTarget(2))
	if err == nil || !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("err = %v, want the restart failure surfaced", err)
	}
}

func TestNVPModelApplyDoesNotRecordATargetTheToolRefusedForAnotherReason(t *testing.T) {
	t.Parallel()
	adapter, runner, now := scriptedAdapter(t,
		scriptedResponse{output: "NV Power Mode: MAXN\n0\n"},
		scriptedResponse{output: "NVPM ERROR: bad input!\n", err: errors.New("exit status 1")},
	)
	err := adapter.Apply(context.Background(), modeRequest(now, "MODE_30W", 2), applyTarget(2))
	if err == nil || !strings.Contains(err.Error(), "apply NVPModel") {
		t.Fatalf("err = %v, want the typed apply failure", err)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("commands = %#v, want the query and the refused apply only", runner.commands)
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

func TestNVPModelRejectsElapsedTransitionDeadline(t *testing.T) {
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
