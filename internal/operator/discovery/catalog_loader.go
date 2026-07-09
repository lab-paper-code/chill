package discovery

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lab-paper-code/chill/internal/deviceclass"
)

func catalogKey(options DeviceDiscoveryOptions) string {
	if options.CatalogKey != "" {
		return options.CatalogKey
	}
	return deviceclass.CatalogDataKey
}

func (r *DeviceDiscoveryReconciler) loadCatalog(ctx context.Context, options DeviceDiscoveryOptions) (deviceclass.Catalog, error) {
	if options.CatalogName == "" {
		return deviceclass.Catalog{}, nil
	}

	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Namespace: options.CatalogNamespace,
		Name:      options.CatalogName,
	}
	if err := r.Get(ctx, key, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return deviceclass.Catalog{}, nil
		}
		return deviceclass.Catalog{}, fmt.Errorf("get discovery catalog configmap %s/%s: %w", key.Namespace, key.Name, err)
	}

	raw := configMap.Data[catalogKey(options)]
	if raw == "" {
		return deviceclass.Catalog{}, nil
	}

	var catalog deviceclass.Catalog
	if err := yaml.Unmarshal([]byte(raw), &catalog); err != nil {
		return deviceclass.Catalog{}, fmt.Errorf("parse discovery catalog %s/%s: %w", key.Namespace, key.Name, err)
	}
	if err := deviceclass.ValidateCatalog(catalog); err != nil {
		return deviceclass.Catalog{}, fmt.Errorf("validate discovery catalog %s/%s: %w", key.Namespace, key.Name, err)
	}
	return catalog, nil
}
