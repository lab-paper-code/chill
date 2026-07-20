package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const (
	defaultIntentPath                   = "/etc/chill/run/intent.json"
	defaultModelPath                    = "/model/model.onnx"
	defaultWorkspacePath                = "/workspace"
	defaultObserverReadyPath            = "/coordination/observer-ready"
	defaultMeasurementStartPath         = "/coordination/measurement-start"
	defaultMetricResultPath             = "/coordination/metric-result.json"
	defaultTerminationMessagePath       = "/dev/termination-log"
	maximumIntentBytes            int64 = 1 << 20
	maximumReceiptBytes                 = 4 << 10
	handoffPollInterval                 = 100 * time.Millisecond
)

var canonicalDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type options struct {
	intentPath             string
	intentDigest           string
	podUID                 string
	scheduledNodeName      string
	modelPath              string
	workspacePath          string
	observerReadyPath      string
	measurementStartPath   string
	metricResultPath       string
	terminationMessagePath string
	runtimeCommand         []string
	attemptTimeout         time.Duration
	observerReadyTimeout   time.Duration
	metricResultTimeout    time.Duration
	maxLatencySamples      int
	maxInputBytes          int64
	maxRuntimeResultBytes  int64
	maxMetricResultBytes   int64
	storeName              string
	storeEndpoint          string
	storeBucket            string
	storeRegion            string
	rawObjectKey           string
	accessKeyID            string
	secretAccessKey        string
	sessionToken           string
}

func (o *options) bindFlags(set *flag.FlagSet) {
	o.intentPath = defaultIntentPath
	o.modelPath = defaultModelPath
	o.workspacePath = defaultWorkspacePath
	o.observerReadyPath = defaultObserverReadyPath
	o.measurementStartPath = defaultMeasurementStartPath
	o.metricResultPath = defaultMetricResultPath
	o.terminationMessagePath = defaultTerminationMessagePath
	o.runtimeCommand = []string{"python3", "/opt/chill/runtime/ortcpu/runner.py"}
	o.podUID = os.Getenv("POD_UID")
	o.scheduledNodeName = os.Getenv("NODE_NAME")
	o.accessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	o.secretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	o.sessionToken = os.Getenv("AWS_SESSION_TOKEN")

	set.StringVar(&o.intentDigest, "intent-digest", "", "expected frozen Run intent SHA-256")
	set.DurationVar(&o.attemptTimeout, "attempt-timeout", 0, "Job-derived total worker attempt timeout")
	set.DurationVar(&o.observerReadyTimeout, "observer-ready-timeout", 0, "Job-derived observer readiness timeout")
	set.DurationVar(&o.metricResultTimeout, "metric-result-timeout", 0, "Job-derived metric-result handoff timeout")
	set.IntVar(&o.maxLatencySamples, "max-latency-samples", 0, "Job-derived retained latency sample capacity")
	set.Int64Var(&o.maxInputBytes, "max-input-bytes", 0, "Job-derived runtime input byte capacity")
	set.Int64Var(&o.maxRuntimeResultBytes, "max-runtime-result-bytes", 0, "Job-derived runtime result byte capacity")
	set.Int64Var(&o.maxMetricResultBytes, "max-metric-result-bytes", 0, "Job-derived metric result byte capacity")
	set.StringVar(&o.storeName, "store-name", "", "logical CHILL Evidence Store identity")
	set.StringVar(&o.storeEndpoint, "store-endpoint", "", "resolved internal Evidence Store endpoint")
	set.StringVar(&o.storeBucket, "store-bucket", "", "resolved physical evidence bucket")
	set.StringVar(&o.storeRegion, "store-region", "", "resolved Evidence Store region")
	set.StringVar(&o.rawObjectKey, "raw-object-key", "", "deterministic raw-measurement object key")
}

func (o options) validate() error {
	if !canonicalDigest.MatchString(o.intentDigest) {
		return errors.New("intent digest must be a canonical SHA-256")
	}
	for _, field := range []struct{ name, value string }{
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
		"intent": o.intentPath, "model": o.modelPath, "workspace": o.workspacePath,
		"observer-ready": o.observerReadyPath, "measurement-start": o.measurementStartPath,
		"metric result": o.metricResultPath, "termination message": o.terminationMessagePath,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return errors.New(name + " path must be a clean absolute path")
		}
	}
	if o.attemptTimeout <= 0 || o.observerReadyTimeout <= 0 || o.metricResultTimeout <= handoffPollInterval {
		return errors.New("Job-derived worker timeouts are invalid")
	}
	if o.observerReadyTimeout >= o.attemptTimeout || o.metricResultTimeout >= o.attemptTimeout {
		return errors.New("handoff timeouts must be shorter than the total worker attempt timeout")
	}
	if o.maxLatencySamples < 1 || o.maxInputBytes < 1 ||
		o.maxRuntimeResultBytes < 1 || o.maxMetricResultBytes < 1 {
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
