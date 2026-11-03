package main

import (
	"context"
	"encoding/json"

	"slices"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type measurementService struct {
	client  client.Reader
	objects evidencestore.LocationReader
	opts    options

	catalog  func(context.Context) (servingkube.CatalogRead, error)
	coverage *querykube.CoverageService
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

func (o *measurementService) listOperationCosts(
	ctx context.Context, namespace string,
) (httpapi.MeasurementProjectionList, error) {
	result := httpapi.MeasurementProjectionList{Items: []httpapi.MeasurementProjection{}}
	list, err := querykube.ListOperationCostMeasurements(ctx, o.client, namespace, o.namespace)
	if err != nil {
		return result, err
	}
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
	cr, err := querykube.GetOperationCostMeasurement(ctx, o.client, namespace, name, o.namespace)
	if err != nil {
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
	projection.Assessment, err = json.Marshal(queryapi.AssessOperationCost(cr, queryOperationSteps(steps)))
	return projection, err
}

func (o *measurementService) listDispatchOccupancies(
	ctx context.Context, namespace string,
) (dispatchOccupancyProjectionList, error) {
	result := dispatchOccupancyProjectionList{Items: []dispatchOccupancyProjection{}}
	list, err := querykube.ListDispatchOccupancyMeasurements(ctx, o.client, namespace, o.namespace)
	if err != nil {
		return result, err
	}
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
	cr, err := querykube.GetDispatchOccupancyMeasurement(ctx, o.client, namespace, name, o.namespace)
	if err != nil {
		return dispatchOccupancyProjection{}, err
	}
	return o.projectDispatchOccupancy(ctx, cr)
}

func catalogView(read servingkube.CatalogSnapshot) httpapi.ProfilesView {
	out := httpapi.ProfilesView{
		ConfigVersion: read.ConfigVersion,
		CatalogView:   read.CatalogView(),
		Preparations:  map[string]servingkube.PrepareSpec{},
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

func queryOperationSteps(steps []servingkube.OperationStepProjection) []queryapi.OperationStepEvidence {
	if steps == nil {
		return nil
	}
	facts := make([]queryapi.OperationStepEvidence, len(steps))
	for i, step := range steps {
		facts[i] = queryapi.OperationStepEvidence{
			OperationID: step.OperationID, DurationNanos: step.DurationNanos,
			WallJoules: step.WallJoules, Reason: step.Reason,
		}
	}
	return facts
}
