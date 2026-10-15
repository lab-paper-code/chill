package modelcatalog

import (
	"errors"
	"fmt"
	"path"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// ValidatePreprocessor checks the raw contracts and one artifact per target model artifact.
func ValidatePreprocessor(p *edgev1alpha1.Preprocessor) error {
	if p == nil {
		return errors.New("preprocessor is required")
	}
	if err := validateRawFields(p.Spec.Input.Fields); err != nil {
		return fmt.Errorf("input: %w", err)
	}
	if len(p.Spec.Output.Fields) == 0 {
		return errors.New("output: at least one field is required")
	}
	outputs := make(map[string]struct{}, len(p.Spec.Output.Fields))
	for _, field := range p.Spec.Output.Fields {
		if err := validateRawField(field.Name, field.Format, outputs); err != nil {
			return fmt.Errorf("output: %w", err)
		}
	}
	if p.Spec.MaxNewTokens != nil && *p.Spec.MaxNewTokens < 1 {
		return errors.New("maxNewTokens must be at least 1")
	}
	if len(p.Spec.Artifacts) == 0 {
		return errors.New("at least one artifact is required")
	}
	targets := make(map[edgev1alpha1.PreprocessorTarget]struct{}, len(p.Spec.Artifacts))
	for _, artifact := range p.Spec.Artifacts {
		target := artifact.Target
		if target.Model == "" || target.Artifact == "" {
			return errors.New("artifact target requires model and artifact")
		}
		if _, duplicate := targets[target]; duplicate {
			return fmt.Errorf("duplicate artifact target %s/%s", target.Model, target.Artifact)
		}
		targets[target] = struct{}{}
		source := artifact.Source
		if err := validateCarrier(source.Path, source.Image, source.Architecture); err != nil {
			return fmt.Errorf("artifact %s/%s source: %w", target.Model, target.Artifact, err)
		}
	}
	return nil
}

// ValidateDataset checks the record carrier and record field declarations.
func ValidateDataset(d *edgev1alpha1.Dataset) error {
	if d == nil {
		return errors.New("dataset is required")
	}
	if err := validateCarrier(d.Spec.Source.Path, d.Spec.Source.Image); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if err := validateRawFields(d.Spec.Fields); err != nil {
		return fmt.Errorf("fields: %w", err)
	}
	return nil
}

// Compatible returns the input contract fields the dataset records do not satisfy.
// A field matches when a record field has the same name and format and a bound
// no larger than the contract bound. An empty result means compatible.
func Compatible(d *edgev1alpha1.Dataset, p *edgev1alpha1.Preprocessor) []string {
	records := make(map[string]edgev1alpha1.RawField, len(d.Spec.Fields))
	for _, field := range d.Spec.Fields {
		records[field.Name] = field
	}
	var mismatched []string
	for _, field := range p.Spec.Input.Fields {
		record, ok := records[field.Name]
		if !ok || record.Format != field.Format || record.MaxBytes > field.MaxBytes {
			mismatched = append(mismatched, field.Name)
		}
	}
	return mismatched
}

// PreprocessorArtifact selects the preprocessing artifact for one target model artifact.
func PreprocessorArtifact(p *edgev1alpha1.Preprocessor, model, artifact string) (edgev1alpha1.PreprocessorArtifact, bool) {
	target := edgev1alpha1.PreprocessorTarget{Model: model, Artifact: artifact}
	for _, candidate := range p.Spec.Artifacts {
		if candidate.Target == target {
			return candidate, true
		}
	}
	return edgev1alpha1.PreprocessorArtifact{}, false
}

func validateRawFields(fields []edgev1alpha1.RawField) error {
	if len(fields) == 0 {
		return errors.New("at least one field is required")
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if err := validateRawField(field.Name, field.Format, seen); err != nil {
			return err
		}
		if field.MaxBytes < 1 {
			return fmt.Errorf("field %q maxBytes must be at least 1", field.Name)
		}
	}
	return nil
}

// validateRawField requires a name that is one safe path element: dataset
// records store each field as the file <recordID>/<name>.
func validateRawField(name, format string, seen map[string]struct{}) error {
	if name == "" || len(name) > 256 || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("field name %q must be 1 to 256 bytes without / or \\ and not . or ..", name)
	}
	if _, duplicate := seen[name]; duplicate {
		return fmt.Errorf("duplicate field %q", name)
	}
	seen[name] = struct{}{}
	if format != "JPEG" && format != "UTF8" {
		return fmt.Errorf("field %q format must be JPEG or UTF8", name)
	}
	return nil
}

// validateCarrier checks a carrier directory path and the carrier identity values.
func validateCarrier(dir string, identity ...string) error {
	for _, value := range append(identity, dir) {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return errors.New("source values are required without surrounding whitespace")
		}
	}
	if !artifactPath.MatchString(dir) || path.Clean(dir) != dir {
		return errors.New("path must be a clean absolute container path")
	}
	return nil
}
