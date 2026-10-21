package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"k8s.io/apimachinery/pkg/util/validation"
)

// options holds every node-agent flag. Discovery reads the node, system,
// host, signature, and interval fields; --actuate reads the rest.
type options struct {
	actuate          bool
	nodeName         string
	systemName       string
	hostRoot         string
	signatureFile    string
	interval         time.Duration
	once             bool
	allowedPolicies  policyListFlag
	jetsonCommand    stringListFlag
	jetsonTimeout    time.Duration
	kubeAPIServer    string
	kubeAPITokenFile string
	kubeAPICAFile    string
	metricsAddr      string
	probeAddr        string
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.BoolVar(&o.actuate, "actuate", false,
		"Apply the desired power mode the assigned Node's label names instead of publishing discovery metadata.")
	flags.StringVar(&o.nodeName, "node-name", o.nodeName, "Exact assigned Kubernetes Node name.")
	flags.StringVar(&o.systemName, "system-name", o.systemName, "ChillSystem name that owns this node agent.")
	flags.StringVar(&o.hostRoot, "host-root", "/host", "Root path containing read-only host mounts.")
	flags.StringVar(&o.signatureFile, "signature-file", "/etc/chill/node-agent/signatures.yaml",
		"YAML file containing node discovery signatures.")
	flags.DurationVar(&o.interval, "interval", 10*time.Second, "How often to refresh node discovery metadata.")
	flags.BoolVar(&o.once, "once", false, "Run one discovery pass and exit.")
	flags.Var(
		&o.allowedPolicies, "allowed-policy",
		"Authorized actuation-policy JSON object; repeatable.",
	)
	flags.Var(
		&o.jetsonCommand, "jetson-nvpmodel-command",
		"One ordered Jetson NVPModel command token; repeatable.",
	)
	flags.DurationVar(
		&o.jetsonTimeout, "jetson-nvpmodel-timeout", 30*time.Second,
		"Maximum duration of one Jetson NVPModel host command.",
	)
	flags.StringVar(&o.kubeAPIServer, "kube-api-server", o.kubeAPIServer,
		"Kubernetes API URL; empty uses standard in-cluster config.")
	flags.StringVar(
		&o.kubeAPITokenFile, "kube-api-token-file", kubeconfig.DefaultServiceAccountTokenFile,
		"Path to the service account bearer token file.",
	)
	flags.StringVar(
		&o.kubeAPICAFile, "kube-api-ca-file", kubeconfig.DefaultServiceAccountCAFile,
		"Path to the Kubernetes API CA file.",
	)
	flags.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "Metrics endpoint bind address, or 0 to disable.")
	flags.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
}

func (o options) validateActuation() error {
	for name, value := range map[string]string{
		"node name": o.nodeName, "metrics bind address": o.metricsAddr,
		"health probe bind address": o.probeAddr, "Kubernetes API token file": o.kubeAPITokenFile,
		"Kubernetes API CA file": o.kubeAPICAFile,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if len(validation.IsDNS1123Subdomain(o.nodeName)) != 0 {
		return errors.New("node name must be a valid DNS subdomain")
	}
	if len(o.allowedPolicies) == 0 {
		return errors.New("at least one actuation policy is required")
	}
	if len(o.jetsonCommand) == 0 || o.jetsonTimeout < time.Second || o.jetsonTimeout > 5*time.Minute {
		return errors.New("Jetson NVPModel command and a timeout between 1 second and 5 minutes are required")
	}
	if strings.TrimSpace(o.kubeAPIServer) != o.kubeAPIServer {
		return errors.New("Kubernetes API server must not have surrounding whitespace")
	}
	if err := validateUniquePolicies(o.allowedPolicies); err != nil {
		return err
	}
	for _, argument := range o.jetsonCommand {
		if strings.TrimSpace(argument) == "" || strings.TrimSpace(argument) != argument || strings.ContainsRune(argument, 0) {
			return errors.New("Jetson NVPModel command tokens must be non-empty without surrounding whitespace or NUL")
		}
	}
	return nil
}

type stringListFlag []string

type policyListFlag []nodeactuator.ActuationPolicySpec

func (f *policyListFlag) String() string {
	return fmt.Sprintf("%v", []nodeactuator.ActuationPolicySpec(*f))
}

func (f *policyListFlag) Set(value string) error {
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	var policy nodeactuator.ActuationPolicySpec
	if err := decoder.Decode(&policy); err != nil {
		return fmt.Errorf("decode allowed actuation policy: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("allowed actuation policy must contain exactly one JSON object")
	}
	*f = append(*f, policy)
	return nil
}

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return errors.New("repeated value is required without surrounding whitespace")
	}
	*f = append(*f, value)
	return nil
}

func validateUniquePolicies(policies []nodeactuator.ActuationPolicySpec) error {
	seen := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if err := policy.Validate(); err != nil {
			return err
		}
		selector := policy.DeviceClass + "\x00" + policy.PowerMode + "\x00" + policy.ActuatorKind
		if _, duplicate := seen[selector]; duplicate {
			return fmt.Errorf("duplicate allowed actuation-policy selector %q", selector)
		}
		seen[selector] = struct{}{}
	}
	return nil
}
