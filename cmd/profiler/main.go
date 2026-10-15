package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(edgev1alpha1.AddToScheme(scheme))
}

func main() {
	opts := options{}
	opts.bindFlags(flag.CommandLine)

	zapOptions := zap.Options{Development: true}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))

	if err := opts.validate(); err != nil {
		setupLog.Error(err, "invalid Profiler options")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.enableLeaderElection,
		LeaderElectionID:       opts.leaderElectionID,
		// Terminal status fences irreversible evidence writers; their reads must
		// not replay a preterminal informer snapshot after explicit cleanup.
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{
			&edgev1alpha1.BatchEnergyOptimization{}, &edgev1alpha1.ProfilingRun{},
		}}},
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{
			opts.watchNamespace: {},
		}},
	})
	if err != nil {
		setupLog.Error(err, "unable to start Profiler manager")
		os.Exit(1)
	}
	if err := setupProfilerControllers(mgr, opts); err != nil {
		setupLog.Error(err, "unable to register Profiler controllers")
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
		"systemName", opts.systemName,
		"watchNamespace", opts.watchNamespace,
		"factSourceConfigMap", opts.factSourceConfigMap,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running Profiler")
		os.Exit(1)
	}
}
