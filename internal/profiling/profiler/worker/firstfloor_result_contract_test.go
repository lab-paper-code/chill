package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// pythonActiveResult runs runtime/firstfloor/measurement.py execute_sampled
// with a fake actor (no serving-worker) over a temporary dataset and prints
// the active RuntimeResult it constructs.
const pythonActiveResult = `
import json, sys, time
from pathlib import Path
from unittest import mock
sys.path.insert(0, sys.argv[1])
import measurement
common = measurement.common

request = json.load(sys.stdin)
work = Path(sys.argv[2])
run = request["runConfig"]
for record in ("r0", "r1", "r2"):
    for field in run["state"]["preprocessor"]["inputs"]:
        (work / "dataset" / record).mkdir(parents=True, exist_ok=True)
        (work / "dataset" / record / field).write_bytes(b"sample")
request["datasetPath"] = str(work / "dataset")
for ready in request["observerReadyPaths"] + [request["workloadWindowReadyPath"]]:
    Path(ready).write_text("signal\n")
CPU_MAX, AFFINITY, CPUSET = "400000 100000", [0, 1, 2, 3], "0-3"

class FakeActor(measurement.FirstfloorSession):
    counter = 0
    def json_call(self, method, path, payload=None):
        state, firstfloor = run["state"], run["state"]["firstfloor"]
        if path.endswith("/metadata"):
            return {"model": state["model"], "mode": "profiling",
                    "maxQueueRequests": firstfloor["maxQueueSize"], "maxBatchSize": firstfloor["maxBatchSize"],
                    "policy": {"deadlinePolicy": measurement.deadline_policy(firstfloor)}, "modelInstanceCount": 1,
                    "maxQueueBytes": min(request["maxInputBytes"], measurement.FRAME_BYTES),
                    "maxRequestBytes": measurement.FRAME_BYTES,
                    "runtime": {"runtime": state["backend"], "runtimeVersion": "1.16.0", "numpyVersion": "2.0.0",
                                "modelFiles": sorted("/model/" + Path(f).name for f in state["artifactFiles"]),
                                "inputs": state["preprocessor"]["inputs"], "outputs": state["preprocessor"]["outputs"],
                                "providers": ["CPUExecutionProvider"], "availableProviders": ["CPUExecutionProvider"],
                                "device": "cpu", "cpuAffinity": AFFINITY,
                                "intraOpThreads": run["cpu"]["ortIntraOpThreads"],
                                "interOpThreads": run["cpu"]["ortInterOpThreads"],
                                "initializedBatches": measurement.initialized_batches(run),
                                "warmupIterations": run["measurement"]["warmupIterations"], "synthetic": False,
                                "cgroupCPUMax": CPU_MAX, "cpuSet": CPUSET}}
        if path.endswith("/state"):
            return {"ready": True, "requests": [], "acceptedRequests": FakeActor.counter,
                    "completedRequests": FakeActor.counter, "executions": FakeActor.counter}
        time.sleep(0.002)
        FakeActor.counter += 1
        batch = run["measurement"]["batch"]
        return {"batchSize": batch, "executionComplete": True, "batchIDs": [FakeActor.counter],
                "serviceSamplesMS": [0.7], "preprocessSamplesMS": [0.1], "backendSamplesMS": [0.4],
                "postprocessSamplesMS": [0.1], "nativeSamplesMS": [0.9], "nativeElapsedMS": 0.95,
                "units": [1] * batch}

process = mock.Mock(poll=mock.Mock(return_value=None))
with mock.patch.object(measurement, "FirstfloorSession", FakeActor), \
        mock.patch.object(measurement.subprocess, "Popen", return_value=process), \
        mock.patch.object(measurement, "memory_peak", return_value=(123456, "cgroup-v2-memory.peak:runtime-container-lifetime")), \
        mock.patch.object(common, "cpu_stat", return_value={"usage_usec": 1}), \
        mock.patch.object(common, "validate_cpu_contract", return_value=(CPU_MAX, AFFINITY, CPUSET)):
    result = measurement.execute_sampled(request, work, run, "x86_64", CPU_MAX, AFFINITY, CPUSET, None)
json.dump(result, sys.stdout, allow_nan=False)
`

// TestPythonFirstfloorActiveResultPassesGoValidation pins the active result
// field names Python emits (native sample fields, raw input names, firstfloor
// modelFiles) to the Go strict decode and runtime validation.
func TestPythonFirstfloorActiveResultPassesGoValidation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the Python-to-Go runtime result contract test")
	}
	runtimeDirectory, err := filepath.Abs("../../../../runtime/firstfloor")
	if err != nil {
		t.Fatal(err)
	}
	config := testRunConfig(t)
	config.Measurement.DurationSeconds = 1
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runtime := newTestFirstfloorRuntime(t, filepath.Join(work, "runtime-result.json"))
	runtime.config.MaxLatencySamples = 1 << 20
	request := runtimeRequest(config)
	payload, err := runtime.encodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", pythonActiveResult, runtimeDirectory, work)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python result construction failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var result profilingrun.RuntimeResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("Go rejected the Python result shape: %v", err)
	}
	if err := profilingrun.ValidateRuntime(config, request.PodUID, result); err != nil {
		t.Fatalf("Go rejected the Python result: %v", err)
	}
}
