package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const evaluationPending = "Pending"

func evaluationPhase(run *api.EvaluationRun) string {
	if meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionCleanupComplete) ||
		len(run.Status.ReleaseRecords) > 0 {
		return "Done"
	}
	if meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionPoolAcquired) &&
		meta.IsStatusConditionFalse(run.Status.Conditions, api.EvaluationConditionCleanupComplete) &&
		evaluationExecutionFinished(run) {
		return "Blocked"
	}
	if run.Status.StartedAt == nil ||
		!meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionPoolAcquired) {
		return evaluationPending
	}
	execution := meta.FindStatusCondition(run.Status.Conditions, api.EvaluationConditionExecutionComplete)
	if execution == nil || execution.Status == metav1.ConditionUnknown {
		return "Running"
	}
	return "Finishing"
}
func evaluationView(run *api.EvaluationRun) httpapi.EvaluationView {
	value := httpapi.EvaluationView{
		Name: run.Name, UID: string(run.UID), Spec: run.Spec, Phase: evaluationPhase(run),
		Conditions: run.Status.Conditions, CurrentStage: run.Status.CurrentStage,
		StartedAt: run.Status.StartedAt, FinishedAt: run.Status.FinishedAt,
	}
	if meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionPublished) {
		value.PublicationID = string(run.UID)
	}
	return value
}
func (s *evaluationService) list(ctx context.Context) (httpapi.EvaluationList, error) {
	result := httpapi.EvaluationList{Items: []httpapi.EvaluationView{}}
	var runs api.EvaluationRunList
	if err := s.reader.List(ctx, &runs, client.InNamespace(s.namespace)); err != nil {
		return result, err
	}
	for i := range runs.Items {
		result.Items = append(result.Items, evaluationView(&runs.Items[i]))
	}
	return result, nil
}
func (s *evaluationService) get(ctx context.Context, name string) (httpapi.EvaluationView, error) {
	var run api.EvaluationRun
	if err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &run); err != nil {
		return httpapi.EvaluationView{}, err
	}
	return evaluationView(&run), nil
}
func (s *evaluationService) create(
	ctx context.Context, request httpapi.EvaluationRequest, actor string,
) (httpapi.EvaluationIdentity, error) {
	result, err := s.preflight(ctx, request)
	if err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	for _, check := range result.Checks {
		if !check.Passed {
			return httpapi.EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Field: check.ID, Message: check.Reason}
		}
	}
	run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: request.Name, Namespace: s.namespace,
		Annotations: map[string]string{"edge.dacs.io/requested-by": actor}}, Spec: request.Spec}
	if err := s.client.Create(ctx, run); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	return httpapi.EvaluationIdentity{Name: run.Name, UID: string(run.UID)}, nil
}
func (s *evaluationService) delete(ctx context.Context, name, uid, actor string) (httpapi.EvaluationIdentity, error) {
	var run api.EvaluationRun
	if err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &run); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	if string(run.UID) != uid {
		return httpapi.EvaluationIdentity{}, &serving.Error{
			Code: "ResourceVersionConflict", Message: "EvaluationRun UID changed",
		}
	}
	before := run.DeepCopy()
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations["edge.dacs.io/stop-requested-by"] = actor
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	if err := s.client.Patch(ctx, &run, patch); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	policy := metav1.DeletePropagationBackground
	options := &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &run.UID}, PropagationPolicy: &policy,
	}
	if err := s.client.Delete(ctx, &run, options); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	return httpapi.EvaluationIdentity{Name: name, UID: uid}, nil
}
func (s *evaluationService) replace(
	ctx context.Context, name, uid string, request httpapi.EvaluationRequest, actor string,
) (httpapi.EvaluationIdentity, error) {
	var run api.EvaluationRun
	key := client.ObjectKey{Namespace: s.namespace, Name: name}
	if err := s.reader.Get(ctx, key, &run); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	if string(run.UID) != uid || evaluationPhase(&run) != evaluationPending {
		return httpapi.EvaluationIdentity{}, &serving.Error{
			Code: "ResourceVersionConflict", Message: "only unchanged Pending evaluations may be replaced",
		}
	}
	if request.Name == "" {
		request.Name = name
	}
	if request.Name != name {
		return httpapi.EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Message: "request name must match path"}
	}
	checks, err := s.preflight(ctx, request)
	if err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	for _, check := range checks.Checks {
		if !check.Passed {
			return httpapi.EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Message: check.Reason}
		}
	}
	policy := metav1.DeletePropagationBackground
	options := &client.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &run.UID, ResourceVersion: &run.ResourceVersion},
		PropagationPolicy: &policy,
	}
	if err := s.client.Delete(ctx, &run, options); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		var current api.EvaluationRun
		err := s.reader.Get(ctx, key, &current)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	result, err := s.create(ctx, request, actor)
	if err != nil {
		return result, fmt.Errorf("previous object deleted; recreation failed: %w", err)
	}
	return result, nil
}
func (s *evaluationService) release(
	ctx context.Context, name string, request httpapi.EvaluationReleaseRequest, actor string,
) (httpapi.EvaluationIdentity, error) {
	var run api.EvaluationRun
	if err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &run); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	if run.UID != types.UID(request.UID) {
		return httpapi.EvaluationIdentity{}, &serving.Error{
			Code: "ResourceVersionConflict", Message: "EvaluationRun UID changed",
		}
	}
	if evaluationPhase(&run) != "Blocked" {
		return httpapi.EvaluationIdentity{}, &serving.Error{
			Code: "ResourceVersionConflict", Message: "release requires a finished, blocked evaluation",
		}
	}
	if request.Reason == "" {
		return httpapi.EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Message: "release reason required"}
	}
	annotation, err := json.Marshal(struct {
		Reason string    `json:"reason"`
		At     time.Time `json:"at"`
		By     string    `json:"by"`
	}{request.Reason, time.Now().UTC(), actor})
	if err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	before := run.DeepCopy()
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[api.EvaluationReleaseAnnotation] = string(annotation)
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	if err := s.client.Patch(ctx, &run, patch); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	return httpapi.EvaluationIdentity{Name: name, UID: request.UID}, nil
}

func evaluationExecutionFinished(run *api.EvaluationRun) bool {
	execution := meta.FindStatusCondition(run.Status.Conditions, api.EvaluationConditionExecutionComplete)
	return execution != nil && execution.Status != metav1.ConditionUnknown
}
