package discoverycontroller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lab-paper-code/chill/internal/deviceclasscatalog"
)

func (r *DeviceDiscoveryReconciler) catalogKey() string {
	if r.Options.CatalogKey != "" {
		return r.Options.CatalogKey
	}
	return deviceclasscatalog.CatalogDataKey
}

func (r *DeviceDiscoveryReconciler) loadCatalog(ctx context.Context) (deviceclasscatalog.Catalog, error) {
	if r.Options.CatalogName == "" {
		return deviceclasscatalog.Catalog{}, nil
	}

	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Namespace: r.Options.CatalogNamespace,
		Name:      r.Options.CatalogName,
	}
	if err := r.Get(ctx, key, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return deviceclasscatalog.Catalog{}, nil
		}
		return deviceclasscatalog.Catalog{}, fmt.Errorf("get discovery catalog configmap %s/%s: %w", key.Namespace, key.Name, err)
	}

	raw := configMap.Data[r.catalogKey()]
	if raw == "" {
		return deviceclasscatalog.Catalog{}, nil
	}

	var catalog deviceclasscatalog.Catalog
	if err := yaml.Unmarshal([]byte(raw), &catalog); err != nil {
		return deviceclasscatalog.Catalog{}, fmt.Errorf("parse discovery catalog %s/%s: %w", key.Namespace, key.Name, err)
	}
	if err := deviceclasscatalog.ValidateCatalog(catalog); err != nil {
		return deviceclasscatalog.Catalog{}, fmt.Errorf("validate discovery catalog %s/%s: %w", key.Namespace, key.Name, err)
	}
	return catalog, nil
}
