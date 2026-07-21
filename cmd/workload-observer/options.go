package main

import (
	"errors"
	"flag"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
)

const (
	defaultIntentPath                  = "/etc/chill/run/intent.json"
	defaultReadyPath                   = "/coordination/workload-observer-ready"
	defaultWindowStartSignalPath       = "/coordination/workload-window-start"
	defaultWindowReadyPath             = "/coordination/workload-window-ready"
	defaultEndSignalPath               = "/coordination/measurement-end"
	defaultWorkloadResultPath          = "/coordination/workload-result.json"
	maximumIntentBytes           int64 = 1 << 20
)

var canonicalDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type options struct {
	intentPath       string
	intentDigest     string
	readyPath        string
	windowStartPath  string
	windowReadyPath  string
	endPath          string
	resultPath       string
	policyDigest     string
	selectorLabelCSV string
	endWaitTimeout   time.Duration
	endFenceTimeout  time.Duration
	maxEvents        int
	maxResultBytes   int64
	kubeAPIServer    string
	kubeAPITokenFile string
	kubeAPICAFile    string
	podNamespace     string
	podName          string
	podUID           string
}

func (o *options) bindFlags(set *flag.FlagSet) {
	set.StringVar(&o.intentPath, "intent-file", defaultIntentPath, "read-only frozen Run intent JSON")
	set.StringVar(&o.intentDigest, "intent-digest", "", "expected frozen Run intent SHA-256")
	set.StringVar(&o.readyPath, "ready-file", defaultReadyPath, "workload-observer-ready signal path")
	set.StringVar(
		&o.windowStartPath, "window-start-signal-file", defaultWindowStartSignalPath,
		"workload-window-start request signal path",
	)
	set.StringVar(
		&o.windowReadyPath, "window-ready-file", defaultWindowReadyPath,
		"measurement-bound workload-window-ready signal path",
	)
	set.StringVar(&o.endPath, "end-signal-file", defaultEndSignalPath, "measurement-end signal path")
	set.StringVar(&o.resultPath, "result-file", defaultWorkloadResultPath, "atomic WorkloadObserver result path")
	set.StringVar(&o.policyDigest, "policy-digest", "", "frozen allowed-background policy SHA-256")
	set.StringVar(&o.selectorLabelCSV, "selector-label-keys", "", "sorted comma-separated policy label keys to retain")
	set.DurationVar(&o.endWaitTimeout, "end-wait-timeout", 0, "Job-derived maximum wait for measurement-end")
	set.DurationVar(
		&o.endFenceTimeout,
		"end-fence-timeout",
		0,
		"Job-derived maximum wait for the closing Kubernetes bookmark",
	)
	set.IntVar(&o.maxEvents, "max-events", 0, "Job-derived maximum retained material Pod events")
	set.Int64Var(&o.maxResultBytes, "max-result-bytes", 0, "Job-derived maximum workload result bytes")
	set.StringVar(&o.kubeAPIServer, "kube-api-server", "", "Kubernetes API URL; empty uses standard in-cluster config")
	set.StringVar(
		&o.kubeAPITokenFile,
		"kube-api-token-file",
		kubeconfig.DefaultServiceAccountTokenFile,
		"service account bearer token path",
	)
	set.StringVar(&o.kubeAPICAFile, "kube-api-ca-file", kubeconfig.DefaultServiceAccountCAFile, "Kubernetes API CA path")
}

func (o options) validate() error {
	for name, value := range map[string]string{
		"Pod namespace": o.podNamespace,
		"Pod name":      o.podName,
		"Pod UID":       o.podUID,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return errors.New(name + " is required without surrounding whitespace")
		}
	}
	if !canonicalDigest.MatchString(o.intentDigest) {
		return errors.New("intent digest must be a canonical SHA-256")
	}
	if !canonicalDigest.MatchString(o.policyDigest) {
		return errors.New("policy digest must be a canonical SHA-256")
	}
	if o.endWaitTimeout <= 0 || o.endFenceTimeout <= 0 {
		return errors.New("end wait and fence timeouts must be supplied by the Job execution contract")
	}
	if o.maxEvents < 1 || o.maxResultBytes < 1 {
		return errors.New("event and result-size bounds must be supplied by the Job execution contract")
	}
	paths := map[string]string{
		"ready": o.readyPath, "workload-window-start": o.windowStartPath,
		"workload-window-ready": o.windowReadyPath, "measurement-end": o.endPath,
		"result": o.resultPath,
	}
	seenPaths := make(map[string]struct{}, len(paths))
	for name, value := range paths {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
			return errors.New(name + " path must be a clean absolute file path")
		}
		if _, exists := seenPaths[value]; exists {
			return errors.New("workload observer coordination paths must be unique")
		}
		seenPaths[value] = struct{}{}
	}
	_, err := o.selectorLabelKeys()
	return err
}

func (o options) selectorLabelKeys() ([]string, error) {
	if o.selectorLabelCSV == "" {
		return nil, nil
	}
	keys := strings.Split(o.selectorLabelCSV, ",")
	if !sort.StringsAreSorted(keys) {
		return nil, errors.New("selector label keys must be sorted")
	}
	previous := ""
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key || key == previous {
			return nil, errors.New("selector label keys must be non-empty, unique, and without whitespace")
		}
		previous = key
	}
	return keys, nil
}
