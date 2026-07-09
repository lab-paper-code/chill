package main

import (
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/operator/discovery"
	"github.com/lab-paper-code/chill/internal/operator/nodediscovery"
	"github.com/lab-paper-code/chill/internal/operator/resources"
	"github.com/lab-paper-code/chill/internal/operator/system"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(edgev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var deviceDiscoveryLabelKey string
	var deviceDiscoveryOverwriteManualLabels bool
	var deviceDiscoveryNodeLabelSelector string
	var deviceDiscoveryRequireCatalogMatch bool
	var deviceDiscoveryCatalogNamespace string
	var deviceDiscoveryCatalogName string
	var deviceDiscoveryCatalogKey string
	var systemName string
	var systemNamespace string
	var operatorDeploymentName string
	var systemRefreshInterval time.Duration
	var nodeDiscoveryConfigNamespace string
	var nodeDiscoveryConfigName string
	var nodeDiscoveryConfigKey string
	var nodeDiscoveryReconcileInterval time.Duration
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for the CHILL operator. "+
			"Enabling this will ensure there is only one active operator.")
	flag.StringVar(&deviceDiscoveryLabelKey, "device-discovery-label-key", chillmeta.DeviceClass,
		"Default Node label key used to bind nodes to discovered DeviceClasses.")
	flag.BoolVar(&deviceDiscoveryOverwriteManualLabels, "device-discovery-overwrite-manual-labels", false,
		"Default policy for overwriting existing node device-class labels during discovery.")
	flag.StringVar(&deviceDiscoveryNodeLabelSelector, "device-discovery-node-label-selector", "",
		"Default label selector limiting which Nodes participate in device discovery.")
	flag.BoolVar(&deviceDiscoveryRequireCatalogMatch, "device-discovery-require-catalog-match", true,
		"Default policy requiring Nodes to match the discovery catalog.")
	flag.StringVar(&deviceDiscoveryCatalogNamespace, "device-discovery-catalog-namespace", os.Getenv("POD_NAMESPACE"),
		"Default namespace containing the optional device discovery catalog ConfigMap.")
	flag.StringVar(&deviceDiscoveryCatalogName, "device-discovery-catalog-name", "",
		"Default name of the optional device discovery catalog ConfigMap.")
	flag.StringVar(&deviceDiscoveryCatalogKey, "device-discovery-catalog-key", deviceclass.CatalogDataKey,
		"Default data key containing the device discovery catalog in the ConfigMap.")
	flag.StringVar(&systemName, "system-name", system.DefaultSystemName,
		"Default ChillSystem name used by compatibility refresh paths.")
	flag.StringVar(&systemName, "system-status-name", system.DefaultSystemName,
		"Deprecated alias for --system-name.")
	flag.StringVar(&systemNamespace, "operator-namespace", system.DefaultNamespace(),
		"Operator namespace and default CHILL management namespace.")
	flag.StringVar(&systemNamespace, "system-status-namespace", system.DefaultNamespace(),
		"Deprecated alias for --operator-namespace.")
	flag.StringVar(&operatorDeploymentName, "operator-deployment-name", "",
		"Name of the operator Deployment reported in ChillSystem status.")
	flag.StringVar(&operatorDeploymentName, "system-status-operator-deployment-name", "",
		"Deprecated alias for --operator-deployment-name.")
	flag.DurationVar(
		&systemRefreshInterval,
		"system-refresh-interval",
		system.DefaultRefreshInterval,
		"Periodic refresh interval for ChillSystem status.")
	flag.DurationVar(
		&systemRefreshInterval,
		"system-status-refresh-interval",
		system.DefaultRefreshInterval,
		"Deprecated alias for --system-refresh-interval.")
	flag.StringVar(&nodeDiscoveryConfigNamespace, "node-discovery-config-namespace", os.Getenv("POD_NAMESPACE"),
		"Namespace containing the node-discovery operator config ConfigMap.")
	flag.StringVar(&nodeDiscoveryConfigName, "node-discovery-config-name", "",
		"Name of the node-discovery operator config ConfigMap.")
	flag.StringVar(&nodeDiscoveryConfigKey, "node-discovery-config-key", nodediscovery.DefaultConfigKey,
		"Data key containing node-discovery operator config.")
	flag.DurationVar(
		&nodeDiscoveryReconcileInterval,
		"node-discovery-reconcile-interval",
		nodediscovery.DefaultReconcileInterval,
		"Periodic refresh interval for node-discovery reconciliation.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	systemOptions := system.Options{
		SystemName:             systemName,
		Namespace:              systemNamespace,
		OperatorDeploymentName: operatorDeploymentName,
		RefreshInterval:        systemRefreshInterval,
	}
	if err := systemOptions.DefaultAndValidate(); err != nil {
		setupLog.Error(err, "invalid ChillSystem configuration")
		os.Exit(1)
	}
	if nodeDiscoveryConfigNamespace == "" {
		nodeDiscoveryConfigNamespace = systemOptions.Namespace
	}
	if deviceDiscoveryCatalogNamespace == "" {
		deviceDiscoveryCatalogNamespace = systemOptions.Namespace
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress: metricsAddr,
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "1ba2121f.dacs.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start operator")
		os.Exit(1)
	}

	if err = (&resources.DeviceClassReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "DeviceClass")
		os.Exit(1)
	}
	if err = (&system.ChillSystemReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Options: systemOptions,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "ChillSystem")
		os.Exit(1)
	}
	if err = (&nodediscovery.Reconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Options: nodediscovery.Options{
			SystemName:        systemName,
			Namespace:         nodeDiscoveryConfigNamespace,
			ConfigMapName:     nodeDiscoveryConfigName,
			ConfigMapKey:      nodeDiscoveryConfigKey,
			ReconcileInterval: nodeDiscoveryReconcileInterval,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "NodeDiscovery")
		os.Exit(1)
	}
	if err = (&discovery.DeviceDiscoveryReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Options: discovery.DeviceDiscoveryOptions{
			SystemName:            systemName,
			Namespace:             systemOptions.Namespace,
			LabelKey:              deviceDiscoveryLabelKey,
			OverwriteManualLabels: deviceDiscoveryOverwriteManualLabels,
			NodeLabelSelector:     deviceDiscoveryNodeLabelSelector,
			RequireCatalogMatch:   deviceDiscoveryRequireCatalogMatch,
			CatalogNamespace:      deviceDiscoveryCatalogNamespace,
			CatalogName:           deviceDiscoveryCatalogName,
			CatalogKey:            deviceDiscoveryCatalogKey,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "DeviceDiscovery")
		os.Exit(1)
	}
	if err = (&resources.ModelSpecReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "ModelSpec")
		os.Exit(1)
	}
	if err = (&resources.DeviceProfileReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "DeviceProfile")
		os.Exit(1)
	}
	if err = (&resources.ClusterEnergyModelReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "ClusterEnergyModel")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running operator")
		os.Exit(1)
	}
}
