package kube

import (
	"context"
	"sort"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ListMeasurementRequests(
	ctx context.Context, reader client.Reader, namespace string, authorize func(string) error,
) (api.BatchEnergyOptimizationList, error) {
	result := api.BatchEnergyOptimizationList{}
	if err := authorize(namespace); err != nil {
		return result, err
	}
	if err := reader.List(ctx, &result, client.InNamespace(namespace)); err != nil {
		return result, err
	}
	result.APIVersion, result.Kind = api.GroupVersion.String(), "BatchEnergyOptimizationList"
	if result.Items == nil {
		result.Items = []api.BatchEnergyOptimization{}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Name < result.Items[j].Name })
	return result, nil
}
