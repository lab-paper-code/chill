package system

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"
)

// Options configures CHILL system reconciliation defaults.
type Options struct {
	SystemName             string
	Namespace              string
	OperatorDeploymentName string
	RefreshInterval        time.Duration
}

// DefaultNamespace returns the operator Pod namespace when it can be resolved.
func DefaultNamespace() string {
	if namespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); namespace != "" {
		return namespace
	}
	return ""
}

func (o Options) Defaulted() Options {
	defaulted := Options{
		SystemName:             defaults.String(o.SystemName, component.DefaultSystemName),
		Namespace:              strings.TrimSpace(o.Namespace),
		OperatorDeploymentName: strings.TrimSpace(o.OperatorDeploymentName),
		RefreshInterval:        o.RefreshInterval,
	}
	if defaulted.OperatorDeploymentName == "" {
		defaulted.OperatorDeploymentName = DefaultOperatorDeploymentName()
	}
	return defaulted
}

func (o *Options) DefaultAndValidate() error {
	defaulted := o.Defaulted()
	if defaulted.Namespace == "" {
		return fmt.Errorf("operator namespace is required; set --operator-namespace or POD_NAMESPACE")
	}
	if defaulted.RefreshInterval <= 0 {
		return fmt.Errorf("system status refresh interval must be positive")
	}
	*o = defaulted
	return nil
}

func DefaultOperatorDeploymentName() string {
	return component.OperatorDeploymentName(component.DefaultSystemName)
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

func (r *ChillSystemReconciler) refreshInterval() time.Duration {
	return r.Options.Defaulted().RefreshInterval
}
