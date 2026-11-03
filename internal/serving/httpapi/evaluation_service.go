package httpapi

import (
	"context"

	evaluation "github.com/lab-paper-code/chill/internal/evaluation/service"
)

// ConfigureEvaluations binds application operations to the HTTP DTO boundary.
func ConfigureEvaluations(config *HandlerConfig, s *evaluation.Service) {
	config.ListActiveEvaluations = func(ctx context.Context) (ActiveEvaluationList, error) {
		value, err := s.ListActive(ctx)
		result := ActiveEvaluationList{Items: make([]ActiveEvaluation, len(value))}
		for i, item := range value {
			projected := ActiveEvaluation{Name: item.Name, UID: item.UID, StartedAt: item.StartedAt, StageIndex: item.StageIndex, StagesTotal: item.StagesTotal}
			if item.Stage != nil {
				stage := item.Stage
				projected.Stage = &ActiveEvaluationStage{ID: stage.ID, StartedAt: stage.StartedAt, GoAt: stage.GoAt, PlannedSeconds: stage.PlannedSeconds, Injections: make([]ActiveEvaluationInjection, len(stage.Injections))}
				for j, injection := range stage.Injections {
					projected.Stage.Injections[j] = ActiveEvaluationInjection(injection)
				}
			}
			result.Items[i] = projected
		}
		return result, err
	}
	config.ListEvaluations = func(ctx context.Context) (EvaluationList, error) {
		value, err := s.List(ctx)
		result := EvaluationList{Items: make([]EvaluationView, len(value))}
		for i, item := range value {
			result.Items[i] = evaluationViewDTO(item)
		}
		return result, err
	}
	config.GetEvaluation = func(ctx context.Context, name string) (EvaluationView, error) {
		value, err := s.Get(ctx, name)
		return evaluationViewDTO(value), err
	}
	config.PreflightEvaluation = func(ctx context.Context, request EvaluationRequest) (EvaluationPreflight, error) {
		value, err := s.Preflight(ctx, evaluation.EvaluationRequest(request))
		return EvaluationPreflight{Checks: evaluationChecksDTO(value.Checks), LiveTarget: evaluationTargetsDTO(value.LiveTarget)}, err
	}
	config.CreateEvaluation = func(ctx context.Context, request EvaluationRequest, actor string) (EvaluationIdentity, error) {
		value, err := s.Create(ctx, evaluation.EvaluationRequest(request), actor)
		return EvaluationIdentity(value), err
	}
	config.ReplaceEvaluation = func(ctx context.Context, name, uid string, request EvaluationRequest, actor string) (EvaluationIdentity, error) {
		value, err := s.Replace(ctx, name, uid, evaluation.EvaluationRequest(request), actor)
		return EvaluationIdentity(value), err
	}
	config.DeleteEvaluation = func(ctx context.Context, name, uid, actor string) (EvaluationIdentity, error) {
		value, err := s.Delete(ctx, name, uid, actor)
		return EvaluationIdentity(value), err
	}
	config.ReleaseEvaluation = func(ctx context.Context, name string, request EvaluationReleaseRequest, actor string) (EvaluationIdentity, error) {
		value, err := s.Release(ctx, name, evaluation.EvaluationReleaseRequest(request), actor)
		return EvaluationIdentity(value), err
	}
}

func evaluationChecksDTO(values []evaluation.EvaluationCheck) []EvaluationCheck {
	if values == nil {
		return nil
	}
	result := make([]EvaluationCheck, len(values))
	for i, value := range values {
		result[i] = EvaluationCheck(value)
	}
	return result
}
func evaluationTargetsDTO(values []evaluation.EvaluationLiveTarget) []EvaluationLiveTarget {
	if values == nil {
		return nil
	}
	result := make([]EvaluationLiveTarget, len(values))
	for i, value := range values {
		result[i] = EvaluationLiveTarget(value)
	}
	return result
}
func evaluationViewDTO(value evaluation.EvaluationView) EvaluationView {
	return EvaluationView{Name: value.Name, UID: value.UID, Spec: value.Spec, Conditions: value.Conditions, CurrentStage: value.CurrentStage, StartedAt: value.StartedAt, FinishedAt: value.FinishedAt, Phase: value.Phase, PublicationID: value.PublicationID, LiveTarget: evaluationTargetsDTO(value.LiveTarget)}
}

// ConfigureLoadScenarios keeps HTTP request and response shapes outside the application.
func ConfigureLoadScenarios(config *HandlerConfig, s *evaluation.LoadService) {
	config.ListLoadScenarios = func(ctx context.Context) (LoadScenarioList, error) {
		value, err := s.List(ctx)
		result := LoadScenarioList{Items: make([]LoadScenarioView, len(value))}
		for i, item := range value {
			result.Items[i] = LoadScenarioView(item)
		}
		return result, err
	}
	config.GetLoadScenario = func(ctx context.Context, name string) (LoadScenarioView, error) {
		value, err := s.Get(ctx, name)
		return LoadScenarioView(value), err
	}
	config.PreflightLoadScenario = func(ctx context.Context, request LoadScenarioRequest) (LoadScenarioPreflight, error) {
		value, err := s.Preflight(ctx, evaluation.LoadScenarioRequest(request))
		var workloads map[string]LiveWorkload
		if value.LiveWorkloads != nil {
			workloads = make(map[string]LiveWorkload, len(value.LiveWorkloads))
			for name, workload := range value.LiveWorkloads {
				workloads[name] = LiveWorkload(workload)
			}
		}
		return LoadScenarioPreflight{Checks: evaluationChecksDTO(value.Checks), LiveWorkloads: workloads}, err
	}
	config.CreateLoadScenario = func(ctx context.Context, request LoadScenarioRequest, actor string) (EvaluationIdentity, error) {
		value, err := s.Create(ctx, evaluation.LoadScenarioRequest(request), actor)
		return EvaluationIdentity(value), err
	}
	config.ReplaceLoadScenario = func(ctx context.Context, name, uid string, request LoadScenarioRequest, actor string) (EvaluationIdentity, error) {
		value, err := s.Replace(ctx, name, uid, evaluation.LoadScenarioRequest(request), actor)
		return EvaluationIdentity(value), err
	}
	config.DeleteLoadScenario = func(ctx context.Context, name, uid string) (EvaluationIdentity, error) {
		value, err := s.Delete(ctx, name, uid)
		return EvaluationIdentity(value), err
	}
	config.ListEvaluationInputs = func(ctx context.Context) (EvaluationInputList, error) {
		value, err := s.Inputs(ctx)
		result := EvaluationInputList{Items: make([]EvaluationInput, len(value))}
		for i, item := range value {
			result.Items[i] = EvaluationInput(item)
		}
		return result, err
	}
}

func EvaluationTemplateDTO(value evaluation.EvaluationTemplate) EvaluationTemplate {
	result := EvaluationTemplate{Spec: value.Spec, Sources: value.Sources}
	if value.SourcePublication != nil {
		identity := EvaluationIdentity(*value.SourcePublication)
		result.SourcePublication = &identity
	}
	return result
}
