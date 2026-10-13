package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

type options struct {
	bindAddress, servingNamespace, actuationNamespace, tlsCert, tlsKey, observationDirectory string
	storeName, storeEndpoint, storeBucket, storeRegion                                       string
	consoleURL, dashboardURL                                                                 string
	kube                                                                                     kubeconfig.Options
	queueLimit, maxQueryEvents                                                               int
	maxRequestBytes, maxEvidenceBytes                                                        int64
	profileConfidenceLevel                                                                   float64
	requestTimeout, drainTimeout, sourceInterval, profileInterval, observationWindow         time.Duration
	campaigns                                                                                []types.NamespacedName
	definitions, outcomes                                                                    []string
	materializeProfile                                                                       bool
	profileObjectKey, profileOutput                                                          string
	profileObjectSize                                                                        int64
	schedulerConfigMap, schedulerConfigKey                                                   string
}

func (o *options) bindFlags(f *flag.FlagSet) {
	f.StringVar(&o.bindAddress, "bind-address", ":8080", "HTTPS listen address.")
	f.StringVar(&o.servingNamespace, "serving-namespace", "", "Namespace holding native serving instances.")
	f.StringVar(&o.actuationNamespace, "actuation-namespace", "", "NodeActuation namespace; default: serving namespace.")
	f.StringVar(&o.tlsCert, "tls-cert", "", "TLS certificate file.")
	f.StringVar(&o.tlsKey, "tls-key", "", "TLS private key file.")
	f.StringVar(&o.observationDirectory, "observation-directory", "", "Private persistent journal directory.")
	f.StringVar(&o.consoleURL, "console-url", "",
		"Optional fixed private Console HTTP(S) origin; query and evidence routes require it.")
	f.StringVar(&o.dashboardURL, "dashboard-url", "",
		"Optional fixed private Dashboard origin served on this HTTPS listener outside owner API routes.")
	f.StringVar(&o.storeName, "store-name", "", "Logical Evidence Store name.")
	f.StringVar(&o.storeEndpoint, "store-endpoint", "", "S3-compatible Evidence Store endpoint.")
	f.StringVar(&o.storeBucket, "store-bucket", "", "Evidence Store bucket.")
	f.StringVar(&o.storeRegion, "store-region", "", "Evidence Store region.")
	f.BoolVar(&o.materializeProfile, "materialize-profile", false,
		"Materialize one bounded immutable profile and exit without Kubernetes or TLS.")
	f.StringVar(&o.profileObjectKey, "profile-object-key", "", "Immutable profile object key.")
	f.Int64Var(&o.profileObjectSize, "profile-object-size", 0, "Exact immutable profile object byte count.")
	f.StringVar(&o.profileOutput, "profile-output", "", "Absolute materialized profile file.")
	f.StringVar(&o.schedulerConfigMap, "serving-scheduler-configmap", "",
		"Named scheduler ConfigMap in serving namespace; empty disables scheduler.")
	f.StringVar(&o.schedulerConfigKey, "serving-scheduler-config-key", "scheduler.json",
		"Scheduler configuration JSON ConfigMap key.")
	f.StringVar(&o.kube.APIServer, "kube-api-server", "", "Explicit Kubernetes API endpoint; otherwise in-cluster.")
	f.StringVar(&o.kube.TokenFile, "kube-token-file", "", "Kubernetes service-account token file.")
	f.StringVar(&o.kube.CAFile, "kube-ca-file", "", "Kubernetes API CA file.")
	f.IntVar(&o.queueLimit, "queue-limit", 1024, "Per-Workload ingress waiting limit.")
	f.IntVar(&o.maxQueryEvents, "max-query-events", 1000, "Maximum raw observation events per query page.")
	f.Int64Var(&o.maxRequestBytes, "max-request-bytes", 8<<20, "Maximum management or inference body bytes.")
	f.Int64Var(&o.maxEvidenceBytes, "max-evidence-bytes", 64<<20, "Maximum bytes per profiling evidence object.")
	f.DurationVar(&o.requestTimeout, "request-timeout", 30*time.Second, "Read/operation and delivery bound; not SLO.")
	f.DurationVar(&o.drainTimeout, "drain-timeout", 2*time.Minute, "Management and process shutdown drain bound.")
	f.DurationVar(&o.sourceInterval, "source-interval", time.Second, "Current instance and management refresh interval.")
	f.DurationVar(&o.profileInterval, "profile-interval", time.Minute, "Selected profiling evidence refresh interval.")
	f.DurationVar(&o.observationWindow, "observation-window", time.Minute, "Raw observation publication window.")
	f.Float64Var(&o.profileConfidenceLevel, "profile-confidence-level", 0.95, "Measured idle-baseline confidence.")
	f.Func("profile-campaign", "Selected namespace/name terminal campaign; repeatable.", func(value string) error {
		namespace, name, ok := strings.Cut(value, "/")
		if !ok || len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
			return fmt.Errorf("invalid campaign %q: expected namespace/name", value)
		}
		o.campaigns = append(o.campaigns, types.NamespacedName{Namespace: namespace, Name: name})
		return nil
	})
	f.Func("observation-definition", "Default aggregate definition filter; repeatable; empty selects all.",
		func(value string) error {
			o.definitions = append(o.definitions, value)
			return nil
		})
	f.Func("observation-outcome", "Default aggregate response-outcome filter; repeatable; empty selects all.",
		func(value string) error {
			o.outcomes = append(o.outcomes, value)
			return nil
		})
}

func (o options) validate() error {
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
	if o.actuationNamespace != "" && len(validation.IsDNS1123Label(o.actuationNamespace)) != 0 {
		return errors.New("invalid actuation namespace")
	}
	for _, path := range []string{o.tlsCert, o.tlsKey, o.observationDirectory} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("TLS files and private observation directory must be clean absolute paths")
		}
	}
	if o.queueLimit <= 0 || o.maxQueryEvents <= 0 || o.maxRequestBytes <= 0 || o.maxEvidenceBytes <= 0 {
		return errors.New("queue, body, evidence and query bounds must be positive")
	}
	if o.requestTimeout <= 0 || o.drainTimeout <= 0 || o.sourceInterval <= 0 ||
		o.profileInterval <= 0 || o.observationWindow <= 0 {
		return errors.New("request, drain, refresh and observation durations must be positive")
	}
	if !(o.profileConfidenceLevel > 0 && o.profileConfidenceLevel < 1) {
		return errors.New("profile confidence level must be between zero and one")
	}
	if _, err := privateOrigin(o.consoleURL); err != nil {
		return err
	}
	if _, err := privateOrigin(o.dashboardURL); err != nil {
		return err
	}
	return nil
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
