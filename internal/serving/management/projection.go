package management

import (
	"fmt"
	"slices"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/util/validation"
)

func managementError(code, message, field string) error {
	return &serving.Error{Code: code, Message: message, Field: field}
}

// ValidateModel checks a model declaration before lifecycle effects.
func ValidateModel(model *api.ModelSpec) error {
	if messages := validation.IsDNS1123Subdomain(model.Name); len(messages) != 0 {
		return managementError("InvalidModel", fmt.Sprint(messages), "metadata.name")
	}
	if len(model.Spec.Artifacts) == 0 || len(model.Spec.ExecutionPaths) == 0 {
		return managementError("InvalidModel", "artifacts and executionPaths must not be empty", "spec")
	}
	for _, artifact := range model.Spec.Artifacts {
		if artifact.Name == "" || artifact.Format == "" {
			return managementError("InvalidModel", "artifact name and format are required", "spec.artifacts")
		}
	}
	for _, path := range model.Spec.ExecutionPaths {
		if path.Name == "" || path.Runtime.Family == "" || path.Runtime.Backend == "" {
			return managementError("InvalidModel", "execution path name, runtime family and backend are required", "spec.executionPaths")
		}
	}
	if err := modelcatalog.Validate(model); err != nil {
		return managementError("InvalidModel", err.Error(), "spec")
	}
	return nil
}

// Snapshot keeps only the execution paths whose artifact the preprocessor
// provides code for (WM-08).
func Snapshot(w *api.Workload, model *api.ModelSpec, p *api.Preprocessor, dataset *api.Dataset) (*serving.Definition, error) {
	if err := ValidateModel(model); err != nil {
		return nil, err
	}
	if w.UID == "" || model.UID == "" || p.UID == "" {
		return nil, managementError("IdentityUnavailable", "native registration UID is unavailable", "metadata.uid")
	}
	d := &serving.Definition{Name: w.Name, UID: string(w.UID), Generation: w.Generation,
		Model:        serving.Model{Name: model.Name, UID: string(model.UID), Generation: model.Generation},
		Preprocessor: serving.Preprocessor{Name: p.Name, UID: string(p.UID), Generation: p.Generation},
		SLO:          serving.SLO{Percentile: w.Spec.SLO.Percentile, TargetLatency: w.Spec.SLO.TargetLatency.Duration},
		Admission: serving.Admission{Policy: serving.AdmissionPolicy(w.Spec.Admission.Policy),
			MaxQueuedRequests: w.Spec.Admission.MaxQueuedRequests}}
	if w.Spec.RequestLifetime != nil {
		lifetime := w.Spec.RequestLifetime.Duration
		d.RequestLifetime = &lifetime
	}
	for _, field := range p.Spec.Input.Fields {
		d.Preprocessor.Input = append(d.Preprocessor.Input, serving.RawField{Name: field.Name, Format: field.Format, MaxBytes: field.MaxBytes})
	}
	for _, field := range p.Spec.Output.Fields {
		d.Preprocessor.Output = append(d.Preprocessor.Output, serving.RawField{Name: field.Name, Format: field.Format})
	}
	if p.Spec.MaxNewTokens != nil {
		d.Preprocessor.MaxNewTokens = *p.Spec.MaxNewTokens
	}
	if dataset != nil {
		d.Dataset = &serving.DatasetReference{Name: dataset.Name, UID: string(dataset.UID)}
	}
	for _, path := range model.Spec.ExecutionPaths {
		for _, artifact := range model.Spec.Artifacts {
			if artifact.Name != path.Artifact {
				continue
			}
			code, ok := modelcatalog.PreprocessorArtifact(p, model.Name, artifact.Name)
			if ok {
				d.Model.Paths = append(d.Model.Paths, serving.ModelPath{Name: path.Name, Artifact: artifact.Name, Format: artifact.Format,
					Image: artifact.Source.Image, Architecture: artifact.Source.Architecture, Files: slices.Clone(artifact.Source.Files),
					RuntimeFamily: path.Runtime.Family, Backend: path.Runtime.Backend, Firstfloor: modelcatalog.FirstfloorSettings(path.Runtime),
					Preprocessor: serving.PreprocessorArtifact{Image: code.Source.Image, Architecture: code.Source.Architecture, Path: code.Source.Path}})
			}
			break
		}
	}
	return d, nil
}
