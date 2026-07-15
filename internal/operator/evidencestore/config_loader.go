package evidencestore

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func loadConfig(ctx context.Context, reader client.Reader, instance Instance) (Config, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.ConfigMapName}
	if err := reader.Get(ctx, key, configMap); err != nil {
		return Config{}, fmt.Errorf("get Evidence Store config ConfigMap %s: %w", key.String(), err)
	}

	raw := configMap.Data[instance.ConfigMapKey]
	if raw == "" {
		return Config{}, fmt.Errorf(
			"Evidence Store config ConfigMap %s missing key %q",
			key.String(),
			instance.ConfigMapKey,
		)
	}
	config, err := parseConfig([]byte(raw))
	if err != nil {
		return Config{}, fmt.Errorf("load Evidence Store config ConfigMap %s: %w", key.String(), err)
	}
	return config, nil
}
