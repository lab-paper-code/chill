package kube

import (
	"context"
	"sort"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ListOperationCostMeasurements(
	ctx context.Context, reader client.Reader, namespace string, authorize func(string) error,
) (api.OperationCostMeasurementList, error) {
	var list api.OperationCostMeasurementList
	if err := authorize(namespace); err != nil {
		return list, err
	}
	if err := reader.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return list, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list, nil
}

func GetOperationCostMeasurement(
	ctx context.Context, reader client.Reader, namespace, name string, authorize func(string) error,
) (api.OperationCostMeasurement, error) {
	var cr api.OperationCostMeasurement
	if err := authorize(namespace); err != nil {
		return cr, err
	}
	err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cr)
	return cr, err
}

func ListDispatchOccupancyMeasurements(
	ctx context.Context, reader client.Reader, namespace string, authorize func(string) error,
) (api.DispatchOccupancyMeasurementList, error) {
	var list api.DispatchOccupancyMeasurementList
	if err := authorize(namespace); err != nil {
		return list, err
	}
	if err := reader.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return list, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list, nil
}

func GetDispatchOccupancyMeasurement(
	ctx context.Context, reader client.Reader, namespace, name string, authorize func(string) error,
) (api.DispatchOccupancyMeasurement, error) {
	var cr api.DispatchOccupancyMeasurement
	if err := authorize(namespace); err != nil {
		return cr, err
	}
	err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cr)
	return cr, err
}
