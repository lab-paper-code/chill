package system

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"
)

const (
	DefaultSystemName      = component.DefaultSystemName
	DefaultRefreshInterval = 30 * time.Second

	serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// Options configures the namespace-local CHILL status surface.
type Options struct {
	SystemName                 string
	Namespace                  string
	OperatorDeploymentName     string
	NodeDiscoveryDaemonSetName string
	NodeDiscoveryEnabled       bool
	RefreshInterval            time.Duration
}

// DefaultNamespace returns the operator Pod namespace when it can be resolved.
func DefaultNamespace() string {
	if namespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); namespace != "" {
		return namespace
	}
	data, err := os.ReadFile(serviceAccountNamespaceFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (o Options) Defaulted() Options {
	defaulted := Options{
		SystemName:                 defaults.String(o.SystemName, DefaultSystemName),
		Namespace:                  strings.TrimSpace(o.Namespace),
		OperatorDeploymentName:     strings.TrimSpace(o.OperatorDeploymentName),
		NodeDiscoveryDaemonSetName: strings.TrimSpace(o.NodeDiscoveryDaemonSetName),
		NodeDiscoveryEnabled:       o.NodeDiscoveryEnabled,
		RefreshInterval:            o.RefreshInterval,
	}
	if defaulted.OperatorDeploymentName == "" {
		defaulted.OperatorDeploymentName = DefaultOperatorDeploymentName()
	}
	if defaulted.NodeDiscoveryDaemonSetName == "" {
		defaulted.NodeDiscoveryDaemonSetName = DefaultNodeDiscoveryDaemonSetName()
	}
	if defaulted.RefreshInterval == 0 {
		defaulted.RefreshInterval = DefaultRefreshInterval
	}
	return defaulted
}

func (o *Options) DefaultAndValidate() error {
	defaulted := o.Defaulted()
	if defaulted.Namespace == "" {
		return fmt.Errorf("system status namespace is required; set --system-status-namespace or POD_NAMESPACE")
	}
	if defaulted.RefreshInterval <= 0 {
		return fmt.Errorf("system status refresh interval must be positive")
	}
	*o = defaulted
	return nil
}

func DefaultOperatorDeploymentName() string {
	return component.OperatorDeploymentName(DefaultSystemName)
}

func DefaultNodeDiscoveryDaemonSetName() string {
	return component.NodeDiscoveryDaemonSetName(DefaultSystemName)
}

func (r *ChillSystemReconciler) systemName() string {
	return r.Options.Defaulted().SystemName
}

func (r *ChillSystemReconciler) namespace() string {
	return r.Options.Defaulted().Namespace
}

func (r *ChillSystemReconciler) operatorDeploymentName() string {
	return r.Options.Defaulted().OperatorDeploymentName
}

func (r *ChillSystemReconciler) nodeDiscoveryDaemonSetName() string {
	return r.Options.Defaulted().NodeDiscoveryDaemonSetName
}

func (r *ChillSystemReconciler) refreshInterval() time.Duration {
	return r.Options.Defaulted().RefreshInterval
}
