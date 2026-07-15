package evidencestore

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

func parseConfig(raw []byte) (Config, error) {
	var config Config
	if err := yaml.UnmarshalStrict(raw, &config); err != nil {
		return Config{}, fmt.Errorf("parse Evidence Store config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate Evidence Store config: %w", err)
	}
	return config, nil
}
