package main

import (
	"errors"
	"flag"
	"os"
	"strings"
)

type options struct {
	metricsAddr                    string
	probeAddr                      string
	systemName                     string
	watchNamespace                 string
	enableLeaderElection           bool
	leaderElectionID               string
	factSourceConfigMap            string
	executionFactsKey              string
	metricSourcesKey               string
	workloadObservationKey         string
	admissionPoliciesKey           string
	executionPoliciesKey           string
	actuationPoliciesKey           string
	workloadObserverServiceAccount string
	workloadObserverKubeAPIServer  string
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to.")
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
	flags.BoolVar(&o.enableLeaderElection, "leader-elect", false, "Enable leader election for the Profiler controller.")
	flags.StringVar(&o.leaderElectionID, "leader-election-id", "profiler.edge.dacs.io", "Leader election lock identity.")
	flags.StringVar(&o.factSourceConfigMap, "fact-source-config-map", "", "Profiler fact-source ConfigMap name.")
	flags.StringVar(&o.executionFactsKey, "execution-facts-key", "", "ConfigMap key containing execution facts.")
	flags.StringVar(&o.metricSourcesKey, "metric-sources-key", "", "ConfigMap key containing Node metric sources.")
	flags.StringVar(
		&o.admissionPoliciesKey,
		"admission-policies-key",
		"",
		"ConfigMap key containing versioned scientific admission policies.",
	)
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
		{name: "admission-policies key", value: o.admissionPoliciesKey},
		{name: "execution-policies key", value: o.executionPoliciesKey},
		{name: "actuation-policies key", value: o.actuationPoliciesKey},
		{name: "workload-observer ServiceAccount", value: o.workloadObserverServiceAccount},
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
		o.admissionPoliciesKey, o.executionPoliciesKey,
		o.actuationPoliciesKey,
	} {
		keys[key] = struct{}{}
	}
	if len(keys) != 6 {
		return errors.New("Profiler fact-source keys must be different")
	}
	return nil
}
