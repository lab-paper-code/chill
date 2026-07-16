package controller

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/lab-paper-code/chill/internal/profiler"
	corev1 "k8s.io/api/core/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	executionFactsSchema     = "chill.dacs.io/profiler-execution-facts.v1alpha1"
	runtimeDeclarationSchema = "spikes.chill.dacs.io/runtime-declaration.v1alpha1"
	artifactSupplySchema     = "chill.dacs.io/artifact-supply.v1alpha1"
	trustedRuntimeInspection = "exact-image-runtime-inspection-v1"
)

var (
	pinnedWorkloadImage = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
	artifactPath        = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
	digestPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type executionFactsDocument struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Declarations  []executionDeclaration `json:"declarations"`
}

type executionDeclaration struct {
	ModelSpec     string                   `json:"modelSpec"`
	DeviceClass   string                   `json:"deviceClass"`
	ExecutionPath string                   `json:"executionPath"`
	Runtime       runtimeDeclarationSource `json:"runtime"`
	Artifact      artifactSupply           `json:"artifact"`
}

// runtimeDeclarationSource preserves the exact producer output instead of
// allowing installation configuration to restate observed runtime facts.
type runtimeDeclarationSource struct {
	ContentDigest string `json:"contentDigest"`
	Payload       string `json:"payload"`
}

type runtimeDeclaration struct {
	SchemaVersion string `json:"schemaVersion"`
	Image         string `json:"image"`
	Architecture  string `json:"architecture"`
	Runtime       struct {
		Family   string   `json:"family"`
		Backends []string `json:"backends"`
	} `json:"runtime"`
	Verification string `json:"verification"`
}

// artifactSupply is a versioned delivery contract. The future Job worker must
// still hash the bytes copied from Path and compare them with Digest.
type artifactSupply struct {
	SchemaVersion string `json:"schemaVersion"`
	Image         string `json:"image"`
	Architecture  string `json:"architecture"`
	Path          string `json:"path"`
	Digest        string `json:"digest"`
}

type resolvedExecutionDeclaration struct {
	ModelSpec                string
	DeviceClass              string
	ExecutionPath            string
	Runtime                  runtimeDeclaration
	RuntimeDeclarationDigest string
	Artifact                 artifactSupply
}

func selectExecutionDeclaration(
	configMap *corev1.ConfigMap,
	configMapKey string,
	selection profiler.CandidateSelection,
) (resolvedExecutionDeclaration, error) {
	if configMap.UID == "" || configMap.ResourceVersion == "" {
		return resolvedExecutionDeclaration{}, errors.New("Profiler fact-source ConfigMap has incomplete API identity")
	}
	raw, exists := configMap.Data[configMapKey]
	if !exists || strings.TrimSpace(raw) == "" {
		return resolvedExecutionDeclaration{}, fmt.Errorf("Profiler fact-source ConfigMap missing key %q", configMapKey)
	}
	documentBytes, err := readSingleYAMLDocument([]byte(raw))
	if err != nil {
		return resolvedExecutionDeclaration{}, fmt.Errorf("decode Profiler execution facts: %w", err)
	}
	var document executionFactsDocument
	if err := yaml.UnmarshalStrict(documentBytes, &document); err != nil {
		return resolvedExecutionDeclaration{}, fmt.Errorf("decode Profiler execution facts: %w", err)
	}
	if document.SchemaVersion != executionFactsSchema {
		return resolvedExecutionDeclaration{}, fmt.Errorf("unsupported Profiler execution facts schema %q", document.SchemaVersion)
	}
	if len(document.Declarations) == 0 {
		return resolvedExecutionDeclaration{}, errors.New("Profiler execution facts require at least one declaration")
	}

	seen := make(map[string]struct{}, len(document.Declarations))
	var selected *resolvedExecutionDeclaration
	for index := range document.Declarations {
		declaration, err := validateExecutionDeclaration(document.Declarations[index])
		if err != nil {
			return resolvedExecutionDeclaration{}, fmt.Errorf("validate execution declaration %d: %w", index, err)
		}
		identity := declaration.ModelSpec + "\x00" + declaration.DeviceClass + "\x00" + declaration.ExecutionPath
		if _, duplicate := seen[identity]; duplicate {
			return resolvedExecutionDeclaration{}, fmt.Errorf("duplicate execution declaration for %s/%s/%s",
				declaration.ModelSpec, declaration.DeviceClass, declaration.ExecutionPath)
		}
		seen[identity] = struct{}{}
		if declaration.ModelSpec == selection.ModelSpec && declaration.DeviceClass == selection.DeviceClass &&
			declaration.ExecutionPath == selection.ExecutionPath {
			copy := declaration
			copy.Runtime.Runtime.Backends = append([]string(nil), declaration.Runtime.Runtime.Backends...)
			selected = &copy
		}
	}
	if selected == nil {
		return resolvedExecutionDeclaration{}, fmt.Errorf("no trusted execution declaration for %s/%s/%s",
			selection.ModelSpec, selection.DeviceClass, selection.ExecutionPath)
	}
	return *selected, nil
}

func readSingleYAMLDocument(raw []byte) ([]byte, error) {
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	document, err := reader.Read()
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(document)) == 0 {
		return nil, errors.New("execution facts must contain one non-empty YAML document")
	}
	trailing, err := reader.Read()
	if !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(trailing)) == 0 {
			return nil, errors.New("execution facts must contain exactly one YAML document")
		}
		return nil, errors.New("execution facts must contain exactly one YAML document")
	}
	return document, nil
}

func validateExecutionDeclaration(source executionDeclaration) (resolvedExecutionDeclaration, error) {
	for name, value := range map[string]string{
		"modelSpec": source.ModelSpec, "deviceClass": source.DeviceClass,
		"executionPath": source.ExecutionPath, "runtime declaration digest": source.Runtime.ContentDigest,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return resolvedExecutionDeclaration{}, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	runtime, err := decodeRuntimeDeclaration(source.Runtime)
	if err != nil {
		return resolvedExecutionDeclaration{}, err
	}
	if err := validateArtifactSupply(source.Artifact); err != nil {
		return resolvedExecutionDeclaration{}, err
	}
	return resolvedExecutionDeclaration{
		ModelSpec: source.ModelSpec, DeviceClass: source.DeviceClass, ExecutionPath: source.ExecutionPath,
		Runtime: runtime, RuntimeDeclarationDigest: source.Runtime.ContentDigest, Artifact: source.Artifact,
	}, nil
}

func decodeRuntimeDeclaration(source runtimeDeclarationSource) (runtimeDeclaration, error) {
	sum := sha256.Sum256([]byte(source.Payload))
	actualDigest := "sha256:" + hex.EncodeToString(sum[:])
	if source.ContentDigest != actualDigest {
		return runtimeDeclaration{}, errors.New("runtime producer declaration content digest mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(source.Payload)))
	decoder.DisallowUnknownFields()
	var declaration runtimeDeclaration
	if err := decoder.Decode(&declaration); err != nil {
		return runtimeDeclaration{}, fmt.Errorf("decode runtime producer declaration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return runtimeDeclaration{}, errors.New("runtime producer declaration must contain exactly one JSON object")
	}
	if declaration.SchemaVersion != runtimeDeclarationSchema {
		return runtimeDeclaration{}, fmt.Errorf("unsupported runtime declaration schema %q", declaration.SchemaVersion)
	}
	for name, value := range map[string]string{
		"runtime image": declaration.Image, "runtime architecture": declaration.Architecture,
		"runtime family": declaration.Runtime.Family, "runtime verification": declaration.Verification,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return runtimeDeclaration{}, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if !pinnedWorkloadImage.MatchString(declaration.Image) {
		return runtimeDeclaration{}, errors.New("runtime image must be digest-pinned")
	}
	if declaration.Verification != trustedRuntimeInspection {
		return runtimeDeclaration{}, fmt.Errorf("unsupported runtime verification %q", declaration.Verification)
	}
	if len(declaration.Runtime.Backends) == 0 {
		return runtimeDeclaration{}, errors.New("runtime backends are required")
	}
	seen := map[string]struct{}{}
	for _, backend := range declaration.Runtime.Backends {
		if strings.TrimSpace(backend) == "" || strings.TrimSpace(backend) != backend {
			return runtimeDeclaration{}, errors.New("runtime backend is required without surrounding whitespace")
		}
		if _, duplicate := seen[backend]; duplicate {
			return runtimeDeclaration{}, fmt.Errorf("duplicate runtime backend %q", backend)
		}
		seen[backend] = struct{}{}
	}
	return declaration, nil
}

func validateArtifactSupply(artifact artifactSupply) error {
	if artifact.SchemaVersion != artifactSupplySchema {
		return fmt.Errorf("unsupported artifact supply schema %q", artifact.SchemaVersion)
	}
	for name, value := range map[string]string{
		"artifact image": artifact.Image, "artifact architecture": artifact.Architecture,
		"artifact path": artifact.Path, "artifact digest": artifact.Digest,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if !pinnedWorkloadImage.MatchString(artifact.Image) {
		return errors.New("artifact image must be digest-pinned")
	}
	if !artifactPath.MatchString(artifact.Path) || path.Clean(artifact.Path) != artifact.Path {
		return errors.New("artifact path must be a clean absolute container path")
	}
	if !digestPattern.MatchString(artifact.Digest) {
		return errors.New("artifact digest must be a canonical SHA-256 digest")
	}
	return nil
}
