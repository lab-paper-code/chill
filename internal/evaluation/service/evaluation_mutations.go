package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
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
func (s *Service) evaluationView(
	ctx context.Context, run *api.EvaluationRun, stages map[client.ObjectKey]*api.EvaluationStage,
) (EvaluationView, error) {
	value := EvaluationView{
		Name: run.Name, UID: string(run.UID), Spec: run.Spec, Phase: evaluationPhase(run),
		Conditions: run.Status.Conditions, CurrentStage: run.Status.CurrentStage,
		StartedAt: run.Status.StartedAt, FinishedAt: run.Status.FinishedAt,
		CurrentProcedure: run.Status.CurrentProcedure, Procedures: evaluationProcedures(run.Status.Procedures),
	}
	var liveTarget []EvaluationLiveTarget
	if json.Unmarshal([]byte(run.Annotations["edge.dacs.io/evaluation-live-target"]), &liveTarget) == nil {
		value.LiveTarget = liveTarget
	}
	if meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionPublished) {
		value.PublicationID = string(run.UID)
	}
	for _, ref := range run.Status.ChildRefs {
		key := client.ObjectKey{Namespace: run.Namespace, Name: ref.Name}
		stage := stages[key]
		if stages == nil {
			stage = &api.EvaluationStage{}
			if err := s.reader.Get(ctx, key, stage); apierrors.IsNotFound(err) {
				continue
			} else if err != nil {
				return EvaluationView{}, err
			}
		}
		// A deleted or replaced child is not evidence about the referenced stage.
		if stage == nil || stage.UID != ref.UID {
			continue
		}
		value.Stages = append(value.Stages, EvaluationStage{
			ID: stage.Spec.StageID, Name: stage.Name, UID: string(stage.UID),
			Conditions: stage.Status.Conditions, MeasurementVerdict: stage.Status.MeasurementVerdict,
			CurrentProcedure: stage.Status.CurrentProcedure, Procedures: evaluationProcedures(stage.Status.Procedures),
			StartedAt: stage.Status.StartedAt, FinishedAt: stage.Status.FinishedAt, GoAt: stage.Status.GoAt,
		})
	}
	return value, nil
}
func evaluationProcedures(procedures []api.EvaluationProcedure) []EvaluationProcedure {
	if procedures == nil {
		return nil
	}
	result := make([]EvaluationProcedure, len(procedures))
	for i, procedure := range procedures {
		result[i] = EvaluationProcedure{
			Step: procedure.Step, StartedAt: procedure.StartedAt, FinishedAt: procedure.FinishedAt, Reason: procedure.Reason,
			MeasurementVerdict: procedure.MeasurementVerdict,
		}
	}
	return result
}
func (s *Service) List(ctx context.Context) ([]EvaluationView, error) {
	var runs api.EvaluationRunList
	if err := s.reader.List(ctx, &runs, client.InNamespace(s.namespace)); err != nil {
		return nil, err
	}
	var children api.EvaluationStageList
	if err := s.reader.List(ctx, &children, client.InNamespace(s.namespace)); err != nil {
		return nil, err
	}
	stages := make(map[client.ObjectKey]*api.EvaluationStage, len(children.Items))
	for i := range children.Items {
		stage := &children.Items[i]
		stages[client.ObjectKeyFromObject(stage)] = stage
	}
	result := make([]EvaluationView, 0, len(runs.Items))
	for i := range runs.Items {
		value, err := s.evaluationView(ctx, &runs.Items[i], stages)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}
func (s *Service) Get(ctx context.Context, name string) (EvaluationView, error) {
	var run api.EvaluationRun
	if err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &run); err != nil {
		return EvaluationView{}, err
	}
	return s.evaluationView(ctx, &run, nil)
}
func (s *Service) Create(
	ctx context.Context, request EvaluationRequest, actor string,
) (EvaluationIdentity, error) {
	result, err := s.Preflight(ctx, request)
	if err != nil {
		return EvaluationIdentity{}, err
	}
	for _, check := range result.Checks {
		if !check.Passed {
			return EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Field: check.ID, Message: check.Reason}
		}
	}
	liveTarget, err := json.Marshal(result.LiveTarget)
	if err != nil {
		return EvaluationIdentity{}, err
	}
	run := &api.EvaluationRun{
		ObjectMeta: metav1.ObjectMeta{Name: request.Name, Namespace: s.namespace, Annotations: map[string]string{
			"edge.dacs.io/requested-by":           actor,
			"edge.dacs.io/evaluation-live-target": string(liveTarget),
		}},
		Spec: request.Spec,
	}
	if err := s.client.Create(ctx, run); err != nil {
		return EvaluationIdentity{}, err
	}
	return EvaluationIdentity{Name: run.Name, UID: string(run.UID)}, nil
}
func (s *Service) Delete(ctx context.Context, name, uid, actor string) (EvaluationIdentity, error) {
	var run api.EvaluationRun
	if err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &run); err != nil {
		return EvaluationIdentity{}, err
	}
	if string(run.UID) != uid {
		return EvaluationIdentity{}, &serving.Error{
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
		return EvaluationIdentity{}, err
	}
	policy := metav1.DeletePropagationBackground
	options := &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &run.UID}, PropagationPolicy: &policy,
	}
	if err := s.client.Delete(ctx, &run, options); err != nil {
		return EvaluationIdentity{}, err
	}
	return EvaluationIdentity{Name: name, UID: uid}, nil
}
func (s *Service) Replace(
	ctx context.Context, name, uid string, request EvaluationRequest, actor string,
) (EvaluationIdentity, error) {
	var run api.EvaluationRun
	key := client.ObjectKey{Namespace: s.namespace, Name: name}
	if err := s.reader.Get(ctx, key, &run); err != nil {
		return EvaluationIdentity{}, err
	}
	if string(run.UID) != uid || evaluationPhase(&run) != evaluationPending {
		return EvaluationIdentity{}, &serving.Error{
			Code: "ResourceVersionConflict", Message: "only unchanged Pending evaluations may be replaced",
		}
	}
	if request.Name == "" {
		request.Name = name
	}
	if request.Name != name {
		return EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Message: "request name must match path"}
	}
	checks, err := s.Preflight(ctx, request)
	if err != nil {
		return EvaluationIdentity{}, err
	}
	for _, check := range checks.Checks {
		if !check.Passed {
			return EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Message: check.Reason}
		}
	}
	policy := metav1.DeletePropagationBackground
	options := &client.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &run.UID, ResourceVersion: &run.ResourceVersion},
		PropagationPolicy: &policy,
	}
	if err := s.client.Delete(ctx, &run, options); err != nil {
		return EvaluationIdentity{}, err
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
		return EvaluationIdentity{}, err
	}
	result, err := s.Create(ctx, request, actor)
	if err != nil {
		return result, fmt.Errorf("previous object deleted; recreation failed: %w", err)
	}
	return result, nil
}
func (s *Service) Release(
	ctx context.Context, name string, request EvaluationReleaseRequest, actor string,
) (EvaluationIdentity, error) {
	var run api.EvaluationRun
	if err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &run); err != nil {
		return EvaluationIdentity{}, err
	}
	if run.UID != types.UID(request.UID) {
		return EvaluationIdentity{}, &serving.Error{
			Code: "ResourceVersionConflict", Message: "EvaluationRun UID changed",
		}
	}
	if evaluationPhase(&run) != "Blocked" {
		return EvaluationIdentity{}, &serving.Error{
			Code: "ResourceVersionConflict", Message: "release requires a finished, blocked evaluation",
		}
	}
	if request.Reason == "" {
		return EvaluationIdentity{}, &serving.Error{Code: "InvalidInput", Message: "release reason required"}
	}
	annotation, err := json.Marshal(struct {
		Reason string    `json:"reason"`
		At     time.Time `json:"at"`
		By     string    `json:"by"`
	}{request.Reason, time.Now().UTC(), actor})
	if err != nil {
		return EvaluationIdentity{}, err
	}
	before := run.DeepCopy()
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[api.EvaluationReleaseAnnotation] = string(annotation)
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	if err := s.client.Patch(ctx, &run, patch); err != nil {
		return EvaluationIdentity{}, err
	}
	return EvaluationIdentity{Name: name, UID: request.UID}, nil
}

func evaluationExecutionFinished(run *api.EvaluationRun) bool {
	execution := meta.FindStatusCondition(run.Status.Conditions, api.EvaluationConditionExecutionComplete)
	return execution != nil && execution.Status != metav1.ConditionUnknown
}
