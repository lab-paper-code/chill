package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilerapp "github.com/lab-paper-code/chill/internal/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(edgev1alpha1.AddToScheme(scheme))
}

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
	flag.StringVar(
		&opts.watchNamespace,
		"watch-namespace",
		os.Getenv("POD_NAMESPACE"),
		"Namespace containing CHILL profiling resources.",
	)
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
		Scheme:                 scheme,
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
	if err := setupIntentController(mgr, opts); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "ProfilingRunIntent")
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

func setupIntentController(mgr ctrl.Manager, opts options) error {
	reader := mgr.GetAPIReader()
	candidates, err := profilercontroller.NewKubernetesCandidateResolver(
		reader,
		profilercontroller.CandidateResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.executionFactsKey,
		},
	)
	if err != nil {
		return fmt.Errorf("construct candidate resolver: %w", err)
	}
	nodes, err := profilercontroller.NewKubernetesNodeResolver(reader)
	if err != nil {
		return fmt.Errorf("construct Node resolver: %w", err)
	}
	power, err := profilercontroller.NewKubernetesPowerSourceResolver(
		reader,
		profilercontroller.PowerSourceResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.powerSourcesKey,
		},
	)
	if err != nil {
		return fmt.Errorf("construct power-source resolver: %w", err)
	}
	preparer, err := profilerapp.NewPreparer(candidates, nodes, power)
	if err != nil {
		return fmt.Errorf("construct Run preparer: %w", err)
	}
	records, err := profilercontroller.NewIntentRecords(mgr.GetClient(), mgr.GetScheme())
	if err != nil {
		return fmt.Errorf("construct intent records: %w", err)
	}
	freezer, err := profilercontroller.NewIntentFreezer(mgr.GetClient(), preparer, records)
	if err != nil {
		return fmt.Errorf("construct intent freezer: %w", err)
	}
	reconciler, err := profilercontroller.NewIntentReconciler(mgr.GetClient(), freezer)
	if err != nil {
		return fmt.Errorf("construct intent reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up intent reconciler: %w", err)
	}
	return nil
}
