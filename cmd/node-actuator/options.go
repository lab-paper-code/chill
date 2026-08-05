package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"k8s.io/apimachinery/pkg/util/validation"
)

type options struct {
	nodeName             string
	podUID               string
	watchNamespace       string
	leaseNamespace       string
	leaseDuration        int
	safetyValidFor       time.Duration
	allowedNamespaces    stringListFlag
	allowedPolicyDigests stringListFlag
	metricsAddr          string
	probeAddr            string
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.nodeName, "node-name", o.nodeName, "Exact assigned Kubernetes Node name.")
	flags.StringVar(&o.podUID, "pod-uid", o.podUID, "Exact NodeActuator Pod UID excluded from workload safety.")
	flags.StringVar(&o.watchNamespace, "watch-namespace", o.watchNamespace, "Namespace containing NodeActuation handoffs.")
	flags.StringVar(&o.leaseNamespace, "lease-namespace", o.leaseNamespace, "Namespace containing Node-UID Leases.")
	flags.IntVar(&o.leaseDuration, "lease-duration-seconds", 30, "Node-UID Lease duration in seconds.")
	flags.DurationVar(&o.safetyValidFor, "safety-valid-for", time.Minute, "Maximum workload-safety observation age.")
	flags.Var(
		&o.allowedNamespaces, "allowed-namespace",
		"Explicit workload namespace allowed during actuation; repeatable.",
	)
	flags.Var(
		&o.allowedPolicyDigests, "allowed-policy-digest",
		"Authorized canonical actuation-policy digest; repeatable.",
	)
	flags.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "Metrics endpoint bind address, or 0 to disable.")
	flags.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
}

func (o options) validate() error {
	for name, value := range map[string]string{
		"node name": o.nodeName, "Pod UID": o.podUID, "watch namespace": o.watchNamespace,
		"Lease namespace": o.leaseNamespace, "metrics bind address": o.metricsAddr,
		"health probe bind address": o.probeAddr,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if len(validation.IsDNS1123Subdomain(o.nodeName)) != 0 {
		return errors.New("node name must be a valid DNS subdomain")
	}
	for _, namespace := range append([]string{o.watchNamespace, o.leaseNamespace}, o.allowedNamespaces...) {
		if len(validation.IsDNS1123Label(namespace)) != 0 {
			return fmt.Errorf("invalid namespace %q", namespace)
		}
	}
	if o.leaseDuration < 5 || o.leaseDuration > 300 {
		return errors.New("Lease duration must be between 5 and 300 seconds")
	}
	if o.safetyValidFor < time.Second || o.safetyValidFor > 5*time.Minute {
		return errors.New("safety validity must be between 1 second and 5 minutes")
	}
	if len(o.allowedNamespaces) == 0 || len(o.allowedPolicyDigests) == 0 {
		return errors.New("at least one allowed namespace and policy digest are required")
	}
	if err := validateUniqueStrings(o.allowedNamespaces, "namespace", func(value string) error {
		if len(validation.IsDNS1123Label(value)) != 0 {
			return fmt.Errorf("invalid namespace %q", value)
		}
		return nil
	}); err != nil {
		return err
	}
	return validateUniqueStrings(o.allowedPolicyDigests, "policy digest", nodeactuator.ValidatePolicyDigest)
}

type stringListFlag []string

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

func validateUniqueStrings(values []string, kind string, validate func(string) error) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validate(value); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate allowed %s %q", kind, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
