package main

import (
	"context"
	"fmt"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type evaluationService struct{ reader client.Reader }

func (s *evaluationService) listActive(ctx context.Context) (httpapi.ActiveEvaluationList, error) {
	result := httpapi.ActiveEvaluationList{Items: []httpapi.ActiveEvaluation{}}
	var runs api.EvaluationRunList
	if err := s.reader.List(ctx, &runs); err != nil {
		return result, err
	}
	for _, run := range runs.Items {
		if !meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionPoolAcquired) {
			continue
		}
		item := httpapi.ActiveEvaluation{
			Name: run.Name, UID: string(run.UID), StartedAt: run.Status.StartedAt, StagesTotal: len(run.Spec.Stages),
		}
		for i, stage := range run.Spec.Stages {
			if stage.ID == run.Status.CurrentStage {
				item.StageIndex = i + 1
				break
			}
		}
		for _, ref := range run.Status.ChildRefs {
			if ref.StageID != run.Status.CurrentStage {
				continue
			}
			var stage api.EvaluationStage
			if err := s.reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: ref.Name}, &stage); err != nil {
				return result, err
			}
			if stage.UID != ref.UID {
				return result, fmt.Errorf("active evaluation stage %s identity changed", ref.Name)
			}
			var load api.LoadScenario
			key := client.ObjectKey{Namespace: stage.Namespace, Name: stage.Spec.LoadScenarioRef.Name}
			if err := s.reader.Get(ctx, key, &load); err != nil {
				return result, err
			}
			if load.UID != stage.Spec.LoadScenarioRef.UID {
				return result, fmt.Errorf("active evaluation load scenario %s identity changed", load.Name)
			}
			projected := &httpapi.ActiveEvaluationStage{
				ID: stage.Spec.StageID, Mode: string(stage.Spec.Mode),
				StartedAt: stage.Status.StartedAt, GoAt: stage.Status.GoAt,
				Injections: []httpapi.ActiveEvaluationInjection{},
			}
			for _, part := range load.Spec.Parts {
				projected.PlannedSeconds += part.DurationSeconds
			}
			for _, plan := range stage.Spec.FailureInjection {
				injection := httpapi.ActiveEvaluationInjection{
					ID: plan.ID, Action: plan.Action, OffsetSeconds: plan.OffsetSeconds,
				}
				for _, record := range stage.Status.InjectionRecords {
					if record.ID != plan.ID {
						continue
					}
					injection.Outcome = record.Outcome
					if record.NodeRef != nil {
						injection.NodeName = record.NodeRef.Name
					}
					if record.PodRef != nil {
						injection.PodName = record.PodRef.Name
					}
					break
				}
				projected.Injections = append(projected.Injections, injection)
			}
			item.Stage = projected
			break
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}
