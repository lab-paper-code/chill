package measurement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	measurementRequestAccepted          = "Accepted"
	measurementRequestNotSubmitted      = "NOT_SUBMITTED"
	measurementRequestDeletionRequested = "DeletionRequested"
)

// The measurement service removes only source-proven, unreferenced measurement
// evidence before requesting native deletion. Native GC/finalizers stay native.
type QueryBearerContextKey struct{}

type Service struct {
	client    client.Client
	scheduler *SchedulerInputs
	kube      kubernetes.Interface
	objects   Objects
	opts      Config
	query     *url.URL
	http      *http.Client
}

type Config struct {
	ServingNamespace       string
	ProfileNamespaces      []string
	SchedulerConfigMap     string
	SchedulerConfigKey     string
	MaxRequestBytes        int64
	MaxEvidenceBytes       int64
	StoreName              string
	LoadGeneratorNamespace string
}

// SchedulerInputs shares the scheduler's mutation authority and accepted snapshot.
// DecodePolicy must use the scheduler's full configuration decoder.
type SchedulerInputs struct {
	Lock                  *sync.Mutex
	LoadProfiles          func() (string, []serving.Profile, map[string]servingkube.PrepareSpec, bool)
	DecodePolicy          func(string, int64) (*DispatchOccupancyPolicy, error)
	GatewayRequestTimeout time.Duration
}

type Objects interface {
	evidencestore.LocationReader
	Delete(context.Context, evidencestore.ObjectLocation) error
}

func New(native client.Client, kube kubernetes.Interface, scheduler *SchedulerInputs,
	objects Objects, opts Config, origin *url.URL, transport *http.Client,
) *Service {
	return &Service{
		client: native, kube: kube, scheduler: scheduler, objects: objects, opts: opts,
		query: origin, http: transport,
	}
}

func (o *Service) Namespace(namespace string) error {
	if len(validation.IsDNS1123Label(namespace)) != 0 {
		return &serving.Error{Code: "InvalidInput", Message: "valid measurement namespace is required"}
	}
	allowed := namespace == o.opts.ServingNamespace
	if len(o.opts.ProfileNamespaces) != 0 {
		allowed = slices.Contains(o.opts.ProfileNamespaces, namespace)
	}
	if !allowed {
		return servingkube.ErrForbidden
	}
	return nil
}

func (o *Service) preflight(ctx context.Context, task *api.BatchEnergyOptimization) error {
	unavailable := &serving.Error{
		Code: "MeasurementPreflightUnavailable", Message: "production QueryAPI preflight is unavailable",
	}
	if o.query == nil {
		return unavailable
	}
	body, err := json.Marshal(task)
	if err != nil {
		return err
	}
	target := *o.query
	target.Path = "/api/v1/namespaces/" + task.Namespace + "/batch-energy-optimizations/preflight"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization, ok := ctx.Value(QueryBearerContextKey{}).(string); ok {
		req.Header.Set("Authorization", authorization)
	}
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
	var result queryapi.BatchEnergyOptimizationPreflight
	if json.Unmarshal(payload, &result) != nil ||
		result.APIVersion != queryapi.BatchEnergyOptimizationPreflightAPIVersion ||
		result.Kind != "BatchEnergyOptimizationPreflight" ||
		result.Campaign.Namespace != task.Namespace || result.Campaign.Name != task.Name {
		return unavailable
	}
	if !result.Resolves {
		return &serving.Error{Code: "MeasurementPreflightRejected", Message: result.Reason}
	}
	return nil
}

func (o *Service) Submit(
	ctx context.Context, task api.BatchEnergyOptimization, dryRun bool,
) (MeasurementMutationResult, error) {
	result := MeasurementMutationResult{}
	if err := o.Namespace(task.Namespace); err != nil {
		return result, err
	}
	if err := o.preflight(ctx, &task); err != nil {
		return result, err
	}
	options := &client.CreateOptions{}
	result.Request = measurementRequestAccepted
	if dryRun {
		options.DryRun = []string{meta.DryRunAll}
		result.Request = measurementRequestNotSubmitted
	}
	if err := o.client.Create(ctx, &task, options); err != nil {
		return MeasurementMutationResult{}, err
	}
	result.Task = &task
	return result, nil
}

func (o *Service) Delete(
	ctx context.Context, namespace, name, uid, version string,
) (MeasurementMutationResult, error) {
	result := MeasurementMutationResult{}
	if err := o.Namespace(namespace); err != nil {
		return result, err
	}
	if uid == "" || version == "" {
		return result, &serving.Error{
			Code:    "ResourceVersionRequired",
			Message: "native UID and resourceVersion are required",
		}
	}
	if o.scheduler != nil {
		o.scheduler.Lock.Lock()
		defer o.scheduler.Lock.Unlock()
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
	if task.Status.Phase == string(api.MeasurementPhaseMeasuring) {
		return result, &serving.Error{Code: "MeasurementInUse", Message: "an active measurement cannot be deleted"}
	}
	pods, err := o.kube.CoreV1().Pods(o.opts.ServingNamespace).List(ctx, meta.ListOptions{})
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
	result.DeletedEvidence = make([]MeasurementEvidenceLocation, 0, len(locations))
	for _, location := range locations {
		if err := o.objects.Delete(ctx, location); err != nil {
			return result, &serving.Error{
				Code: "MeasurementEvidenceDeletionIncomplete",
				Message: "evidence cleanup stopped; some owned objects may already be deleted, " +
					"the native measurement is retained, and retry with its UID/resourceVersion is required",
			}
		}
		result.DeletedEvidence = append(result.DeletedEvidence, MeasurementEvidenceLocation{
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
	result.Request = measurementRequestDeletionRequested
	return result, nil
}
