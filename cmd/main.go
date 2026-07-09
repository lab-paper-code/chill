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
	"github.com/lab-paper-code/chill/internal/controller"
	"github.com/lab-paper-code/chill/internal/deviceclasscatalog"
	"github.com/lab-paper-code/chill/internal/discoverycontroller"
	chilllabels "github.com/lab-paper-code/chill/internal/labels"
	"github.com/lab-paper-code/chill/internal/systemcontroller"
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
	var enableDeviceDiscovery bool
	var deviceDiscoveryLabelKey string
	var deviceDiscoveryOverwriteManualLabels bool
	var deviceDiscoveryNodeLabelSelector string
	var deviceDiscoveryRequireCatalogMatch bool
	var deviceDiscoveryCatalogNamespace string
	var deviceDiscoveryCatalogName string
	var deviceDiscoveryCatalogKey string
	var systemStatusName string
	var systemStatusNamespace string
	var systemStatusControllerDeploymentName string
	var systemStatusNodeDiscoveryDaemonSetName string
	var systemStatusNodeDiscoveryEnabled bool
	var systemStatusRefreshInterval time.Duration
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableDeviceDiscovery, "device-discovery-enabled", false,
		"Enable node-based DeviceClass discovery.")
	flag.StringVar(&deviceDiscoveryLabelKey, "device-discovery-label-key", chilllabels.DeviceClass,
		"Node label key used to bind nodes to discovered DeviceClasses.")
	flag.BoolVar(&deviceDiscoveryOverwriteManualLabels, "device-discovery-overwrite-manual-labels", false,
		"Overwrite existing node device-class labels during discovery.")
	flag.StringVar(&deviceDiscoveryNodeLabelSelector, "device-discovery-node-label-selector", "",
		"Label selector limiting which Nodes participate in device discovery.")
	flag.BoolVar(&deviceDiscoveryRequireCatalogMatch, "device-discovery-require-catalog-match", true,
		"Only discover DeviceClasses for Nodes matched by the discovery catalog.")
	flag.StringVar(&deviceDiscoveryCatalogNamespace, "device-discovery-catalog-namespace", os.Getenv("POD_NAMESPACE"),
		"Namespace containing the optional device discovery catalog ConfigMap.")
	flag.StringVar(&deviceDiscoveryCatalogName, "device-discovery-catalog-name", "",
		"Name of the optional device discovery catalog ConfigMap.")
	flag.StringVar(&deviceDiscoveryCatalogKey, "device-discovery-catalog-key", deviceclasscatalog.CatalogDataKey,
		"Data key containing the device discovery catalog in the ConfigMap.")
	flag.StringVar(&systemStatusName, "system-status-name", systemcontroller.DefaultSystemName,
		"Name of the namespace-local ChillSystem status resource.")
	flag.StringVar(&systemStatusNamespace, "system-status-namespace", systemcontroller.DefaultNamespace(),
		"Namespace containing the namespace-local ChillSystem status resource.")
	flag.StringVar(&systemStatusControllerDeploymentName, "system-status-controller-deployment-name", "",
		"Name of the controller Deployment reported in ChillSystem status.")
	flag.StringVar(&systemStatusNodeDiscoveryDaemonSetName, "system-status-node-discovery-daemonset-name", "",
		"Name of the node-discovery DaemonSet reported in ChillSystem status.")
	flag.BoolVar(&systemStatusNodeDiscoveryEnabled, "system-status-node-discovery-enabled", false,
		"Report node-discovery as an enabled component in ChillSystem status.")
	flag.DurationVar(
		&systemStatusRefreshInterval,
		"system-status-refresh-interval",
		systemcontroller.DefaultRefreshInterval,
		"Periodic refresh interval for ChillSystem status.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	systemStatusOptions := systemcontroller.Options{
		SystemName:                 systemStatusName,
		Namespace:                  systemStatusNamespace,
		ControllerDeploymentName:   systemStatusControllerDeploymentName,
		NodeDiscoveryDaemonSetName: systemStatusNodeDiscoveryDaemonSetName,
		NodeDiscoveryEnabled:       systemStatusNodeDiscoveryEnabled,
		RefreshInterval:            systemStatusRefreshInterval,
	}
	if err := systemStatusOptions.DefaultAndValidate(); err != nil {
		setupLog.Error(err, "invalid ChillSystem status configuration")
		os.Exit(1)
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
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.DeviceClassReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DeviceClass")
		os.Exit(1)
	}
	if err = (&systemcontroller.ChillSystemReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Options: systemStatusOptions,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ChillSystem")
		os.Exit(1)
	}
	if enableDeviceDiscovery {
		if err = (&discoverycontroller.DeviceDiscoveryReconciler{
			Client: mgr.GetClient(),
			Options: discoverycontroller.DeviceDiscoveryOptions{
				LabelKey:              deviceDiscoveryLabelKey,
				OverwriteManualLabels: deviceDiscoveryOverwriteManualLabels,
				NodeLabelSelector:     deviceDiscoveryNodeLabelSelector,
				RequireCatalogMatch:   deviceDiscoveryRequireCatalogMatch,
				CatalogNamespace:      deviceDiscoveryCatalogNamespace,
				CatalogName:           deviceDiscoveryCatalogName,
				CatalogKey:            deviceDiscoveryCatalogKey,
			},
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "DeviceDiscovery")
			os.Exit(1)
		}
	}
	if err = (&controller.ModelSpecReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ModelSpec")
		os.Exit(1)
	}
	if err = (&controller.DeviceProfileReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DeviceProfile")
		os.Exit(1)
	}
	if err = (&controller.ClusterEnergyModelReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterEnergyModel")
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

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
