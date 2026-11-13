package measurement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (o *Service) SubmitOccupancy(
	ctx context.Context,
	task api.DispatchOccupancyMeasurement,
	dryRun bool,
) (OccupancyMutationResult, error) {
	if err := o.Namespace(task.Namespace); err != nil {
		return OccupancyMutationResult{}, err
	}
	result := OccupancyMutationResult{Task: &task, Request: measurementRequestAccepted}
	if dryRun {
		// The final validated frozen object is returned without even a native create.
		result.Request = measurementRequestNotSubmitted
		return result, nil
	}
	if err := o.client.Create(ctx, &task); err != nil {
		return OccupancyMutationResult{}, err
	}
	return result, nil
}

func (o *Service) DeleteOccupancy(
	ctx context.Context,
	namespace, name, uid, version string,
) (OccupancyMutationResult, error) {
	result := OccupancyMutationResult{}
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
	task := &api.DispatchOccupancyMeasurement{}
	if err := o.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, task); err != nil {
		return result, err
	}
	result.Task = task
	if string(task.UID) != uid || task.ResourceVersion != version {
		return result, apierrors.NewConflict(
			schema.GroupResource{Group: api.GroupVersion.Group, Resource: "dispatchoccupancymeasurements"},
			name,
			errors.New("native UID or resourceVersion changed"),
		)
	}
	if task.Status.Phase == api.MeasurementPhaseMeasuring {
		return result, measurementInUse("a Measuring occupancy measurement cannot be deleted")
	}
	if err := o.occupancyLoadStopped(ctx, task); err != nil {
		return result, err
	}
	location, err := o.occupancyOwnedEvidence(ctx, task)
	if err != nil {
		return result, err
	}
	// The store treats a missing key as deleted, so a retry completes.
	if err := o.objects.Delete(ctx, location); err != nil {
		return result, &serving.Error{
			Code:    "MeasurementEvidenceDeletionIncomplete",
			Message: "owned occupancy evidence cleanup failed; native measurement retained",
		}
	}
	result.DeletedEvidence = []MeasurementEvidenceLocation{{Store: location.Store, Key: location.Key}}
	result.EvidenceDeleted = true
	policy := meta.DeletePropagationBackground
	deletion := &client.DeleteOptions{
		Preconditions:     &meta.Preconditions{UID: &task.UID, ResourceVersion: &task.ResourceVersion},
		PropagationPolicy: &policy,
	}
	if err := o.client.Delete(ctx, task, deletion); err != nil {
		return result, &serving.Error{
			Code: "MeasurementDeletionIncomplete",
			Message: "owned occupancy evidence cleaned but native deletion was not confirmed; " +
				"query UID/resourceVersion before retrying",
		}
	}
	result.Request = measurementRequestDeletionRequested
	return result, nil
}

// A load Job is named by the producer's hash of this DOM UID, not by Node alone.
func (o *Service) occupancyLoadStopped(ctx context.Context, task *api.DispatchOccupancyMeasurement) error {
	sum := sha256.Sum256([]byte(task.UID))
	name := "occupancy-" + hex.EncodeToString(sum[:10])
	namespace := o.opts.LoadGeneratorNamespace
	if namespace == "" {
		namespace = o.opts.ServingNamespace
	}
	job, err := o.kube.BatchV1().Jobs(namespace).Get(ctx, name, meta.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	jobUID := ""
	if err == nil {
		if !measurementJobTerminal(job) {
			return measurementInUse("the occupancy load Job is not terminal")
		}
		jobUID = string(job.UID)
	}
	pods, err := o.kube.CoreV1().Pods(namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		owner := meta.GetControllerOf(&pod)
		if owner != nil && owner.Kind == "Job" && owner.Name == name && (jobUID == "" || string(owner.UID) == jobUID) &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			return measurementInUse("an occupancy load Pod remains active")
		}
	}
	return nil
}

// occupancyOwnedEvidence derives the producer's key from the frozen Node UID
// and this measurement's UID. The stored record is not read, so a record an
// older writer produced stays deletable.
func (o *Service) occupancyOwnedEvidence(
	ctx context.Context,
	task *api.DispatchOccupancyMeasurement,
) (evidencestore.ObjectLocation, error) {
	if o.objects == nil || o.opts.StoreName == "" {
		return evidencestore.ObjectLocation{}, &serving.Error{
			Code:    "MeasurementEvidenceUnavailable",
			Message: "occupancy Evidence Store access is required for deletion",
		}
	}
	var frozen struct {
		Node serving.NodeReference `json:"node"`
	}
	if task.Spec.Conditions.InputsJSON != "" {
		if err := json.Unmarshal([]byte(task.Spec.Conditions.InputsJSON), &frozen); err != nil {
			return evidencestore.ObjectLocation{}, measurementInUse("unreadable frozen occupancy Node identity")
		}
	}
	if frozen.Node.UID == "" {
		node, err := o.kube.CoreV1().Nodes().Get(ctx, task.Spec.Node, meta.GetOptions{})
		if err != nil {
			return evidencestore.ObjectLocation{}, err
		}
		frozen.Node = serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	}
	if frozen.Node.Name != task.Spec.Node || frozen.Node.UID == "" || strings.ContainsAny(frozen.Node.UID, "/\\") {
		return evidencestore.ObjectLocation{}, measurementInUse("invalid frozen occupancy Node identity")
	}
	prefix := servingkube.DispatchOccupancyPrefix
	if task.Spec.MinimumPairs > 0 {
		prefix = servingkube.BurstOccupancyPrefix
	}
	return o.measurementLocation(prefix + frozen.Node.UID + "/" + string(task.UID) + ".json"), nil
}
