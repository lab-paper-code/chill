package evidencestore

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/component"
)

const DefaultConfigKey = component.EvidenceStoreConfigKey

// Options supplies operator-level defaults for Evidence Store instances.
type Options struct {
	SystemName   string
	Namespace    string
	ConfigMapKey string
}

func (o Options) Defaulted() Options {
	systemName := cmp.Or(strings.TrimSpace(o.SystemName), component.DefaultSystemName)
	defaulted := Options{
		SystemName:   systemName,
		Namespace:    strings.TrimSpace(o.Namespace),
		ConfigMapKey: strings.TrimSpace(o.ConfigMapKey),
	}
	if defaulted.ConfigMapKey == "" {
		defaulted.ConfigMapKey = DefaultConfigKey
	}
	return defaulted
}

func (o *Options) DefaultAndValidate() error {
	defaulted := o.Defaulted()
	required := []struct {
		name  string
		value string
	}{
		{name: "namespace", value: defaulted.Namespace},
		{name: "system name", value: defaulted.SystemName},
		{name: "config ConfigMap key", value: defaulted.ConfigMapKey},
	}
	for _, field := range required {
		if field.value == "" {
			return fmt.Errorf("Evidence Store %s is required", field.name)
		}
	}
	*o = defaulted
	return nil
}
