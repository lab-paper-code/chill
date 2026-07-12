package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	modelSpecAPIVersion       = "edge.dacs.io/v1alpha1"
	modelSpecKind             = "ModelSpec"
	deviceClassKind           = "DeviceClass"
	runtimeDeclarationSchema  = "spikes.chill.dacs.io/runtime-declaration.v1alpha1"
	trustedVerificationMethod = "exact-image-runtime-inspection-v1"
	reportSchema              = "spikes.chill.dacs.io/modelcompat-report.v1alpha1"
	reportScope               = "StaticCompatibility"
)

var (
	sha256Pattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	pinnedImagePattern = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)
)

type runtimeDeclaration struct {
	SchemaVersion string `json:"schemaVersion"`
	Image         string `json:"image"`
	Architecture  string `json:"architecture"`
	Runtime       struct {
		Family   string    `json:"family"`
		Backends *[]string `json:"backends"`
	} `json:"runtime"`
	Verification string `json:"verification"`
}

type namedInput struct {
	Name          string `json:"name"`
	ContentDigest string `json:"contentDigest"`
}

type runtimeInput struct {
	ContentDigest string `json:"contentDigest"`
	Image         string `json:"image"`
}

type reportInputs struct {
	ModelSpec          namedInput   `json:"modelSpec"`
	DeviceClass        namedInput   `json:"deviceClass"`
	RuntimeDeclaration runtimeInput `json:"runtimeDeclaration"`
}

type reportSelection struct {
	ExecutionPath  string `json:"executionPath"`
	Artifact       string `json:"artifact"`
	ArtifactDigest string `json:"artifactDigest"`
}

type reportReason struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

type compatibilityReport struct {
	SchemaVersion string          `json:"schemaVersion"`
	Scope         string          `json:"scope"`
	Inputs        reportInputs    `json:"inputs"`
	Selection     reportSelection `json:"selection"`
	Verdict       string          `json:"verdict"`
	Reasons       []reportReason  `json:"reasons"`
}

type modelSelection struct {
	model    *edgev1alpha1.ModelSpec
	artifact edgev1alpha1.ModelArtifact
	path     edgev1alpha1.ModelExecutionPath
}

type options struct {
	modelSpecPath          string
	deviceClassPath        string
	runtimeDeclarationPath string
	executionPath          string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("candidate-report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts options
	flags.StringVar(&opts.modelSpecPath, "model-spec", "", "path to one ModelSpec YAML file")
	flags.StringVar(&opts.deviceClassPath, "device-class", "", "path to one DeviceClass YAML file")
	flags.StringVar(&opts.runtimeDeclarationPath, "runtime-declaration", "", "path to one runtime declaration JSON file")
	flags.StringVar(&opts.executionPath, "execution-path", "", "local execution-path name to evaluate")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(opts.modelSpecPath) == "" ||
		strings.TrimSpace(opts.deviceClassPath) == "" ||
		strings.TrimSpace(opts.runtimeDeclarationPath) == "" || strings.TrimSpace(opts.executionPath) == "" {
		writeDiagnostic(stderr, "candidate-report: all four flags are required and positional arguments are not accepted\n")
		return 2
	}

	report, result, err := buildReport(opts)
	if err != nil {
		writeDiagnostic(stderr, "candidate-report: %v\n", err)
		return 1
	}
	payload, err := json.Marshal(report)
	if err != nil {
		writeDiagnostic(stderr, "candidate-report: encode report: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", payload); err != nil {
		writeDiagnostic(stderr, "candidate-report: write report: %v\n", err)
		return 1
	}
	if _, err := io.WriteString(stderr, humanReport(report, result)); err != nil {
		return 1
	}
	return 0
}

func buildReport(opts options) (compatibilityReport, modelcompat.Result, error) {
	modelBytes, modelDigest, err := readInput(opts.modelSpecPath)
	if err != nil {
		return compatibilityReport{}, modelcompat.Result{}, err
	}
	deviceBytes, deviceDigest, err := readInput(opts.deviceClassPath)
	if err != nil {
		return compatibilityReport{}, modelcompat.Result{}, err
	}
	runtimeBytes, runtimeDigest, err := readInput(opts.runtimeDeclarationPath)
	if err != nil {
		return compatibilityReport{}, modelcompat.Result{}, err
	}

	selection, err := decodeModelSpec(modelBytes, opts.executionPath)
	if err != nil {
		return compatibilityReport{}, modelcompat.Result{}, fmt.Errorf("ModelSpec input: %w", err)
	}
	deviceClass, err := decodeDeviceClass(deviceBytes)
	if err != nil {
		return compatibilityReport{}, modelcompat.Result{}, fmt.Errorf("DeviceClass input: %w", err)
	}
	runtime, err := decodeRuntimeDeclaration(runtimeBytes)
	if err != nil {
		return compatibilityReport{}, modelcompat.Result{}, fmt.Errorf("runtime declaration input: %w", err)
	}

	result := modelcompat.Evaluate(modelcompat.Input{
		Artifact: modelcompat.ArtifactRequirement{Format: selection.artifact.Format},
		Path: modelcompat.ExecutionPathRequirement{
			RuntimeFamily: selection.path.Runtime.Family,
			Backend:       selection.path.Runtime.Backend,
		},
		DeviceClass: modelcompat.DeviceClassInfo{Architecture: deviceClass.Spec.Architecture},
		Runtime: &modelcompat.RuntimeDeclaration{
			Trusted:      runtime.Verification == trustedVerificationMethod,
			Architecture: runtime.Architecture,
			Family:       runtime.Runtime.Family,
			Backends:     *runtime.Runtime.Backends,
		},
	})

	reasons := make([]reportReason, len(result.Reasons))
	for index, reason := range result.Reasons {
		reasons[index] = reportReason{Kind: string(reason.Kind), Code: string(reason.Code)}
	}
	report := compatibilityReport{
		SchemaVersion: reportSchema,
		Scope:         reportScope,
		Inputs: reportInputs{
			ModelSpec:   namedInput{Name: selection.model.Name, ContentDigest: modelDigest},
			DeviceClass: namedInput{Name: deviceClass.Name, ContentDigest: deviceDigest},
			RuntimeDeclaration: runtimeInput{
				ContentDigest: runtimeDigest,
				Image:         runtime.Image,
			},
		},
		Selection: reportSelection{
			ExecutionPath:  selection.path.Name,
			Artifact:       selection.artifact.Name,
			ArtifactDigest: selection.artifact.Digest,
		},
		Verdict: string(result.Verdict),
		Reasons: reasons,
	}
	return report, result, nil
}

func readInput(path string) ([]byte, string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read %q: %w", path, err)
	}
	digest := sha256.Sum256(value)
	return value, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func decodeModelSpec(value []byte, executionPath string) (modelSelection, error) {
	model := &edgev1alpha1.ModelSpec{}
	if err := decodeSingleYAML(value, model); err != nil {
		return modelSelection{}, err
	}
	if model.APIVersion != modelSpecAPIVersion || model.Kind != modelSpecKind {
		return modelSelection{}, errors.New("expected edge.dacs.io/v1alpha1 ModelSpec")
	}
	if strings.TrimSpace(model.Name) == "" || len(model.Spec.Artifacts) == 0 || len(model.Spec.ExecutionPaths) == 0 {
		return modelSelection{}, errors.New("name, artifacts, and executionPaths are required")
	}
	artifacts := make(map[string]edgev1alpha1.ModelArtifact, len(model.Spec.Artifacts))
	for _, artifact := range model.Spec.Artifacts {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.Format) == "" ||
			!sha256Pattern.MatchString(artifact.Digest) {
			return modelSelection{}, fmt.Errorf("artifact %q has invalid required fields", artifact.Name)
		}
		if _, exists := artifacts[artifact.Name]; exists {
			return modelSelection{}, fmt.Errorf("duplicate artifact name %q", artifact.Name)
		}
		artifacts[artifact.Name] = artifact
	}
	paths := make(map[string]edgev1alpha1.ModelExecutionPath, len(model.Spec.ExecutionPaths))
	for _, path := range model.Spec.ExecutionPaths {
		if strings.TrimSpace(path.Name) == "" || strings.TrimSpace(path.Artifact) == "" ||
			strings.TrimSpace(path.Runtime.Family) == "" || strings.TrimSpace(path.Runtime.Backend) == "" {
			return modelSelection{}, fmt.Errorf("execution path %q has invalid required fields", path.Name)
		}
		if _, exists := paths[path.Name]; exists {
			return modelSelection{}, fmt.Errorf("duplicate execution-path name %q", path.Name)
		}
		if _, exists := artifacts[path.Artifact]; !exists {
			return modelSelection{}, fmt.Errorf(
				"execution path %q references missing artifact %q", path.Name, path.Artifact,
			)
		}
		paths[path.Name] = path
	}
	path, exists := paths[executionPath]
	if !exists {
		return modelSelection{}, fmt.Errorf("execution path %q not found", executionPath)
	}
	return modelSelection{model: model, artifact: artifacts[path.Artifact], path: path}, nil
}

func decodeDeviceClass(value []byte) (*edgev1alpha1.DeviceClass, error) {
	deviceClass := &edgev1alpha1.DeviceClass{}
	if err := decodeSingleYAML(value, deviceClass); err != nil {
		return nil, err
	}
	if deviceClass.APIVersion != modelSpecAPIVersion || deviceClass.Kind != deviceClassKind {
		return nil, errors.New("expected edge.dacs.io/v1alpha1 DeviceClass")
	}
	if strings.TrimSpace(deviceClass.Name) == "" || strings.TrimSpace(deviceClass.Spec.Architecture) == "" ||
		len(deviceClass.Spec.NodeSelector) == 0 || len(deviceClass.Spec.PowerModes) == 0 {
		return nil, errors.New("name, architecture, nodeSelector, and powerModes are required")
	}
	for _, mode := range deviceClass.Spec.PowerModes {
		if strings.TrimSpace(mode.Name) == "" {
			return nil, errors.New("power-mode name is required")
		}
	}
	return deviceClass, nil
}

func decodeSingleYAML(value []byte, target any) error {
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(value)))
	document, err := reader.Read()
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(document)) == 0 {
		return errors.New("expected one non-empty YAML document")
	}
	if trailing, err := reader.Read(); err == nil {
		if len(bytes.TrimSpace(trailing)) == 0 {
			return errors.New("empty trailing YAML document is not accepted")
		}
		return errors.New("expected exactly one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return yaml.UnmarshalStrict(document, target)
}

func decodeRuntimeDeclaration(value []byte) (runtimeDeclaration, error) {
	if err := rejectDuplicateJSONKeys(value); err != nil {
		return runtimeDeclaration{}, err
	}
	var declaration runtimeDeclaration
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&declaration); err != nil {
		return runtimeDeclaration{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return runtimeDeclaration{}, errors.New("expected exactly one JSON object")
	}
	if declaration.SchemaVersion != runtimeDeclarationSchema {
		return runtimeDeclaration{}, fmt.Errorf("unsupported schemaVersion %q", declaration.SchemaVersion)
	}
	if !pinnedImagePattern.MatchString(declaration.Image) {
		return runtimeDeclaration{}, errors.New("image must be an exact repository@sha256 reference")
	}
	if strings.TrimSpace(declaration.Architecture) == "" ||
		strings.TrimSpace(declaration.Runtime.Family) == "" ||
		strings.TrimSpace(declaration.Verification) == "" || declaration.Runtime.Backends == nil {
		return runtimeDeclaration{}, errors.New(
			"architecture, runtime family, backends, and verification are required",
		)
	}
	for _, backend := range *declaration.Runtime.Backends {
		if strings.TrimSpace(backend) == "" {
			return runtimeDeclaration{}, errors.New("runtime backend entries must be non-empty")
		}
	}
	return declaration, nil
}

func rejectDuplicateJSONKeys(value []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("expected exactly one JSON value")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return fmt.Errorf("unexpected JSON closing delimiter %q", closing)
	}
	return nil
}

func humanReport(report compatibilityReport, result modelcompat.Result) string {
	meaning := "all required CPU ORT static declarations matched"
	switch result.Verdict {
	case modelcompat.VerdictIncompatible:
		meaning = "trusted static declarations contain an explicit conflict"
	case modelcompat.VerdictUnknown:
		meaning = "a required trusted static fact could not be established"
	}
	lines := []string{
		"--- CHILL STATIC COMPATIBILITY REPORT ------------------------",
		fmt.Sprintf("scope                   : %s declarations only", report.Scope),
		fmt.Sprintf("model                   : %s", report.Inputs.ModelSpec.Name),
		fmt.Sprintf("execution path          : %s", report.Selection.ExecutionPath),
		fmt.Sprintf("artifact                : %s", report.Selection.Artifact),
		fmt.Sprintf("device class            : %s", report.Inputs.DeviceClass.Name),
		fmt.Sprintf("runtime image           : %s", report.Inputs.RuntimeDeclaration.Image),
		fmt.Sprintf("verdict                 : %s", report.Verdict),
		fmt.Sprintf("meaning                 : %s", meaning),
	}
	for _, reason := range report.Reasons {
		lines = append(lines, fmt.Sprintf("reason                  : %s/%s", reason.Kind, reason.Code))
	}
	lines = append(lines,
		"not proven              : Node availability, scheduling, image pull,",
		"                          model load, inference, performance, power, or SLO",
		"---------------------------------------------------------------",
	)
	return strings.Join(lines, "\n") + "\n"
}

func writeDiagnostic(writer io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(writer, format, args...)
}
