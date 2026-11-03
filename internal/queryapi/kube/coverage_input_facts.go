package kube

import (
	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
)

func coverageInputFacts(
	datasets []api.Dataset,
	preprocessors []api.Preprocessor,
) ([]queryapi.CoverageDataset, []queryapi.CoveragePreprocessor) {
	data := make([]queryapi.CoverageDataset, len(datasets))
	for i, dataset := range datasets {
		data[i] = queryapi.CoverageDataset{Name: dataset.Name, UID: string(dataset.UID), Fields: dataset.Spec.Fields}
	}
	pre := make([]queryapi.CoveragePreprocessor, len(preprocessors))
	for i, value := range preprocessors {
		pre[i] = queryapi.CoveragePreprocessor{Name: value.Name, Fields: value.Spec.Input.Fields}
		for _, artifact := range value.Spec.Artifacts {
			pre[i].Artifacts = append(pre[i].Artifacts, queryapi.CoveragePreprocessorArtifact{
				Model: artifact.Target.Model, Artifact: artifact.Target.Artifact,
				Image: artifact.Source.Image, Path: artifact.Source.Path,
			})
		}
	}
	return data, pre
}
