package main

import (
	"errors"
	"flag"
	"os"
	"strings"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var setupLog = ctrl.Log.WithName("setup")

type options struct {
	metricsAddr          string
	probeAddr            string
	watchNamespace       string
	enableLeaderElection bool
	leaderElectionID     string
	factSourceConfigMap  string
	executionFactsKey    string
	powerSourcesKey      string
}

func (o options) validate() error {
	for name, value := range map[string]string{
		"watch namespace":       o.watchNamespace,
		"fact-source ConfigMap": o.factSourceConfigMap,
		"execution-facts key":   o.executionFactsKey,
		"power-sources key":     o.powerSourcesKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return errors.New(name + " is required without surrounding whitespace")
		}
	}
	if o.executionFactsKey == o.powerSourcesKey {
		return errors.New("execution-facts and power-sources keys must be different")
	}
	return nil
}

func main() {
	opts := options{}
	flag.StringVar(&opts.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to.")
	flag.StringVar(&opts.probeAddr, "health-probe-bind-address", ":8081", "The address the health probes bind to.")
	flag.StringVar(&opts.watchNamespace, "watch-namespace", os.Getenv("POD_NAMESPACE"), "Namespace containing CHILL profiling resources.")
	flag.BoolVar(&opts.enableLeaderElection, "leader-elect", false, "Enable leader election for the Profiler controller.")
	flag.StringVar(&opts.leaderElectionID, "leader-election-id", "profiler.edge.dacs.io", "Leader election lock identity.")
	flag.StringVar(&opts.factSourceConfigMap, "fact-source-config-map", "", "Profiler fact-source ConfigMap name.")
	flag.StringVar(&opts.executionFactsKey, "execution-facts-key", "", "ConfigMap key containing execution facts.")
	flag.StringVar(&opts.powerSourcesKey, "power-sources-key", "", "ConfigMap key containing Node power sources.")

	zapOptions := zap.Options{Development: true}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))

	if err := opts.validate(); err != nil {
		setupLog.Error(err, "invalid Profiler options")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Metrics:                metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.enableLeaderElection,
		LeaderElectionID:       opts.leaderElectionID,
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{
			opts.watchNamespace: {},
		}},
	})
	if err != nil {
		setupLog.Error(err, "unable to start Profiler manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up readiness check")
		os.Exit(1)
	}

	setupLog.Info(
		"starting Profiler",
		"watchNamespace", opts.watchNamespace,
		"factSourceConfigMap", opts.factSourceConfigMap,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running Profiler")
		os.Exit(1)
	}
}
