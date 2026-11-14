package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

type options struct {
	bindAddress                                 string
	servingURL, servingCAFile, servingTokenFile string
	servingNamespace, storeName                 string
	profileNamespaces                           []string
	requestTimeout                              time.Duration
	maxRequestBytes, maxEvidenceBytes           int64
	tempDir                                     string
	systemName                                  string
	namespace                                   string
	factSourceConfigMap                         string
	executionFactsKey                           string
	executionPoliciesKey                        string
	measurementMemoryFraction                   float64
	kubeAPIQPS                                  float64
	kubeAPIBurst                                int
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.servingURL, "serving-url", "", "Fixed HTTPS serving owner origin for typed catalog reads.")
	flags.StringVar(&o.servingCAFile, "serving-ca-file", "", "CA file for the serving owner.")
	flags.StringVar(
		&o.servingTokenFile,
		"serving-token-file",
		"/var/run/secrets/kubernetes.io/serviceaccount/token",
		"Token file for authenticated serving-owner reads.",
	)
	flags.StringVar(
		&o.servingNamespace,
		"serving-namespace",
		"",
		"Allowed measurement namespace; defaults to management namespace.",
	)
	flags.Func(
		"profile-namespace",
		"Allowed measurement namespace (repeatable).",
		func(s string) error { o.profileNamespaces = append(o.profileNamespaces, s); return nil },
	)
	flags.StringVar(&o.storeName, "store-name", "", "Logical Evidence Store used by measurement projections.")
	flags.DurationVar(&o.requestTimeout, "request-timeout", 30*time.Second, "Deadline for query source reads.")
	flags.Int64Var(&o.maxRequestBytes, "max-request-bytes", 8<<20, "Maximum query response bytes.")
	flags.Int64Var(&o.maxEvidenceBytes, "max-evidence-bytes", 64<<20, "Maximum retained measurement evidence bytes.")
	flags.StringVar(&o.bindAddress, "bind-address", ":8080", "Address for the CHILL Query API read API.")
	flags.Float64Var(&o.kubeAPIQPS, "kube-api-qps", 200, "Kubernetes API client QPS.")
	flags.IntVar(&o.kubeAPIBurst, "kube-api-burst", 400, "Kubernetes API client burst.")
	flags.StringVar(
		&o.tempDir, "evidence-temp-dir", "/var/run/chill/evidence",
		"Directory for bounded Evidence Store reads.",
	)
	flags.StringVar(&o.systemName, "system-name", "", "ChillSystem name that owns the Evidence Store.")
	flags.StringVar(
		&o.namespace, "namespace", os.Getenv("POD_NAMESPACE"),
		"Management namespace holding the Evidence Store operand.",
	)
	flags.StringVar(&o.factSourceConfigMap, "fact-source-config-map", "", "Profiler fact-source ConfigMap name.")
	flags.StringVar(&o.executionFactsKey, "execution-facts-key", "", "ConfigMap key containing execution facts.")
	flags.StringVar(
		&o.executionPoliciesKey, "execution-policies-key", "",
		"ConfigMap key containing versioned worker execution envelopes.",
	)
	flags.Float64Var(
		&o.measurementMemoryFraction, "measurement-memory-fraction", 0,
		"Required measurement runtime fraction of Node allocatable memory (0 < fraction < 1).",
	)
}

func (o options) validate() error {
	if o.requestTimeout <= 0 || o.maxRequestBytes <= 0 || o.maxEvidenceBytes <= 0 {
		return errors.New("query timeout and read bounds must be positive")
	}
	if math.IsNaN(o.measurementMemoryFraction) || o.measurementMemoryFraction <= 0 || o.measurementMemoryFraction >= 1 {
		return errors.New("measurement-memory-fraction must be between 0 and 1 (exclusive)")
	}
	if o.kubeAPIQPS <= 0 || math.IsNaN(o.kubeAPIQPS) || math.IsInf(o.kubeAPIQPS, 0) ||
		o.kubeAPIQPS > math.MaxFloat32 || float32(o.kubeAPIQPS) == 0 {
		return errors.New("Kubernetes API QPS must be positive and finite in float32 range")
	}
	if o.kubeAPIBurst <= 0 {
		return errors.New("Kubernetes API burst must be positive")
	}
	if strings.TrimSpace(o.bindAddress) == "" || strings.TrimSpace(o.bindAddress) != o.bindAddress {
		return errors.New("bind address is required without surrounding whitespace")
	}
	if !strings.HasPrefix(o.tempDir, "/") || strings.TrimSpace(o.tempDir) != o.tempDir {
		return errors.New("evidence temporary directory must be an absolute path without surrounding whitespace")
	}
	for name, value := range map[string]string{
		"system name":            o.systemName,
		"namespace":              o.namespace,
		"fact-source ConfigMap":  o.factSourceConfigMap,
		"execution-facts key":    o.executionFactsKey,
		"execution-policies key": o.executionPoliciesKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return nil
}
