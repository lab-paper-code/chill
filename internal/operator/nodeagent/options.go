package nodeagent

import (
	"cmp"
	"errors"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/component"
)

const (
	DefaultConfigKey         = "config.yaml"
	DefaultReconcileInterval = 30 * time.Second
	nvidiaActuationSuffix    = "-nvidia"
)

// Options selects the node agent DaemonSets managed by the operator.
type Options struct {
	SystemName        string
	Namespace         string
	ConfigMapName     string
	ReconcileInterval time.Duration
}

func (o Options) Defaulted() Options {
	systemName := cmp.Or(strings.TrimSpace(o.SystemName), component.DefaultSystemName)
	return Options{
		SystemName:        systemName,
		Namespace:         strings.TrimSpace(o.Namespace),
		ConfigMapName:     cmp.Or(strings.TrimSpace(o.ConfigMapName), component.NodeAgentConfigMapName(systemName)),
		ReconcileInterval: cmp.Or(o.ReconcileInterval, DefaultReconcileInterval),
	}
}

func (o *Options) DefaultAndValidate() error {
	defaulted := o.Defaulted()
	if defaulted.Namespace == "" {
		return errors.New("node agent namespace is required")
	}
	if defaulted.ReconcileInterval <= 0 {
		return errors.New("node agent reconcile interval must be positive")
	}
	*o = defaulted
	return nil
}

func (o Options) agentDaemonSetName() string {
	return component.NodeAgentDaemonSetName(o.SystemName)
}

func (o Options) actuationDaemonSetName() string {
	return component.NodeAgentActuationDaemonSetName(o.SystemName)
}
