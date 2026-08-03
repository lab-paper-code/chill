package profiler

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/component"
)

const DefaultConfigKey = "config.yaml"

// Options selects the Profiler Deployment and its rendered configuration.
type Options struct {
	SystemName     string
	Namespace      string
	DeploymentName string
	ConfigMapName  string
	ConfigMapKey   string
}

func (o Options) Defaulted() Options {
	systemName := cmp.Or(strings.TrimSpace(o.SystemName), component.DefaultSystemName)
	defaulted := Options{
		SystemName:     systemName,
		Namespace:      strings.TrimSpace(o.Namespace),
		DeploymentName: strings.TrimSpace(o.DeploymentName),
		ConfigMapName:  strings.TrimSpace(o.ConfigMapName),
		ConfigMapKey:   strings.TrimSpace(o.ConfigMapKey),
	}
	if defaulted.DeploymentName == "" {
		defaulted.DeploymentName = component.ProfilerDeploymentName(systemName)
	}
	if defaulted.ConfigMapName == "" {
		defaulted.ConfigMapName = component.ProfilerConfigMapName(systemName)
	}
	if defaulted.ConfigMapKey == "" {
		defaulted.ConfigMapKey = DefaultConfigKey
	}
	return defaulted
}

func (o *Options) DefaultAndValidate() error {
	defaulted := o.Defaulted()
	if defaulted.Namespace == "" {
		return fmt.Errorf("Profiler namespace is required")
	}
	if defaulted.SystemName == "" {
		return fmt.Errorf("Profiler system name is required")
	}
	if defaulted.DeploymentName == "" {
		return fmt.Errorf("Profiler Deployment name is required")
	}
	if defaulted.ConfigMapName == "" {
		return fmt.Errorf("Profiler config ConfigMap name is required")
	}
	if defaulted.ConfigMapKey == "" {
		return fmt.Errorf("Profiler config ConfigMap key is required")
	}
	*o = defaulted
	return nil
}
