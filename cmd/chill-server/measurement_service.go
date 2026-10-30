package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"golang.org/x/sync/errgroup"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The measurement service removes only source-proven, unreferenced measurement
// evidence before requesting native deletion. Native GC/finalizers stay native.
type measurementService struct {
	client        client.Client
	scheduler     *schedulerService
	kube          kubernetes.Interface
	objects       measurementObjects
	opts          options
	console       *url.URL
	http          *http.Client
	coverageCache measurementCoverageCache
}

type measurementObjects interface {
	evidencestore.LocationReader
	Delete(context.Context, evidencestore.ObjectLocation) error
}

func newMeasurementService(
	native client.Client,
	kube kubernetes.Interface,
	scheduler *schedulerService,
	objects measurementObjects,
	opts options,
) (*measurementService, error) {
	origin, err := privateOrigin(opts.consoleURL)
	if err != nil {
		return nil, err
	}
	return &measurementService{
		client: native, kube: kube, scheduler: scheduler, objects: objects, opts: opts, console: origin,
		http: &http.Client{
			Timeout:       opts.requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
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
) (httpapi.MeasurementProjectionList, error) {
	result := httpapi.MeasurementProjectionList{Items: []httpapi.MeasurementProjection{}}
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
			return httpapi.MeasurementProjectionList{}, err
		}
		result.Items = append(result.Items, projection)
	}
	return result, nil
}

func (o *measurementService) getDispatchOccupancy(
	ctx context.Context, namespace, name string,
) (httpapi.MeasurementProjection, error) {
	if err := o.namespace(namespace); err != nil {
		return httpapi.MeasurementProjection{}, err
	}
	var cr api.DispatchOccupancyMeasurement
	if err := o.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cr); err != nil {
		return httpapi.MeasurementProjection{}, err
	}
	return o.projectDispatchOccupancy(ctx, cr)
}

func (o *measurementService) preflight(ctx context.Context, task *api.BatchEnergyOptimization) error {
	unavailable := &serving.Error{
		Code: "MeasurementPreflightUnavailable", Message: "production Console preflight is unavailable",
	}
	if o.console == nil {
		return unavailable
	}
	body, err := json.Marshal(task)
	if err != nil {
		return err
	}
	target := *o.console
	target.Path = "/api/v1/namespaces/" + task.Namespace + "/batch-energy-optimizations/preflight"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := o.http.Do(req)
	if err != nil {
		return unavailable
	}
	const maximumPreflightBytes = 4 << 20
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maximumPreflightBytes+1))
	closeErr := response.Body.Close()
	if errors.Join(readErr, closeErr) != nil ||
		response.StatusCode != http.StatusOK || len(payload) > maximumPreflightBytes {
		return unavailable
	}
	var result operatorconsole.BatchEnergyOptimizationPreflight
	if json.Unmarshal(payload, &result) != nil ||
		result.APIVersion != operatorconsole.BatchEnergyOptimizationPreflightAPIVersion ||
		result.Kind != "BatchEnergyOptimizationPreflight" ||
		result.Campaign.Namespace != task.Namespace || result.Campaign.Name != task.Name {
		return unavailable
	}
	if !result.Resolves {
		return &serving.Error{Code: "MeasurementPreflightRejected", Message: result.Reason}
	}
	return nil
}

func (o *measurementService) submit(
	ctx context.Context, task api.BatchEnergyOptimization, dryRun bool,
) (httpapi.MeasurementMutationResult, error) {
	result := httpapi.MeasurementMutationResult{}
	if err := o.namespace(task.Namespace); err != nil {
		return result, err
	}
	if err := o.preflight(ctx, &task); err != nil {
		return result, err
	}
	options := &client.CreateOptions{}
	result.Request = "Accepted"
	if dryRun {
		options.DryRun = []string{meta.DryRunAll}
		result.Request = "NOT_SUBMITTED"
	}
	if err := o.client.Create(ctx, &task, options); err != nil {
		return httpapi.MeasurementMutationResult{}, err
	}
	result.Task = &task
	return result, nil
}

func (o *measurementService) delete(
	ctx context.Context, namespace, name, uid, version string,
) (httpapi.MeasurementMutationResult, error) {
	result := httpapi.MeasurementMutationResult{}
	if err := o.namespace(namespace); err != nil {
		return result, err
	}
	if uid == "" || version == "" {
		return result, &serving.Error{Code: "ResourceVersionRequired", Message: "native UID and resourceVersion are required"}
	}
	if o.scheduler != nil {
		o.scheduler.lock.Lock()
		defer o.scheduler.lock.Unlock()
	}
	task := &api.BatchEnergyOptimization{}
	if err := o.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, task); err != nil {
		return result, err
	}
	result.Task = task
	if string(task.UID) != uid || task.ResourceVersion != version {
		resource := schema.GroupResource{Group: api.GroupVersion.Group, Resource: "batchenergyoptimizations"}
		return result, apierrors.NewConflict(resource, name, errors.New("native UID or resourceVersion changed"))
	}
	if task.Status.Phase == measurementPhaseMeasuring {
		return result, &serving.Error{Code: "MeasurementInUse", Message: "an active measurement cannot be deleted"}
	}
	pods, err := o.kube.CoreV1().Pods(o.opts.servingNamespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return result, err
	}
	prefix := namespace + "/" + name + "/"
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.Annotations[servingkube.ServingProfileAnnotation], prefix) &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			return result, measurementInUse("a physical serving Pod still references the measurement")
		}
	}
	locations, err := o.measurementEvidence(ctx, task)
	if err != nil {
		return result, err
	}
	if err := o.checkSharedEvidence(ctx, task, locations); err != nil {
		return result, err
	}
	result.DeletedEvidence = make([]httpapi.MeasurementEvidenceLocation, 0, len(locations))
	for _, location := range locations {
		if err := o.objects.Delete(ctx, location); err != nil {
			return result, &serving.Error{
				Code: "MeasurementEvidenceDeletionIncomplete",
				Message: "evidence cleanup stopped; some owned objects may already be deleted, " +
					"the native measurement is retained, and retry with its UID/resourceVersion is required",
			}
		}
		result.DeletedEvidence = append(result.DeletedEvidence, httpapi.MeasurementEvidenceLocation{
			Store: location.Store, Key: location.Key,
		})
	}
	result.EvidenceDeleted = true
	policy := meta.DeletePropagationBackground
	deletion := &client.DeleteOptions{
		Preconditions:     &meta.Preconditions{UID: &task.UID, ResourceVersion: &task.ResourceVersion},
		PropagationPolicy: &policy,
	}
	if err := o.client.Delete(ctx, task, deletion); err != nil {
		if apierrors.IsConflict(err) {
			return result, &serving.Error{
				Code: "MeasurementDeletionIncomplete", Field: "If-Match",
				Message: "owned evidence cleanup completed, but a native UID/resourceVersion conflict " +
					"prevented measurement deletion; query its identity before retrying",
			}
		}
		return result, &serving.Error{
			Code: "MeasurementDeletionIncomplete",
			Message: "owned evidence cleanup completed but native deletion was not confirmed; " +
				"the native object may remain or have changed; query its identity before retrying",
		}
	}
	result.Request = "DeletionRequested"
	return result, nil
}
