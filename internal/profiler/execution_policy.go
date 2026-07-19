package profiler

import (
	"errors"
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const (
	CPUORTWorkerPolicy        = "cpu-ort-worker"
	CPUORTWorkerPolicyVersion = "v1"

	mebibyte         = int64(1024 * 1024)
	maxPowerEventsV1 = int64(50_000)
)

// ExecutionPlan is the bounded delivery policy for one already-frozen Run.
// It contains no Kubernetes objects; the controller adapter projects it into
// one native Job without inventing independent resource or timeout values.
type ExecutionPlan struct {
	Policy        string
	PolicyVersion string

	ActiveDeadline       time.Duration
	AttemptTimeout       time.Duration
	ObserverReadyTimeout time.Duration
	ObserverStartTimeout time.Duration
	PowerResultTimeout   time.Duration

	MaxLatencySamples     int
	MaxInputBytes         int64
	MaxRuntimeResultBytes int64
	MaxPowerResultBytes   int64
	MaxPowerEvents        int

	RuntimeMemoryRequestBytes  int64
	RuntimeMemoryLimitBytes    int64
	ObserverCPURequest         string
	ObserverCPULimit           string
	ObserverMemoryRequestBytes int64
	ObserverMemoryLimitBytes   int64
	CoordinationBytes          int64
	WorkspaceBytes             int64
}

// DeriveExecutionPlan applies the first measured CPU ORT worker envelope. CPU
// request and limit remain Node-specific frozen facts; only worker overhead is
// fixed by this versioned implementation policy.
func DeriveExecutionPlan(intent profilingrun.Intent) (ExecutionPlan, error) {
	if err := intent.Validate(); err != nil {
		return ExecutionPlan{}, fmt.Errorf("validate frozen Run intent: %w", err)
	}
	if intent.SchemaVersion != profilingrun.SchemaVersion ||
		intent.Candidate.ExecutionPath != "ort-cpu" ||
		intent.State.RuntimeFamily != "onnxruntime" ||
		intent.State.Backend != "CPUExecutionProvider" {
		return ExecutionPlan{}, errors.New("cpu-ort-worker v1 requires the current CPU ONNX Runtime intent")
	}

	powerDuration := time.Duration(intent.Power.DurationSeconds) * time.Second
	interval := time.Duration(intent.Power.IntervalMilliseconds) * time.Millisecond
	maxEvents64 := int64(powerDuration/interval) + 2
	if maxEvents64 < 1 || maxEvents64 > maxPowerEventsV1 || maxEvents64 > int64(^uint(0)>>1) {
		return ExecutionPlan{}, errors.New("power observation schedule exceeds worker event capacity")
	}

	return ExecutionPlan{
		Policy:        CPUORTWorkerPolicy,
		PolicyVersion: CPUORTWorkerPolicyVersion,

		ActiveDeadline:       powerDuration + 3*time.Minute,
		AttemptTimeout:       powerDuration + 150*time.Second,
		ObserverReadyTimeout: 30 * time.Second,
		ObserverStartTimeout: 2 * time.Minute,
		PowerResultTimeout:   powerDuration - time.Duration(intent.Measurement.DurationSeconds)*time.Second + 30*time.Second,

		MaxLatencySamples:     1_000_000,
		MaxInputBytes:         64 * mebibyte,
		MaxRuntimeResultBytes: 32 * mebibyte,
		MaxPowerResultBytes:   8 * mebibyte,
		MaxPowerEvents:        int(maxEvents64),

		RuntimeMemoryRequestBytes:  128 * mebibyte,
		RuntimeMemoryLimitBytes:    1024 * mebibyte,
		ObserverCPURequest:         "5m",
		ObserverCPULimit:           "100m",
		ObserverMemoryRequestBytes: 16 * mebibyte,
		ObserverMemoryLimitBytes:   64 * mebibyte,
		CoordinationBytes:          16 * mebibyte,
		WorkspaceBytes:             128 * mebibyte,
	}, nil
}
