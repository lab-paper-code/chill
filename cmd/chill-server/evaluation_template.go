package main

import (
	"context"
	"encoding/json"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *evaluationService) template(ctx context.Context) (httpapi.EvaluationTemplate, error) {
	result := httpapi.EvaluationTemplate{Sources: map[string]string{
		"target": "none", "powerMeasurement": "none", "evidenceStore": "config",
		"harness": "none", "loadGenerator": "none", "judgment": "none", "control": "none",
	}}
	result.Spec.EvidenceStore = s.store
	publications, err := s.experiments.list(ctx)
	if err != nil {
		return result, err
	}
	for _, publication := range publications.Items {
		detail, err := s.experiments.get(ctx, publication.ID)
		if err != nil {
			return result, err
		}
		if len(detail.Definition) == 0 {
			continue
		}
		var definition struct {
			Spec api.EvaluationRunSpec `json:"spec"`
		}
		if err := json.Unmarshal(detail.Definition, &definition); err != nil {
			return result, err
		}
		result.Spec = definition.Spec
		result.Spec.EvidenceStore = s.store
		for _, key := range []string{"harness", "loadGenerator", "judgment", "control"} {
			result.Sources[key] = "publication/" + publication.ID
		}
		break
	}
	if len(result.Spec.Target.Components) == 0 {
		var deployments appsv1.DeploymentList
		if err := s.reader.List(ctx, &deployments, client.InNamespace(s.namespace),
			client.MatchingLabels{"app.kubernetes.io/component": "serving-entrypoint"}); err != nil {
			return result, err
		}
		for _, deployment := range deployments.Items {
			source := deployment.Labels["org.opencontainers.image.revision"]
			if source == "" {
				source = deployment.Annotations["org.opencontainers.image.revision"]
			}
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name != "serving-entrypoint" {
					continue
				}
				result.Spec.Target.Components = append(result.Spec.Target.Components, api.EvaluationTargetComponent{
					Name: "serving-entrypoint",
					Resource: api.EvaluationResourceReference{
						APIVersion: "apps/v1", Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name,
					},
					EvaluationImage: api.EvaluationImage{Image: container.Image, SourceSHA: source},
				})
			}
		}
	}
	for i := range result.Spec.Target.Components {
		component := &result.Spec.Target.Components[i]
		live, err := s.liveTarget(ctx, *component)
		if err != nil {
			return result, err
		}
		if len(live.LiveImageIDs) == 0 {
			continue
		}
		digest := imageDigest(live.LiveImageIDs[0])
		repository := strings.Split(component.Image, "@")[0]
		component.Image = repository + "@" + digest
		result.Sources["target"] = "live"
	}
	return result, nil
}
