package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/apimachinery/pkg/util/validation"
)

type options struct {
	bindAddress, servingNamespace, tlsCert, tlsKey, observationDirectory             string
	storeName, storeEndpoint, storeBucket, storeRegion                               string
	consoleURL, dashboardURL                                                         string
	storeCredentialsSecret                                                           string
	kube                                                                             kubeconfig.Options
	kubeAPIQPS                                                                       float64
	kubeAPIBurst                                                                     int
	queueLimit, maxQueryEvents                                                       int
	maxRequestBytes, maxEvidenceBytes                                                int64
	requestTimeout, drainTimeout, sourceInterval, profileInterval                    time.Duration
	operationTimeout                                                                 time.Duration
	observationRetention                                                             time.Duration
	profileNamespaces                                                                []string
	definitions, outcomes                                                            []string
	materializeProfile                                                               bool
	profileObjectKey, profileOutput                                                  string
	profileObjectSize                                                                int64
	schedulerConfigMap, schedulerConfigKey                                           string
	factsNamespace, factsConfigMap, actuationPoliciesKey, metricSourcesKey           string
	diagnosticDirectory                                                              string
	checkDeploymentManifest, schedulerConfigMapFile                                  string
	diagnosticStartDelay, diagnosticTraceOffset                                      time.Duration
	pprofAddress                                                                     string
	pprofMutexFraction                                                               int
	pprofBlockRate                                                                   time.Duration
	resourceMaxSamples, resourceMaxContainers, resourceMaxThermal, resourceMaxErrors int
	resourceRelease, resourceSystem                                                  string
	resourceControlComponents                                                        []string
	resourceDirectoryInterval                                                        time.Duration

	// loadGenerator runs the load Job of dispatch-occupancy measurements.
	loadGenerator servingkube.LoadGeneratorConfig
}

func (o *options) bindFlags(f *flag.FlagSet) {
	f.IntVar(&o.resourceMaxSamples, "resource-max-samples", 0,
		"Maximum samples per resource batch; required from deployment config.")
	f.IntVar(&o.resourceMaxContainers, "resource-max-containers", 0, "Maximum containers per resource sample.")
	f.IntVar(&o.resourceMaxThermal, "resource-max-thermal", 0, "Maximum thermal sensors per resource sample.")
	f.IntVar(&o.resourceMaxErrors, "resource-max-errors", 0, "Maximum errors per resource sample/container.")
	f.StringVar(&o.resourceRelease, "resource-release", "", "Exact release label of CHILL control Pods.")
	f.StringVar(&o.resourceSystem, "resource-system", "", "Exact ChillSystem identity of operator-managed control Pods.")
	f.Func("resource-control-component", "Allowed CHILL control component; repeatable.", func(value string) error {
		if len(validation.IsDNS1123Label(value)) != 0 {
			return fmt.Errorf("invalid resource control component %q", value)
		}
		o.resourceControlComponents = append(o.resourceControlComponents, value)
		return nil
	})
	f.DurationVar(&o.resourceDirectoryInterval, "resource-directory-interval", 0,
		"Observation directory byte sampling interval.")
	f.StringVar(&o.checkDeploymentManifest, "check-deployment-config", "",
		"Validate rendered deployment scheduler ConfigMaps offline and exit.")
	f.StringVar(&o.schedulerConfigMapFile, "scheduler-configmap-file", "",
		"Optional selected external scheduler ConfigMap manifest for offline validation.")
	f.StringVar(&o.bindAddress, "bind-address", ":8080", "HTTPS listen address.")
	f.StringVar(&o.pprofAddress, "pprof-address", "", "Separate loopback HTTP profiling host:port; empty disables.")
	f.IntVar(&o.pprofMutexFraction, "pprof-mutex-fraction", 0,
		"Mutex contention sampling fraction; 0 disables; requires pprof-address.")
	f.DurationVar(&o.pprofBlockRate, "pprof-block-rate", 0,
		"Blocking profile sampling interval (e.g. 1us); 0 disables; requires pprof-address.")
	f.StringVar(&o.servingNamespace, "serving-namespace", "", "Namespace holding native serving instances.")
	f.StringVar(&o.tlsCert, "tls-cert", "", "TLS certificate file.")
	f.StringVar(&o.tlsKey, "tls-key", "", "TLS private key file.")
	f.StringVar(&o.observationDirectory, "observation-directory", "", "Private persistent observation log directory.")
	f.DurationVar(&o.observationRetention, "observation-retention", 2*time.Hour,
		"Positive local observation retention measured from each hour's start.")
	f.StringVar(&o.diagnosticDirectory, "diagnostic-directory", "",
		"Fresh absolute private directory for one finite control-only capture; empty disables.")
	f.DurationVar(&o.diagnosticStartDelay, "diagnostic-start-delay", 0,
		"Delay from listener bind to the one 420s numeric window.")
	f.DurationVar(&o.diagnosticTraceOffset, "diagnostic-trace-offset", 0,
		"Offset within the numeric window for at most 30s/32MiB of Go trace; maximum 390s.")
	f.StringVar(&o.consoleURL, "console-url", "",
		"Optional fixed private Console HTTP(S) origin; query and evidence routes require it.")
	f.StringVar(&o.dashboardURL, "dashboard-url", "",
		"Optional fixed private Dashboard origin served on this HTTPS listener outside ChillServer API routes.")
	f.StringVar(&o.storeName, "store-name", "", "Logical Evidence Store name.")
	f.StringVar(&o.storeEndpoint, "store-endpoint", "", "S3-compatible Evidence Store endpoint.")
	f.StringVar(&o.storeBucket, "store-bucket", "", "Evidence Store bucket.")
	f.StringVar(&o.storeRegion, "store-region", "", "Evidence Store region.")
	f.StringVar(&o.storeCredentialsSecret, "store-credentials-secret", "",
		"Evidence Store credentials Secret used by generated serving preparations.")
	f.BoolVar(&o.materializeProfile, "materialize-profile", false,
		"Materialize one bounded immutable profile and exit without Kubernetes or TLS.")
	f.StringVar(&o.profileObjectKey, "profile-object-key", "", "Immutable profile object key.")
	f.Int64Var(&o.profileObjectSize, "profile-object-size", 0, "Exact immutable profile object byte count.")
	f.StringVar(&o.profileOutput, "profile-output", "", "Absolute materialized profile file.")
	f.StringVar(&o.schedulerConfigMap, "serving-scheduler-configmap", "",
		"Named scheduler ConfigMap in serving namespace; empty disables scheduler.")
	f.StringVar(&o.schedulerConfigKey, "serving-scheduler-config-key", "scheduler.json",
		"Scheduler configuration JSON ConfigMap key.")
	f.StringVar(&o.factsNamespace, "profiler-facts-namespace", "", "Namespace of the Profiler facts ConfigMap.")
	f.StringVar(&o.factsConfigMap, "profiler-facts-configmap", "",
		"Profiler facts ConfigMap; empty leaves the scheduler without power requests.")
	f.StringVar(&o.actuationPoliciesKey, "actuation-policies-key", "actuation-policies.yaml",
		"Actuation policy catalog key in the Profiler facts ConfigMap.")
	f.StringVar(&o.metricSourcesKey, "metric-sources-key", "metric-sources.yaml",
		"Metric source key in the Profiler facts ConfigMap.")
	f.StringVar(&o.kube.APIServer, "kube-api-server", "", "Explicit Kubernetes API endpoint; otherwise in-cluster.")
	f.StringVar(&o.kube.TokenFile, "kube-token-file", "", "Kubernetes service-account token file.")
	f.StringVar(&o.kube.CAFile, "kube-ca-file", "", "Kubernetes API CA file.")
	f.Float64Var(&o.kubeAPIQPS, "kube-api-qps", 50,
		"Request rate of each ChillServer Kubernetes client (control, HTTP boundary); must be finite and positive.")
	f.IntVar(&o.kubeAPIBurst, "kube-api-burst", 100, "Burst of each ChillServer Kubernetes client; must be positive.")
	f.IntVar(&o.queueLimit, "queue-limit", 1024, "Per-Workload ingress waiting limit.")
	f.IntVar(&o.maxQueryEvents, "max-query-events", 1000, "Maximum raw observation events per query page.")
	f.Int64Var(&o.maxRequestBytes, "max-request-bytes", 8<<20, "Maximum management or inference body bytes.")
	f.Int64Var(&o.maxEvidenceBytes, "max-evidence-bytes", 64<<20, "Maximum bytes per profiling evidence object.")
	f.DurationVar(&o.requestTimeout, "request-timeout", 30*time.Second, "Read/operation and delivery bound; not SLO.")
	f.DurationVar(&o.operationTimeout, "operation-timeout", 30*time.Minute,
		"Processing bound of one manual operation or historical observation read.")
	f.DurationVar(&o.drainTimeout, "drain-timeout", 2*time.Minute, "Management and process shutdown drain bound.")
	f.DurationVar(&o.sourceInterval, "source-interval", time.Second, "Current instance and management refresh interval.")
	f.DurationVar(&o.profileInterval, "profile-interval", time.Minute, "Selected profiling evidence refresh interval.")
	f.Func("profile-namespace", "Allowed measurement namespace; repeatable; empty uses serving namespace.",
		func(value string) error {
			if len(validation.IsDNS1123Label(value)) != 0 {
				return fmt.Errorf("invalid profile namespace %q", value)
			}
			o.profileNamespaces = append(o.profileNamespaces, value)
			return nil
		})
	f.Func("observation-definition", "Default aggregate definition filter; repeatable; empty selects all.",
		func(value string) error {
			o.definitions = append(o.definitions, value)
			return nil
		})
	f.StringVar(&o.loadGenerator.Image, "load-generator-image", "", "Image of the dispatch-occupancy load Job.")
	f.StringVar(&o.loadGenerator.ServiceAccount, "load-generator-service-account", "", "ServiceAccount of the load Job.")
	f.StringVar(&o.loadGenerator.Namespace, "load-generator-namespace", "", "Namespace of the load Job.")
	f.StringVar(&o.loadGenerator.Endpoint, "load-generator-endpoint", "",
		"HTTPS origin of the serving Service by its .svc name.")
	f.StringVar(&o.loadGenerator.CASecret, "load-generator-ca-secret", "", "Secret whose tls.crt the load Job trusts.")
	f.Func("observation-outcome", "Default aggregate response-outcome filter; repeatable; empty selects all.",
		func(value string) error {
			o.outcomes = append(o.outcomes, value)
			return nil
		})
}

func (o options) validate() error {
	if err := o.validatePprof(); err != nil {
		return err
	}
	if err := o.validateDiagnostics(); err != nil {
		return err
	}
	if o.requestTimeout <= 0 || o.requestTimeout > 129*time.Second {
		return errors.New("request-timeout must be positive and at most 129s")
	}
	if o.materializeProfile {
		return o.validateMaterialization()
	}
	if o.schedulerConfigMap != "" &&
		(len(validation.IsDNS1123Subdomain(o.schedulerConfigMap)) != 0 ||
			len(validation.IsConfigMapKey(o.schedulerConfigKey)) != 0) {
		return errors.New("valid scheduler ConfigMap name and JSON key are required")
	}
	if strings.TrimSpace(o.bindAddress) == "" || strings.TrimSpace(o.bindAddress) != o.bindAddress {
		return errors.New("bind address is required without surrounding whitespace")
	}
	if len(validation.IsDNS1123Label(o.servingNamespace)) != 0 {
		return errors.New("valid serving namespace is required")
	}
	for _, namespace := range o.profileNamespaces {
		if len(validation.IsDNS1123Label(namespace)) != 0 {
			return errors.New("invalid profile namespace")
		}
	}
	if o.storeCredentialsSecret != "" && len(validation.IsDNS1123Subdomain(o.storeCredentialsSecret)) != 0 {
		return errors.New("invalid Evidence Store credentials Secret name")
	}
	for _, path := range []string{o.tlsCert, o.tlsKey, o.observationDirectory} {
		if !isCleanAbsolutePath(path) {
			return errors.New("TLS files and private observation directory must be clean absolute paths")
		}
	}
	if o.queueLimit <= 0 || o.maxQueryEvents <= 0 || o.maxRequestBytes <= 0 || o.maxEvidenceBytes <= 0 {
		return errors.New("queue, body, evidence and query bounds must be positive")
	}
	durations := []time.Duration{o.requestTimeout, o.operationTimeout, o.drainTimeout,
		o.sourceInterval, o.profileInterval, o.observationRetention}
	for _, d := range durations {
		if d <= 0 {
			return errors.New("request, operation, drain, refresh and observation durations must be positive")
		}
	}
	if _, err := privateOrigin(o.consoleURL); err != nil {
		return err
	}
	if _, err := privateOrigin(o.dashboardURL); err != nil {
		return err
	}
	if err := o.validateFacts(); err != nil {
		return err
	}
	if err := o.validateResources(); err != nil {
		return err
	}
	return o.validateKubernetesAPIBudget()
}

func (o options) validateResources() error {
	if o.resourceMaxSamples <= 0 || o.resourceMaxContainers <= 0 || o.resourceMaxThermal <= 0 ||
		o.resourceMaxErrors <= 0 || o.resourceDirectoryInterval <= 0 || o.resourceRelease == "" ||
		o.resourceSystem == "" || len(o.resourceControlComponents) == 0 {
		return errors.New(
			"positive resource bounds/directory interval and explicit release/system/control components are required",
		)
	}
	return nil
}

func isCleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func (o options) validateFacts() error {
	if o.factsConfigMap != "" && (len(validation.IsDNS1123Label(o.factsNamespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(o.factsConfigMap)) != 0 ||
		len(validation.IsConfigMapKey(o.actuationPoliciesKey)) != 0 ||
		len(validation.IsConfigMapKey(o.metricSourcesKey)) != 0) {
		return errors.New("valid Profiler facts namespace, ConfigMap name and keys are required")
	}
	return nil
}

func (o options) validateKubernetesAPIBudget() error {
	if float32(o.kubeAPIQPS) > 0 && o.kubeAPIQPS <= math.MaxFloat32 && o.kubeAPIBurst > 0 {
		return nil
	}
	return errors.New("Kubernetes API QPS must be finite and positive and burst must be positive")
}

func (o options) validateMaterialization() error {
	if o.profileObjectSize <= 0 || o.profileObjectSize > o.maxEvidenceBytes || o.profileObjectKey == "" {
		return errors.New("bounded positive immutable profile object identity is required")
	}
	if !filepath.IsAbs(o.profileOutput) || filepath.Clean(o.profileOutput) != o.profileOutput {
		return errors.New("profile output must be a clean absolute file path")
	}
	if o.requestTimeout <= 0 {
		return errors.New("materialization timeout must be positive")
	}
	return nil
}
