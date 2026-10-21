// Command node-agent publishes Node hardware signature and online-CPU metadata
// on every Node. With --actuate it instead applies NodeActuations on its Node.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
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
	if len(os.Args) == 2 && os.Args[1] == jetson.EnsureBootOrderingCommand {
		wrote, err := jetson.EnsureBootOrdering(jetson.HostSystemdEtcDir, jetson.HostSystemdLibDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ensure Jetson boot ordering:", err)
			os.Exit(1)
		}
		fmt.Println("Jetson boot ordering ensured; drop-in written:", wrote)
		return
	}
	opts := options{
		nodeName:      os.Getenv("NODE_NAME"),
		systemName:    os.Getenv("CHILL_SYSTEM_NAME"),
		kubeAPIServer: os.Getenv("CHILL_KUBE_API_SERVER"),
	}
	opts.bindFlags(flag.CommandLine)
	zapOptions := zap.Options{Development: true}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	if !opts.actuate {
		runDiscovery(opts)
		return
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	runActuation(opts)
}

func runActuation(opts options) {
	if err := opts.validateActuation(); err != nil {
		setupLog.Error(err, "invalid actuation options")
		os.Exit(1)
	}
	kubernetesConfig, err := kubeconfig.BuildConfig(kubeconfig.Options{
		APIServer: opts.kubeAPIServer,
		TokenFile: opts.kubeAPITokenFile,
		CAFile:    opts.kubeAPICAFile,
	})
	if err != nil {
		setupLog.Error(err, "unable to load Kubernetes client config")
		os.Exit(1)
	}
	manager, err := ctrl.NewManager(kubernetesConfig, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress: opts.probeAddr,
		Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{opts.watchNamespace: {}}},
	})
	if err != nil {
		setupLog.Error(err, "unable to start actuation manager")
		os.Exit(1)
	}
	if err := setupTransitionController(context.Background(), manager, opts); err != nil {
		setupLog.Error(err, "unable to register NodeActuation reconciler")
		os.Exit(1)
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up readiness check")
		os.Exit(1)
	}
	setupLog.Info("starting node agent actuation", "node", opts.nodeName, "watchNamespace", opts.watchNamespace)
	if err := manager.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running node agent actuation")
		os.Exit(1)
	}
}
