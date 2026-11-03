package main

import (
	"context"

	"github.com/lab-paper-code/chill/internal/serving/application"

	evaluation "github.com/lab-paper-code/chill/internal/evaluation/service"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

func evaluationSettings(scheduler *application.SchedulerService) func() (serving.SchedulerSettings, error) {
	return func() (serving.SchedulerSettings, error) {
		if scheduler != nil {
			scheduler.Lock.Lock()
			defer scheduler.Lock.Unlock()
			if scheduler.Controller != nil {
				return scheduler.Controller.Settings(), nil
			}
		}
		return serving.SchedulerSettings{}, &serving.Error{
			Code: "CoverageWarming", Message: "public scheduler settings are not initialized",
		}
	}
}

func configureEvaluationTemplate(
	config *httpapi.HandlerConfig, evaluations *evaluation.Service, store string, experiments *experimentService,
) {
	config.GetEvaluationTemplate = func(ctx context.Context) (httpapi.EvaluationTemplate, error) {
		publications, err := experiments.listSummaries(ctx)
		if err != nil {
			return httpapi.EvaluationTemplate{}, err
		}
		definitions := make([]evaluation.PublicationDefinition, len(publications.Items))
		for i, publication := range publications.Items {
			definitions[i] = evaluation.PublicationDefinition{ID: publication.ID, Definition: publication.Definition}
		}
		value, err := evaluations.Template(ctx, store, definitions)
		return httpapi.EvaluationTemplateDTO(value), err
	}
}
