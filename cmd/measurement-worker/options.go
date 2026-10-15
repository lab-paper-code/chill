package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const (
	defaultModelPath                 = "/model/model.onnx"
	defaultRKNNLiteModelPath         = "/model/model.rknn"
	defaultWorkspacePath             = "/workspace"
	defaultObserverReadyPath         = "/coordination/observer-ready"
	defaultWorkloadObserverReadyPath = "/coordination/workload-observer-ready"
	defaultWorkloadWindowStartPath   = "/coordination/workload-window-start"
	defaultMeasurementStartPath      = "/coordination/measurement-start"
	defaultWorkloadWindowReadyPath   = "/coordination/workload-window-ready"
	defaultMeasurementEndPath        = "/coordination/measurement-end"
	defaultMetricResultPath          = "/coordination/metric-result.json"
	defaultWorkloadResultPath        = "/coordination/workload-result.json"
	defaultTerminationMessagePath    = "/dev/termination-log"
	maximumReceiptBytes              = 4 << 10
	handoffPollInterval              = 100 * time.Millisecond
)

type options struct {
	runConfig                string
	podUID                   string
	scheduledNodeName        string
	modelPath                string
	workspacePath            string
	observerReadyPaths       []string
	workloadWindowStartPath  string
	measurementStartPath     string
	workloadWindowReadyPath  string
	measurementEndPath       string
	metricResultPath         string
	workloadResultPath       string
	terminationMessagePath   string
	firstfloorRuntimeCommand []string
	attemptTimeout           time.Duration
	observerReadyTimeout     time.Duration
	metricResultTimeout      time.Duration
	workloadResultTimeout    time.Duration
	maxLatencySamples        int
	maxInputBytes            int64
	maxRuntimeResultBytes    int64
	maxMetricResultBytes     int64
	maxWorkloadResultBytes   int64
	storeName                string
	storeEndpoint            string
	storeBucket              string
	storeRegion              string
	rawObjectKey             string
	accessKeyID              string
	secretAccessKey          string
	sessionToken             string
}

func (o *options) bindFlags(set *flag.FlagSet) {
	o.modelPath = defaultModelPath
	o.workspacePath = defaultWorkspacePath
	o.observerReadyPaths = []string{defaultObserverReadyPath, defaultWorkloadObserverReadyPath}
	o.workloadWindowStartPath = defaultWorkloadWindowStartPath
	o.measurementStartPath = defaultMeasurementStartPath
	o.workloadWindowReadyPath = defaultWorkloadWindowReadyPath
	o.measurementEndPath = defaultMeasurementEndPath
	o.metricResultPath = defaultMetricResultPath
	o.workloadResultPath = defaultWorkloadResultPath
	o.terminationMessagePath = defaultTerminationMessagePath
	o.firstfloorRuntimeCommand = []string{"python3", "/opt/chill/runtime/firstfloor/measurement.py"}
	o.podUID = os.Getenv("POD_UID")
	o.scheduledNodeName = os.Getenv("NODE_NAME")
	o.accessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	o.secretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	o.sessionToken = os.Getenv("AWS_SESSION_TOKEN")

	set.StringVar(&o.runConfig, "run-config", "", "exact RunConfig generated for this Job")
	set.DurationVar(&o.attemptTimeout, "attempt-timeout", 0, "Job-derived total worker attempt timeout")
	set.DurationVar(&o.observerReadyTimeout, "observer-ready-timeout", 0, "Job-derived observer readiness timeout")
	set.DurationVar(&o.metricResultTimeout, "metric-result-timeout", 0, "Job-derived metric-result handoff timeout")
	set.DurationVar(&o.workloadResultTimeout, "workload-result-timeout", 0, "Job-derived workload-result handoff timeout")
	set.IntVar(&o.maxLatencySamples, "max-latency-samples", 0, "Job-derived retained latency sample capacity")
	set.Int64Var(&o.maxInputBytes, "max-input-bytes", 0, "Job-derived runtime input byte capacity")
	set.Int64Var(&o.maxRuntimeResultBytes, "max-runtime-result-bytes", 0, "Job-derived runtime result byte capacity")
	set.Int64Var(&o.maxMetricResultBytes, "max-metric-result-bytes", 0, "Job-derived metric result byte capacity")
	set.Int64Var(&o.maxWorkloadResultBytes, "max-workload-result-bytes", 0, "Job-derived workload result byte capacity")
	set.StringVar(&o.storeName, "store-name", "", "logical CHILL Evidence Store identity")
	set.StringVar(&o.storeEndpoint, "store-endpoint", "", "resolved internal Evidence Store endpoint")
	set.StringVar(&o.storeBucket, "store-bucket", "", "resolved physical evidence bucket")
	set.StringVar(&o.storeRegion, "store-region", "", "resolved Evidence Store region")
	set.StringVar(&o.rawObjectKey, "raw-object-key", "", "deterministic raw-measurement object key")
}

func (o options) validate() error {
	for _, field := range []struct{ name, value string }{
		{name: "RunConfig", value: o.runConfig},
		{name: "Pod UID", value: o.podUID},
		{name: "scheduled Node name", value: o.scheduledNodeName},
		{name: "store name", value: o.storeName},
		{name: "store endpoint", value: o.storeEndpoint},
		{name: "store bucket", value: o.storeBucket},
		{name: "store region", value: o.storeRegion},
		{name: "raw object key", value: o.rawObjectKey},
		{name: "access key ID", value: o.accessKeyID},
		{name: "secret access key", value: o.secretAccessKey},
	} {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return errors.New(field.name + " is required without surrounding whitespace")
		}
	}
	if o.sessionToken != strings.TrimSpace(o.sessionToken) {
		return errors.New("session token must not have surrounding whitespace")
	}
	for name, value := range map[string]string{
		"model": o.modelPath, "workspace": o.workspacePath,
		"workload-window-start": o.workloadWindowStartPath,
		"measurement-start":     o.measurementStartPath, "measurement-end": o.measurementEndPath,
		"workload-window-ready": o.workloadWindowReadyPath,
		"metric result":         o.metricResultPath, "workload result": o.workloadResultPath,
		"termination message": o.terminationMessagePath,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return errors.New(name + " path must be a clean absolute path")
		}
	}
	if len(o.observerReadyPaths) == 0 {
		return errors.New("at least one observer-ready path is required")
	}
	seenReadyPaths := map[string]struct{}{}
	for _, readyPath := range o.observerReadyPaths {
		if !filepath.IsAbs(readyPath) || filepath.Clean(readyPath) != readyPath {
			return errors.New("observer-ready path must be a clean absolute path")
		}
		if _, exists := seenReadyPaths[readyPath]; exists {
			return errors.New("observer-ready paths must be unique")
		}
		seenReadyPaths[readyPath] = struct{}{}
	}
	if o.attemptTimeout <= 0 || o.observerReadyTimeout <= 0 || o.metricResultTimeout <= handoffPollInterval ||
		o.workloadResultTimeout <= handoffPollInterval {
		return errors.New("Job-derived worker timeouts are invalid")
	}
	if o.observerReadyTimeout >= o.attemptTimeout || o.metricResultTimeout >= o.attemptTimeout ||
		o.workloadResultTimeout >= o.attemptTimeout {
		return errors.New("handoff timeouts must be shorter than the total worker attempt timeout")
	}
	if o.maxLatencySamples < 1 || o.maxInputBytes < 1 ||
		o.maxRuntimeResultBytes < 1 || o.maxMetricResultBytes < 1 || o.maxWorkloadResultBytes < 1 {
		return errors.New("Job-derived worker capacities must be positive")
	}
	if err := (evidencestore.ObjectLocation{Store: o.storeName, Key: o.rawObjectKey}).Validate(); err != nil {
		return err
	}
	return nil
}

func (o options) runtimeResultPath() string {
	return filepath.Join(o.workspacePath, "runtime-result.json")
}
