package main

import (
	"errors"
	"flag"
	"os"
	"strings"
)

type options struct {
	metricsAddr            string
	probeAddr              string
	systemName             string
	watchNamespace         string
	enableLeaderElection   bool
	leaderElectionID       string
	factSourceConfigMap    string
	executionFactsKey      string
	metricSourcesKey       string
	workloadObservationKey string
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
	flags.BoolVar(&o.enableLeaderElection, "leader-elect", false, "Enable leader election for the Profiler controller.")
	flags.StringVar(&o.leaderElectionID, "leader-election-id", "profiler.edge.dacs.io", "Leader election lock identity.")
	flags.StringVar(&o.factSourceConfigMap, "fact-source-config-map", "", "Profiler fact-source ConfigMap name.")
	flags.StringVar(&o.executionFactsKey, "execution-facts-key", "", "ConfigMap key containing execution facts.")
	flags.StringVar(&o.metricSourcesKey, "metric-sources-key", "", "ConfigMap key containing Node metric sources.")
	flags.StringVar(
		&o.workloadObservationKey,
		"workload-observation-key",
		"",
		"ConfigMap key containing WorkloadObserver image and allowed-background policy.",
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
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return errors.New(field.name + " is required without surrounding whitespace")
		}
	}
	keys := map[string]struct{}{}
	for _, key := range []string{o.executionFactsKey, o.metricSourcesKey, o.workloadObservationKey} {
		keys[key] = struct{}{}
	}
	if len(keys) != 3 {
		return errors.New("Profiler fact-source keys must be different")
	}
	return nil
}
