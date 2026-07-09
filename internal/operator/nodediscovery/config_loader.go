package nodediscovery

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

func (r *Reconciler) loadConfig(ctx context.Context, options Options) (Config, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: options.Namespace, Name: options.ConfigMapName}
	if err := r.Get(ctx, key, configMap); err != nil {
		return Config{}, fmt.Errorf("get node-discovery config ConfigMap %s: %w", key.String(), err)
	}

	raw := configMap.Data[options.ConfigMapKey]
	if raw == "" {
		return Config{}, fmt.Errorf("node-discovery config ConfigMap %s missing key %q", key.String(), options.ConfigMapKey)
	}

	var config Config
	if err := yaml.Unmarshal([]byte(raw), &config); err != nil {
		return Config{}, fmt.Errorf("parse node-discovery config ConfigMap %s: %w", key.String(), err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate node-discovery config ConfigMap %s: %w", key.String(), err)
	}
	return config, nil
}
