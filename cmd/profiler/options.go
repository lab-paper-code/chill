package main

import (
	"errors"
	"flag"
	"math"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

type options struct {
	metricsAddr                               string
	probeAddr                                 string
	systemName                                string
	watchNamespace                            string
	enableLeaderElection                      bool
	leaderElectionID                          string
	factSourceConfigMap                       string
	executionFactsKey                         string
	metricSourcesKey                          string
	workloadObservationKey                    string
	executionPoliciesKey                      string
	actuationPoliciesKey                      string
	workloadObserverServiceAccount            string
	workloadObserverKubeAPIServer             string
	transitionObserverImage                   string
	transitionObserverPullPolicy              string
	transitionObserverNodeName                string
	transitionObserverServiceAccount          string
	transitionObserverDeadlineOverheadSeconds int64
	transitionMaximumObjectBytes              int64
	failedJobRetentionSeconds                 int
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to.")
	flags.IntVar(&o.failedJobRetentionSeconds, "failed-job-retention-seconds", 86400,
		"Failed measurement Job retention in seconds (1..604800).")
	flags.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the health probes bind to.")
	flags.StringVar(&o.systemName, "system-name", "", "ChillSystem name that owns this Profiler instance.")
	flags.StringVar(
		&o.watchNamespace,
		"watch-namespace",
		os.Getenv("POD_NAMESPACE"),
		"Namespace containing CHILL profiling resources.",
	)
	flags.StringVar(
		&o.workloadObserverServiceAccount,
		"workload-observer-service-account",
		"",
		"ServiceAccount identity used by measurement Jobs for WorkloadObserver API reads.",
	)
	flags.StringVar(
		&o.workloadObserverKubeAPIServer,
		"workload-observer-kube-api-server",
		"",
		"Kubernetes API URL supplied to edge-hosted WorkloadObserver containers.",
	)
	flags.StringVar(
		&o.transitionObserverImage,
		"transition-observer-image",
		"",
		"Profiler image containing the transition-observer binary.",
	)
	flags.StringVar(
		&o.transitionObserverPullPolicy,
		"transition-observer-image-pull-policy",
		"",
		"Image pull policy for transition-observer Jobs.",
	)
	flags.StringVar(
		&o.transitionObserverNodeName,
		"transition-observer-node-name",
		"",
		"Exact off-target Node for transition-observer Jobs.",
	)
	flags.StringVar(
		&o.transitionObserverServiceAccount,
		"transition-observer-service-account",
		"",
		"Dedicated ServiceAccount for transition-observer Jobs.",
	)
	flags.Int64Var(
		&o.transitionObserverDeadlineOverheadSeconds,
		"transition-observer-active-deadline-overhead-seconds",
		0,
		"Operational Job deadline overhead beyond the frozen power observation.",
	)
	flags.Int64Var(
		&o.transitionMaximumObjectBytes,
		"transition-maximum-object-bytes",
		0,
		"Maximum bytes for one retained transition object and observer workspace.",
	)
	flags.BoolVar(&o.enableLeaderElection, "leader-elect", false, "Enable leader election for the Profiler controller.")
	flags.StringVar(&o.leaderElectionID, "leader-election-id", "profiler.edge.dacs.io", "Leader election lock identity.")
	flags.StringVar(&o.factSourceConfigMap, "fact-source-config-map", "", "Profiler fact-source ConfigMap name.")
	flags.StringVar(&o.executionFactsKey, "execution-facts-key", "", "ConfigMap key containing execution facts.")
	flags.StringVar(&o.metricSourcesKey, "metric-sources-key", "", "ConfigMap key containing Node metric sources.")
	flags.StringVar(
		&o.executionPoliciesKey,
		"execution-policies-key",
		"",
		"ConfigMap key containing versioned worker execution envelopes.",
	)
	flags.StringVar(
		&o.actuationPoliciesKey,
		"actuation-policies-key",
		"",
		"ConfigMap key containing versioned Node actuation policies.",
	)
	flags.StringVar(
		&o.workloadObservationKey,
		"workload-observation-key",
		"",
		"ConfigMap key containing the WorkloadObserver image.",
	)
}

func (o options) validate() error {
	if o.failedJobRetentionSeconds < 1 || o.failedJobRetentionSeconds > 604800 {
		return errors.New("failed-job-retention-seconds must be between 1 and 604800")
	}
	required := []struct {
		name  string
		value string
	}{
		{name: "system name", value: o.systemName},
		{name: "watch namespace", value: o.watchNamespace},
		{name: "fact-source ConfigMap", value: o.factSourceConfigMap},
		{name: "execution-facts key", value: o.executionFactsKey},
		{name: "metric-sources key", value: o.metricSourcesKey},
		{name: "workload-observation key", value: o.workloadObservationKey},
		{name: "execution-policies key", value: o.executionPoliciesKey},
		{name: "actuation-policies key", value: o.actuationPoliciesKey},
		{name: "workload-observer ServiceAccount", value: o.workloadObserverServiceAccount},
		{name: "transition-observer image", value: o.transitionObserverImage},
		{name: "transition-observer image pull policy", value: o.transitionObserverPullPolicy},
		{name: "transition-observer Node", value: o.transitionObserverNodeName},
		{name: "transition-observer ServiceAccount", value: o.transitionObserverServiceAccount},
	}
	if strings.TrimSpace(o.workloadObserverKubeAPIServer) != o.workloadObserverKubeAPIServer {
		return errors.New("workload-observer Kubernetes API server must not have surrounding whitespace")
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return errors.New(field.name + " is required without surrounding whitespace")
		}
	}
	keys := map[string]struct{}{}
	for _, key := range []string{
		o.executionFactsKey, o.metricSourcesKey, o.workloadObservationKey,
		o.executionPoliciesKey,
		o.actuationPoliciesKey,
	} {
		keys[key] = struct{}{}
	}
	if len(keys) != 5 {
		return errors.New("Profiler fact-source keys must be different")
	}
	switch corev1.PullPolicy(o.transitionObserverPullPolicy) {
	case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
	default:
		return errors.New("transition-observer image pull policy is invalid")
	}
	if o.transitionObserverDeadlineOverheadSeconds < 1 ||
		o.transitionObserverDeadlineOverheadSeconds > math.MaxInt64/int64(time.Second) {
		return errors.New("transition-observer active deadline overhead is invalid")
	}
	if o.transitionMaximumObjectBytes < 1 || o.transitionMaximumObjectBytes == math.MaxInt64 {
		return errors.New("transition maximum object bytes is invalid")
	}
	return nil
}
