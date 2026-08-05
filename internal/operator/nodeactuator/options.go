package nodeactuator

import (
	"cmp"
	"errors"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/component"
)

const (
	DefaultConfigKey         = "config.yaml"
	DefaultReconcileInterval = 10 * time.Second
)

type Options struct {
	SystemName        string
	Namespace         string
	DaemonSetName     string
	ConfigMapName     string
	ConfigMapKey      string
	ReconcileInterval time.Duration
}

func (o Options) Defaulted() Options {
	systemName := cmp.Or(strings.TrimSpace(o.SystemName), component.DefaultSystemName)
	defaulted := Options{
		SystemName: systemName, Namespace: strings.TrimSpace(o.Namespace),
		DaemonSetName: strings.TrimSpace(o.DaemonSetName),
		ConfigMapName: strings.TrimSpace(o.ConfigMapName), ConfigMapKey: strings.TrimSpace(o.ConfigMapKey),
		ReconcileInterval: o.ReconcileInterval,
	}
	if defaulted.DaemonSetName == "" {
		defaulted.DaemonSetName = component.NodeActuatorDaemonSetName(systemName)
	}
	if defaulted.ConfigMapName == "" {
		defaulted.ConfigMapName = component.NodeActuatorConfigMapName(systemName)
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
	if defaulted.Namespace == "" || defaulted.SystemName == "" || defaulted.DaemonSetName == "" ||
		defaulted.ConfigMapName == "" || defaulted.ConfigMapKey == "" {
		return errors.New("NodeActuator system, namespace, DaemonSet, and ConfigMap identities are required")
	}
	if defaulted.ReconcileInterval <= 0 {
		return errors.New("NodeActuator reconcile interval must be positive")
	}
	*o = defaulted
	return nil
}
