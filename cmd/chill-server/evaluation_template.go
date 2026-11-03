package main

import (
	"context"
	"encoding/json"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	templateSourceLive        = "live"
	templateSourcePublication = "latestPublication"
	templateSourceNone        = "none"
)

func (s *evaluationService) template(ctx context.Context) (httpapi.EvaluationTemplate, error) {
	result := httpapi.EvaluationTemplate{Sources: map[string]string{
		"target": templateSourceNone, "powerMeasurement": templateSourceNone, "evidenceStore": "config",
		"harness": templateSourceNone, "loadGenerator": templateSourceNone,
		"judgment": templateSourceNone, "settingsPatch": templateSourceNone,
	}}
	result.Spec.EvidenceStore = s.store
	publications, err := s.experiments.listSummaries(ctx)
	if err != nil {
		return result, err
	}
	for _, publication := range publications.Items {
		if len(publication.Definition) == 0 {
			continue
		}
		var definition struct {
			Run  httpapi.EvaluationIdentity `json:"run"`
			Spec api.EvaluationRunSpec      `json:"spec"`
		}
		if err := json.Unmarshal(publication.Definition, &definition); err != nil {
			return result, err
		}
		result.Spec = definition.Spec
		result.Spec.EvidenceStore = s.store
		for _, key := range []string{"harness", "loadGenerator", "judgment", "settingsPatch"} {
			result.Sources[key] = templateSourcePublication
		}
		result.SourcePublication = &httpapi.EvaluationIdentity{UID: publication.ID, Name: definition.Run.Name}
		break
	}
	if len(result.Spec.Target.Components) == 0 {
		var deployments appsv1.DeploymentList
		if err := s.reader.List(ctx, &deployments, client.InNamespace(s.namespace),
			client.MatchingLabels{"app.kubernetes.io/component": "serving-entrypoint"}); err != nil {
			return result, err
		}
		for _, deployment := range deployments.Items {
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name != "serving-entrypoint" {
					continue
				}
				result.Spec.Target.Components = append(result.Spec.Target.Components, api.EvaluationTargetComponent{
					Name: "serving-entrypoint",
					Resource: api.EvaluationResourceReference{
						APIVersion: "apps/v1", Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name,
					},
				})
			}
		}
	}
	for _, component := range result.Spec.Target.Components {
		if _, err := s.liveTarget(ctx, component); err != nil {
			return result, err
		}
		result.Sources["target"] = templateSourceLive
	}
	return result, nil
}
