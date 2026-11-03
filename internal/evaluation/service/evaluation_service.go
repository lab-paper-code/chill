package service

import (
	"context"
	"fmt"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Service struct {
	reader    client.Reader
	client    client.Client
	namespace string
	settings  func() (serving.SchedulerSettings, error)
}

func (s *Service) ListActive(ctx context.Context) (ActiveEvaluationList, error) {
	result := ActiveEvaluationList{Items: []ActiveEvaluation{}}
	var runs api.EvaluationRunList
	if err := s.reader.List(ctx, &runs); err != nil {
		return result, err
	}
	for _, run := range runs.Items {
		if !meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionPoolAcquired) {
			continue
		}
		item := ActiveEvaluation{
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
			projected := &ActiveEvaluationStage{
				ID:        stage.Spec.StageID,
				StartedAt: stage.Status.StartedAt, GoAt: stage.Status.GoAt,
				Injections: []ActiveEvaluationInjection{},
			}
			for _, part := range load.Spec.Parts {
				projected.PlannedSeconds += part.DurationSeconds
			}
			for _, plan := range stage.Spec.FailureInjection {
				injection := ActiveEvaluationInjection{
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

func New(reader client.Reader, native client.Client, namespace string, settings func() (serving.SchedulerSettings, error)) *Service {
	return &Service{reader: reader, client: native, namespace: namespace, settings: settings}
}
