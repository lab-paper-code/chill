package fixtures

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// RealFirstfloorCommand returns a local CPURuntime entrypoint using the real
// native actor, IPC runner and ORT CPU backend. The generated Identity model is
// only a test fixture: its basename and raw fields match the frozen catalog,
// not a claim that this is the catalog's deployed MobileNet artifact.
// Python 3, numpy, onnx and onnxruntime, plus readable host cgroup accounting,
// are required. Missing dependencies fail the test rather than synthesizing data.
func RealFirstfloorCommand(t *testing.T) []string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate real firstfloor fixture source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	directory := t.TempDir()
	for _, name := range []string{"model", "preprocessor", "dataset"} {
		if err := os.Mkdir(filepath.Join(directory, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(directory, "preprocessor", "preprocessor.py"), realFirstfloorPreprocessor)
	for _, record := range []string{"one", "two", "three", "four"} {
		path := filepath.Join(directory, "dataset", record)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(path, "image"), "1")
	}
	command := exec.Command("python3", "-c", realFirstfloorModel,
		filepath.Join(directory, "model", "mobilenet-v2-050.onnx"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate genuine ONNX test fixture (requires numpy, onnx, onnxruntime): %v\n%s", err, output)
	}
	wrapper := filepath.Join(directory, "measurement.py")
	write(wrapper, realFirstfloorMeasurement)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"python3", wrapper, root, directory, executable}
}

// RunRealFirstfloorWorkerHelper is called by a consumer's dedicated
// TestRealFirstfloorWorkerHelper. Normal test discovery does nothing; the local
// measurement entrypoint starts that same test executable as the native worker.
func RunRealFirstfloorWorkerHelper(t *testing.T) {
	t.Helper()
	if os.Getenv("CHILL_REAL_FIRSTFLOOR_HELPER") != "1" {
		return
	}
	arguments := os.Args
	for len(arguments) > 0 && arguments[0] != "--" {
		arguments = arguments[1:]
	}
	if len(arguments) == 0 {
		t.Fatal("native worker helper requires arguments after --")
	}
	flags := flag.NewFlagSet("real-firstfloor-worker", flag.ContinueOnError)
	config := flags.String("config", "", "worker configuration")
	address := flags.String("bind-address", "", "HTTP listener")
	node := flags.String("node-name", "", "scheduled node")
	sizeSaturation := flags.Bool("size-saturation", false, "read saturation sizing inputs from stdin")
	if err := flags.Parse(arguments[1:]); err != nil {
		t.Fatal(err)
	}
	if *sizeSaturation {
		if err := realFirstfloorSaturation(os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// This helper is a CLI process; the test harness must not append PASS.
		os.Exit(0)
	}
	if *config == "" || *address == "" || *node == "" {
		t.Fatal("native worker helper requires config, bind-address and node-name")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := firstfloor.ReadConfig(*config)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runner, err := firstfloor.StartRunner(workerCtx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := firstfloor.NewScheduler(workerCtx, cfg, runner)
	server := &http.Server{Addr: *address, Handler: firstfloor.NewHandler(scheduler),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	select {
	case err = <-serverErr:
	case <-scheduler.Done():
		err = errors.New("native runner stopped")
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, shutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdown()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		if err == nil {
			err = shutdownErr
		}
	}
	<-scheduler.Done()
	if ctx.Err() == nil && err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func realFirstfloorSaturation(input io.Reader, output io.Writer) error {
	var request struct {
		Batch                          int     `json:"batch"`
		MaxQueueRequests               int     `json:"maxQueueRequests"`
		MaxQueueBytes                  int64   `json:"maxQueueBytes"`
		FramedDecodedRequestUpperBytes int64   `json:"framedDecodedRequestUpperBytes"`
		RequestLifetimeSeconds         float64 `json:"requestLifetimeSeconds"`
		DeliveryMarginMilliseconds     float64 `json:"deliveryMarginMilliseconds"`
		MaxBatchServiceSeconds         float64 `json:"maxBatchServiceSeconds"`
	}
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("exactly one saturation sizing JSON object required")
	}
	workers, limitingBound, err := firstfloor.SaturationWorkers(request.Batch, request.MaxQueueRequests,
		request.MaxQueueBytes, request.FramedDecodedRequestUpperBytes, request.RequestLifetimeSeconds,
		request.DeliveryMarginMilliseconds, request.MaxBatchServiceSeconds)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Batch                          int     `json:"batch"`
		MaxBatchServiceSeconds         float64 `json:"maxBatchServiceSeconds"`
		ServiceTimeSource              string  `json:"serviceTimeSource"`
		RequestLifetimeSeconds         float64 `json:"requestLifetimeSeconds"`
		MarginSeconds                  float64 `json:"marginSeconds"`
		WorkerCap                      int     `json:"workerCap"`
		QueueBytesCap                  int64   `json:"queueBytesCap"`
		FramedDecodedRequestUpperBytes int64   `json:"framedDecodedRequestUpperBytes"`
		Workers                        int     `json:"workers"`
		LimitingBound                  string  `json:"limitingBound"`
	}{
		Batch: request.Batch, MaxBatchServiceSeconds: request.MaxBatchServiceSeconds,
		ServiceTimeSource: "run.sparse.maxObservedSeconds", RequestLifetimeSeconds: request.RequestLifetimeSeconds,
		MarginSeconds: request.DeliveryMarginMilliseconds / 1000, WorkerCap: 64,
		QueueBytesCap: request.MaxQueueBytes, FramedDecodedRequestUpperBytes: request.FramedDecodedRequestUpperBytes,
		Workers: workers, LimitingBound: limitingBound,
	})
}

const realFirstfloorModel = `
import sys
import numpy
import onnx
import onnxruntime
from onnx import TensorProto, helper

x = helper.make_tensor_value_info("x", TensorProto.FLOAT, ["batch", 1])
y = helper.make_tensor_value_info("y", TensorProto.FLOAT, ["batch", 1])
graph = helper.make_graph([helper.make_node("Identity", ["x"], ["y"])],
                          "chill-real-firstfloor-test-fixture", [x], [y])
model = helper.make_model(graph, producer_name="chill-test-fixture",
                          opset_imports=[helper.make_opsetid("", 13)], ir_version=8)
onnx.checker.check_model(model)
onnx.save(model, sys.argv[1])
`

const realFirstfloorPreprocessor = `
import numpy as np

WARMUP_SAMPLE = {"image": b"1"}


def preprocess(samples):
    values = np.asarray([[float(sample["image"].decode("ascii"))] for sample in samples], dtype=np.float32)
    return {"x": values}, [1 for _ in samples]


def infer(sessions, model_inputs, max_new_tokens, first_token):
    sessions = list(sessions.values())
    return sessions[0].run(["y"], model_inputs)[0]


def postprocess(values):
    return [{"label": row.tobytes()} for row in values]
`

// Relocation is the sole adaptation to container execution. Measurement clocks,
// memory accounting, provider facts, native samples and the AND stop loop remain
// the production implementations. Only model path spelling is restored on output
// so the strict consumer sees the frozen container mount, not t.TempDir.
const realFirstfloorMeasurement = `
import os
from pathlib import Path
import subprocess
import sys

root, directory, executable = sys.argv[1:4]
del sys.argv[1:4]
sys.path.insert(0, str(Path(root) / "runtime"))
from firstfloor import measurement

relocations = {"/model": str(Path(directory) / "model"),
               "/preprocessor": str(Path(directory) / "preprocessor"),
               "/dataset": str(Path(directory) / "dataset"),
               "/opt/chill/runtime/firstfloor/runner.py": str(Path(root) / "runtime/firstfloor/runner.py")}
decode_request = measurement.common.decode_request


def local_request(source):
    request = decode_request(source)
    for key in ("modelDirectory", "preprocessorPath", "datasetPath"):
        if key in request:
            request[key] = relocations[request[key]]
    actor = request["runConfig"]["actor"]
    actor["runnerCommand"] = [relocations.get(value, value) for value in actor["runnerCommand"]]
    return request


popen = subprocess.Popen


def local_worker(command, *args, **kwargs):
    if command[0] == "/opt/chill/bin/serving-worker":
        command = [executable, "-test.run=^TestRealFirstfloorWorkerHelper$", "-test.paniconexit0=false", "--"] + command[1:]
        environment = dict(os.environ if kwargs.get("env") is None else kwargs["env"])
        environment["CHILL_REAL_FIRSTFLOOR_HELPER"] = "1"
        kwargs["env"] = environment
    return popen(command, *args, **kwargs)


encode_result = measurement.common.encode_result


def container_paths(result, max_bytes):
    metadata = result["firstfloor"]["runtime"]
    metadata["modelFiles"] = [str(Path("/model") / Path(value).name) for value in metadata["modelFiles"]]
    return encode_result(result, max_bytes)


measurement.common.decode_request = local_request
measurement.subprocess.Popen = local_worker
measurement.common.encode_result = container_paths
measurement.main()
`
