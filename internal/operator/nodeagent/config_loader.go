package nodeagent

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

func (r *Reconciler) loadConfig(ctx context.Context, options Options, requireActuation bool) (Config, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: options.Namespace, Name: options.ConfigMapName}
	if err := r.Get(ctx, key, configMap); err != nil {
		return Config{}, fmt.Errorf("get node agent config ConfigMap %s: %w", key, err)
	}
	raw := configMap.Data[DefaultConfigKey]
	if raw == "" {
		return Config{}, fmt.Errorf("node agent config ConfigMap %s missing key %q", key, DefaultConfigKey)
	}
	var config Config
	if err := yaml.UnmarshalStrict([]byte(raw), &config); err != nil {
		return Config{}, fmt.Errorf("parse node agent config ConfigMap %s: %w", key, err)
	}
	if err := config.Validate(requireActuation); err != nil {
		return Config{}, fmt.Errorf("validate node agent config ConfigMap %s: %w", key, err)
	}
	return config, nil
}
