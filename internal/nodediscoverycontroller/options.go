package nodediscoverycontroller

import (
	"fmt"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/components"
)

const (
	DefaultConfigKey         = "config.yaml"
	DefaultReconcileInterval = 30 * time.Second
)

// Options selects the node-discovery DaemonSet managed by the operator.
type Options struct {
	Enabled           bool
	SystemName        string
	Namespace         string
	DaemonSetName     string
	ConfigMapName     string
	ConfigMapKey      string
	ReconcileInterval time.Duration
}

func (o Options) Defaulted() Options {
	systemName := defaultString(o.SystemName, components.DefaultSystemName)
	defaulted := Options{
		Enabled:           o.Enabled,
		SystemName:        systemName,
		Namespace:         strings.TrimSpace(o.Namespace),
		DaemonSetName:     strings.TrimSpace(o.DaemonSetName),
		ConfigMapName:     strings.TrimSpace(o.ConfigMapName),
		ConfigMapKey:      strings.TrimSpace(o.ConfigMapKey),
		ReconcileInterval: o.ReconcileInterval,
	}
	if defaulted.DaemonSetName == "" {
		defaulted.DaemonSetName = components.NodeDiscoveryDaemonSetName(systemName)
	}
	if defaulted.ConfigMapName == "" {
		defaulted.ConfigMapName = components.NodeDiscoveryConfigMapName(systemName)
	}
	if defaulted.ConfigMapKey == "" {
		defaulted.ConfigMapKey = DefaultConfigKey
	}
	if defaulted.ReconcileInterval == 0 {
		defaulted.ReconcileInterval = DefaultReconcileInterval
	}
	return defaulted
}

func (o *Options) DefaultAndValidate() error {
	defaulted := o.Defaulted()
	if defaulted.Namespace == "" {
		return fmt.Errorf("node-discovery namespace is required")
	}
	if defaulted.SystemName == "" {
		return fmt.Errorf("node-discovery system name is required")
	}
	if defaulted.DaemonSetName == "" {
		return fmt.Errorf("node-discovery DaemonSet name is required")
	}
	if defaulted.ConfigMapName == "" {
		return fmt.Errorf("node-discovery config ConfigMap name is required")
	}
	if defaulted.ConfigMapKey == "" {
		return fmt.Errorf("node-discovery config ConfigMap key is required")
	}
	if defaulted.ReconcileInterval <= 0 {
		return fmt.Errorf("node-discovery reconcile interval must be positive")
	}
	*o = defaulted
	return nil
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
