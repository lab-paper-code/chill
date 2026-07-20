package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	previousCandidateReportSchemaVersion = "chill.dacs.io/modelcompat-report.v1alpha1"
	CandidateReportSchemaVersion         = "chill.dacs.io/modelcompat-report.v1alpha2"
	LegacyCandidateReportSchemaVersion   = "spikes.chill.dacs.io/modelcompat-report.v1alpha1"
	candidateReportScope                 = "StaticCompatibility"
)

// UnmarshalJSON keeps retained internal reports readable while new reports
// expose only the current observer component name.
func (r *CandidateReport) UnmarshalJSON(payload []byte) error {
	var wire struct {
		SchemaVersion       string                          `json:"schemaVersion"`
		Scope               string                          `json:"scope"`
		Inputs              CandidateReportInputs           `json:"inputs"`
		Selection           CandidateReportSelection        `json:"selection"`
		DeviceClassFacts    CandidateReportDeviceClassFacts `json:"deviceClassFacts,omitempty"`
		ArtifactSupply      CandidateReportArtifactSupply   `json:"artifactSupply,omitempty"`
		MetricObserverImage string                          `json:"metricObserverImage,omitempty"`
		LegacyObserverImage string                          `json:"powerObserverImage,omitempty"`
		Verdict             string                          `json:"verdict"`
		Reasons             []CandidateReportReason         `json:"reasons"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("candidate report contains multiple JSON values")
		}
		return err
	}
	if wire.MetricObserverImage != "" && wire.LegacyObserverImage != "" {
		return errors.New("candidate report contains multiple observer images")
	}
	r.SchemaVersion = wire.SchemaVersion
	r.Scope = wire.Scope
	r.Inputs = wire.Inputs
	r.Selection = wire.Selection
	r.DeviceClassFacts = wire.DeviceClassFacts
	r.ArtifactSupply = wire.ArtifactSupply
	r.MetricObserverImage = wire.MetricObserverImage
	if r.MetricObserverImage == "" {
		r.MetricObserverImage = wire.LegacyObserverImage
	}
	r.Verdict = wire.Verdict
	r.Reasons = wire.Reasons
	return nil
}

// CandidateReport is the shared wire contract between candidate resolution
// and Run preparation. It binds both catalog identities and the concrete
// execution material selected from them.
type CandidateReport struct {
	SchemaVersion       string                          `json:"schemaVersion"`
	Scope               string                          `json:"scope"`
	Inputs              CandidateReportInputs           `json:"inputs"`
	Selection           CandidateReportSelection        `json:"selection"`
	DeviceClassFacts    CandidateReportDeviceClassFacts `json:"deviceClassFacts,omitempty"`
	ArtifactSupply      CandidateReportArtifactSupply   `json:"artifactSupply,omitempty"`
	MetricObserverImage string                          `json:"metricObserverImage,omitempty"`
	Verdict             string                          `json:"verdict"`
	Reasons             []CandidateReportReason         `json:"reasons"`
}

type CandidateReportInputs struct {
	ModelSpec          CandidateReportObjectInput  `json:"modelSpec"`
	DeviceClass        CandidateReportObjectInput  `json:"deviceClass"`
	RuntimeDeclaration CandidateReportRuntimeInput `json:"runtimeDeclaration"`
}

type CandidateReportObjectInput struct {
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	ContentDigest   string `json:"contentDigest"`
}

type CandidateReportRuntimeInput struct {
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name,omitempty"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Key             string `json:"key,omitempty"`
	ContentDigest   string `json:"contentDigest"`
	Image           string `json:"image"`
}

type CandidateReportSelection struct {
	ExecutionPath  string `json:"executionPath"`
	Artifact       string `json:"artifact"`
	ArtifactDigest string `json:"artifactDigest"`
	PowerMode      string `json:"powerMode,omitempty"`
}

type CandidateReportDeviceClassFacts struct {
	Architecture string            `json:"architecture,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	PowerModes   []string          `json:"powerModes,omitempty"`
}

type CandidateReportArtifactSupply struct {
	Image        string `json:"image,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	Path         string `json:"path,omitempty"`
	Digest       string `json:"digest,omitempty"`
}

type CandidateReportReason struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

// DecodeCandidateReport strictly decodes exactly one JSON object. Legacy
// spike reports remain readable, but only the internal schema is required to
// carry complete Kubernetes source identity and execution material facts.
func DecodeCandidateReport(payload []byte) (CandidateReport, error) {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var report CandidateReport
	if err := decoder.Decode(&report); err != nil {
		return CandidateReport{}, fmt.Errorf("decode candidate report: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return CandidateReport{}, errors.New("decode candidate report: multiple JSON values")
		}
		return CandidateReport{}, fmt.Errorf("decode candidate report trailing content: %w", err)
	}
	if report.SchemaVersion != CandidateReportSchemaVersion &&
		report.SchemaVersion != previousCandidateReportSchemaVersion &&
		report.SchemaVersion != LegacyCandidateReportSchemaVersion {
		return CandidateReport{}, fmt.Errorf("unsupported candidate report schemaVersion %q", report.SchemaVersion)
	}
	if report.Scope != candidateReportScope {
		return CandidateReport{}, fmt.Errorf("unsupported candidate report scope %q", report.Scope)
	}
	if report.SchemaVersion == CandidateReportSchemaVersion || report.SchemaVersion == previousCandidateReportSchemaVersion {
		if err := validateInternalCandidateReport(report); err != nil {
			return CandidateReport{}, err
		}
	}
	return report, nil
}

func validateInternalCandidateReport(report CandidateReport) error {
	required := map[string]string{
		"ModelSpec name":                      report.Inputs.ModelSpec.Name,
		"ModelSpec UID":                       report.Inputs.ModelSpec.UID,
		"ModelSpec resourceVersion":           report.Inputs.ModelSpec.ResourceVersion,
		"ModelSpec contentDigest":             report.Inputs.ModelSpec.ContentDigest,
		"DeviceClass name":                    report.Inputs.DeviceClass.Name,
		"DeviceClass UID":                     report.Inputs.DeviceClass.UID,
		"DeviceClass resourceVersion":         report.Inputs.DeviceClass.ResourceVersion,
		"DeviceClass contentDigest":           report.Inputs.DeviceClass.ContentDigest,
		"runtime declaration namespace":       report.Inputs.RuntimeDeclaration.Namespace,
		"runtime declaration name":            report.Inputs.RuntimeDeclaration.Name,
		"runtime declaration UID":             report.Inputs.RuntimeDeclaration.UID,
		"runtime declaration resourceVersion": report.Inputs.RuntimeDeclaration.ResourceVersion,
		"runtime declaration key":             report.Inputs.RuntimeDeclaration.Key,
		"runtime declaration contentDigest":   report.Inputs.RuntimeDeclaration.ContentDigest,
		"runtime declaration image":           report.Inputs.RuntimeDeclaration.Image,
		"selection executionPath":             report.Selection.ExecutionPath,
		"selection artifact":                  report.Selection.Artifact,
		"selection artifactDigest":            report.Selection.ArtifactDigest,
		"selection powerMode":                 report.Selection.PowerMode,
		"DeviceClass architecture":            report.DeviceClassFacts.Architecture,
		"artifact supply image":               report.ArtifactSupply.Image,
		"artifact supply architecture":        report.ArtifactSupply.Architecture,
		"artifact supply path":                report.ArtifactSupply.Path,
		"artifact supply digest":              report.ArtifactSupply.Digest,
		"metric-observer image":               report.MetricObserverImage,
		"verdict":                             report.Verdict,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("candidate report %s is required without surrounding whitespace", name)
		}
	}
	if len(report.DeviceClassFacts.NodeSelector) == 0 || len(report.DeviceClassFacts.PowerModes) == 0 {
		return errors.New("candidate report DeviceClass nodeSelector and powerModes are required")
	}
	if report.Verdict == "Compatible" && len(report.Reasons) != 0 {
		return errors.New("compatible candidate report must not contain reasons")
	}
	return nil
}

func ValidateCandidateReport(payload []byte, intent Intent) error {
	sum := sha256.Sum256(payload)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != intent.Candidate.ReportDigest {
		return fmt.Errorf("candidate report digest mismatch: %s", actual)
	}
	report, err := DecodeCandidateReport(payload)
	if err != nil {
		return err
	}
	checks := []struct{ name, want, got string }{
		{"verdict", intent.Candidate.Verdict, report.Verdict},
		{"ModelSpec name", intent.State.Model, report.Inputs.ModelSpec.Name},
		{"ModelSpec digest", intent.Candidate.ModelSpecContentDigest, report.Inputs.ModelSpec.ContentDigest},
		{"DeviceClass name", intent.State.DeviceClass, report.Inputs.DeviceClass.Name},
		{"DeviceClass digest", intent.Candidate.DeviceClassContentDigest, report.Inputs.DeviceClass.ContentDigest},
		{"runtime declaration digest", intent.Candidate.RuntimeDeclarationDigest, report.Inputs.RuntimeDeclaration.ContentDigest},
		{"runtime image", intent.Images.Runtime, report.Inputs.RuntimeDeclaration.Image},
		{"execution path", intent.Candidate.ExecutionPath, report.Selection.ExecutionPath},
		{"artifact", intent.State.Artifact, report.Selection.Artifact},
		{"artifact digest", intent.State.ArtifactDigest, report.Selection.ArtifactDigest},
	}
	if report.SchemaVersion == CandidateReportSchemaVersion || report.SchemaVersion == previousCandidateReportSchemaVersion {
		checks = append(checks,
			struct{ name, want, got string }{"power mode", intent.State.PowerMode, report.Selection.PowerMode},
			struct{ name, want, got string }{"artifact image", intent.Images.Artifact, report.ArtifactSupply.Image},
			struct{ name, want, got string }{"artifact supply path", intent.State.ArtifactPath, report.ArtifactSupply.Path},
			struct{ name, want, got string }{"artifact supply digest", intent.State.ArtifactDigest, report.ArtifactSupply.Digest},
			struct{ name, want, got string }{"DeviceClass architecture", intent.TargetNode.Architecture, report.DeviceClassFacts.Architecture},
			struct{ name, want, got string }{"artifact architecture", intent.TargetNode.Architecture, report.ArtifactSupply.Architecture},
			struct{ name, want, got string }{"metric-observer image", intent.Images.MetricObserver, report.MetricObserverImage},
		)
	}
	for _, check := range checks {
		if check.want != check.got {
			return fmt.Errorf("candidate %s mismatch", check.name)
		}
	}
	return nil
}
