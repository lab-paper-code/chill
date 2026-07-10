package system

import (
	"fmt"
	"os"
	"strings"

	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"
)

// Options configures CHILL system reconciliation defaults.
type Options struct {
	SystemName string
	Namespace  string
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
		SystemName: defaults.String(o.SystemName, component.DefaultSystemName),
		Namespace:  strings.TrimSpace(o.Namespace),
	}
	return defaulted
}

func (o *Options) DefaultAndValidate() error {
	defaulted := o.Defaulted()
	if defaulted.Namespace == "" {
		return fmt.Errorf("operator namespace is required; set --operator-namespace or POD_NAMESPACE")
	}
	*o = defaulted
	return nil
}

func (r *ChillSystemReconciler) systemName() string {
	return r.Options.Defaulted().SystemName
}

func (r *ChillSystemReconciler) namespace() string {
	return r.Options.Defaulted().Namespace
}
