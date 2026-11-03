package main

import (
	"context"
	"encoding/json"
	"errors"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"io"

	"slices"
	"sort"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type measurementService struct {
	client  client.Reader
	objects evidencestore.LocationReader
	opts    options

	catalog       func(context.Context) (servingkube.CatalogRead, error)
	inventory     func(context.Context) (queryapi.InventoryView, error)
	compatibility func(context.Context) (queryapi.ModelCompatibilityMatrixView, error)
	coverageCache measurementCoverageCache
}

func (o *measurementService) namespace(namespace string) error {
	if len(validation.IsDNS1123Label(namespace)) != 0 {
		return &serving.Error{Code: "InvalidInput", Message: "valid measurement namespace is required"}
	}
	allowed := namespace == o.opts.servingNamespace
	if len(o.opts.profileNamespaces) != 0 {
		allowed = slices.Contains(o.opts.profileNamespaces, namespace)
	}
	if !allowed {
		return servingkube.ErrForbidden
	}
	return nil
}

func (o *measurementService) list(ctx context.Context, namespace string) (api.BatchEnergyOptimizationList, error) {
	result := api.BatchEnergyOptimizationList{}
	if err := o.namespace(namespace); err != nil {
		return result, err
	}
	if err := o.client.List(ctx, &result, client.InNamespace(namespace)); err != nil {
		return result, err
	}
	result.APIVersion, result.Kind = api.GroupVersion.String(), "BatchEnergyOptimizationList"
	if result.Items == nil {
		result.Items = []api.BatchEnergyOptimization{}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Name < result.Items[j].Name })
	return result, nil
}

func (o *measurementService) listOperationCosts(
	ctx context.Context, namespace string,
) (httpapi.MeasurementProjectionList, error) {
	result := httpapi.MeasurementProjectionList{Items: []httpapi.MeasurementProjection{}}
	if err := o.namespace(namespace); err != nil {
		return result, err
	}
	var list api.OperationCostMeasurementList
	if err := o.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return result, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	result.Items = make([]httpapi.MeasurementProjection, len(list.Items))
	group, readCtx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for i, cr := range list.Items {
		group.Go(func() error {
			projection, err := o.projectOperationCost(readCtx, cr)
			if err == nil {
				result.Items[i] = projection
			}
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return httpapi.MeasurementProjectionList{}, err
	}
	return result, nil
}

func (o *measurementService) getOperationCost(
	ctx context.Context, namespace, name string,
) (httpapi.MeasurementProjection, error) {
	if err := o.namespace(namespace); err != nil {
		return httpapi.MeasurementProjection{}, err
	}
	var cr api.OperationCostMeasurement
	if err := o.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cr); err != nil {
		return httpapi.MeasurementProjection{}, err
	}
	return o.projectOperationCost(ctx, cr)
}

func (o *measurementService) projectOperationCost(
	ctx context.Context,
	cr api.OperationCostMeasurement,
) (httpapi.MeasurementProjection, error) {
	steps, err := servingkube.LoadOperationCostSteps(
		ctx,
		cr,
		o.objects,
		o.opts.storeName,
		o.opts.maxEvidenceBytes,
	)
	if err != nil {
		return httpapi.MeasurementProjection{}, err
	}
	projection := httpapi.ProjectOperationCostMeasurement(cr, o.opts.storeName, steps)
	projection.Assessment, err = json.Marshal(assessOperationCost(cr, steps))
	return projection, err
}

func (o *measurementService) listDispatchOccupancies(
	ctx context.Context, namespace string,
) (dispatchOccupancyProjectionList, error) {
	result := dispatchOccupancyProjectionList{Items: []dispatchOccupancyProjection{}}
	if err := o.namespace(namespace); err != nil {
		return result, err
	}
	var list api.DispatchOccupancyMeasurementList
	if err := o.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return result, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	for _, cr := range list.Items {
		projection, err := o.projectDispatchOccupancy(ctx, cr)
		if err != nil {
			return dispatchOccupancyProjectionList{}, err
		}
		result.Items = append(result.Items, projection)
	}
	return result, nil
}

func (o *measurementService) getDispatchOccupancy(
	ctx context.Context, namespace, name string,
) (dispatchOccupancyProjection, error) {
	if err := o.namespace(namespace); err != nil {
		return dispatchOccupancyProjection{}, err
	}
	var cr api.DispatchOccupancyMeasurement
	if err := o.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cr); err != nil {
		return dispatchOccupancyProjection{}, err
	}
	return o.projectDispatchOccupancy(ctx, cr)
}

func (o *measurementService) readEvidence(ctx context.Context, location evidencestore.ObjectLocation) ([]byte, error) {
	stream, err := o.objects.OpenLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, o.opts.maxEvidenceBytes+1))
	closeErr := stream.Close()
	if errors.Join(readErr, closeErr) != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(raw)) > o.opts.maxEvidenceBytes {
		return nil, errors.New("measurement evidence exceeds read bound")
	}
	return raw, nil
}

func catalogView(read servingkube.CatalogSnapshot) httpapi.ProfilesView {
	out := httpapi.ProfilesView{
		ConfigVersion: read.ConfigVersion,
		CatalogView:   read.CatalogView(),
		Preparations:  map[string]servingstate.PrepareSpec{},
	}
	out.CurrentMeasurements = read.CurrentMeasurements
	for id, p := range read.Preparations {
		spec := p.PrepareSpec
		spec.Instance = p.Instance.SchedulerInstance
		if p.Instance.Profile != nil {
			profile := p.Instance.Profile.Profile()
			spec.Instance.Profile = &profile
		}
		out.Preparations[id] = spec
	}
	return out
}
