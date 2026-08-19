package kube

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	corev1 "k8s.io/api/core/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	executionFactsSchema     = "chill.dacs.io/profiler-execution-facts.v1alpha5"
	runtimeDeclarationSchema = "chill.dacs.io/runtime-declaration.v1alpha2"
	artifactSupplySchema     = "chill.dacs.io/artifact-supply.v1alpha1"
	trustedRuntimeInspection = "exact-image-runtime-inspection-v1"
)

var artifactPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

type executionFactsDocument struct {
	SchemaVersion       string                 `json:"schemaVersion"`
	MetricObserverImage string                 `json:"metricObserverImage"`
	Declarations        []executionDeclaration `json:"declarations"`
}

type executionDeclaration struct {
	ModelSpec     string                   `json:"modelSpec"`
	DeviceClass   string                   `json:"deviceClass"`
	ExecutionPath string                   `json:"executionPath"`
	Runtime       runtimeDeclarationSource `json:"runtime"`
	Artifact      artifactSupply           `json:"artifact"`
	Host          *hostExecutionContract   `json:"host,omitempty"`
	Access        *runtimeAccessContract   `json:"access,omitempty"`
}

// runtimeDeclarationSource preserves the exact producer output instead of
// allowing installation configuration to restate observed runtime facts.
type runtimeDeclarationSource struct {
	Payload string `json:"payload"`
}

type runtimeDeclaration struct {
	SchemaVersion string `json:"schemaVersion"`
	Image         string `json:"image"`
	Architecture  string `json:"architecture"`
	Runtime       struct {
		Family   string   `json:"family"`
		Backends []string `json:"backends"`
	} `json:"runtime"`
	Packages     []runtimePackage `json:"packages,omitempty"`
	Verification string           `json:"verification"`
}

type runtimePackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// artifactSupply is a versioned delivery contract.
type artifactSupply struct {
	SchemaVersion string `json:"schemaVersion"`
	Image         string `json:"image"`
	Architecture  string `json:"architecture"`
	Path          string `json:"path"`
}

// hostExecutionContract is installation configuration, not a current host
// observation. The Job projection and worker must mount every declared
// boundary before measurement.
type hostExecutionContract struct {
	Libraries  []hostLibraryContract `json:"libraries"`
	Driver     hostDriverContract    `json:"driver"`
	Devices    []hostDeviceContract  `json:"devices"`
	Platform   *hostPlatformContract `json:"platform,omitempty"`
	Allocation npuAllocationClaim    `json:"allocation"`
}

type hostLibraryContract struct {
	Name          string `json:"name"`
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
}

type hostDriverContract struct {
	Name        string `json:"name"`
	VersionPath string `json:"versionPath"`
	Version     string `json:"version"`
}

type hostDeviceContract struct {
	Path string `json:"path"`
}

type hostPlatformContract struct {
	CompatiblePath  string `json:"compatiblePath"`
	CompatibleToken string `json:"compatibleToken"`
}

// runtimeAccessContract is a Kubernetes delivery requirement, not physical
// host identity.
type runtimeAccessContract struct {
	Privileged         bool    `json:"privileged"`
	SupplementalGroups []int64 `json:"supplementalGroups,omitempty"`
}

type npuAllocationClaim struct {
	ResourceName       string `json:"resourceName"`
	ExclusivityClaimed bool   `json:"exclusivityClaimed"`
}

type resolvedExecutionDeclaration struct {
	ModelSpec           string
	DeviceClass         string
	ExecutionPath       string
	Runtime             runtimeDeclaration
	Artifact            artifactSupply
	Host                *hostExecutionContract
	Access              *runtimeAccessContract
	MetricObserverImage string
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
	if strings.TrimSpace(document.MetricObserverImage) == "" ||
		strings.TrimSpace(document.MetricObserverImage) != document.MetricObserverImage {
		return resolvedExecutionDeclaration{}, errors.New("metric-observer image is required without surrounding whitespace")
	}
	if len(document.Declarations) == 0 {
		return resolvedExecutionDeclaration{}, errors.New("Profiler execution facts require at least one declaration")
	}

	seen := make(map[string]struct{}, len(document.Declarations))
	var selected *resolvedExecutionDeclaration
	for index := range document.Declarations {
		declaration, err := validateExecutionDeclaration(document.SchemaVersion, document.Declarations[index])
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
			copy.Runtime.Packages = append([]runtimePackage(nil), declaration.Runtime.Packages...)
			copy.Host = cloneHostExecutionContract(declaration.Host)
			copy.Access = cloneRuntimeAccessContract(declaration.Access)
			copy.MetricObserverImage = document.MetricObserverImage
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

func validateExecutionDeclaration(schemaVersion string, source executionDeclaration) (resolvedExecutionDeclaration, error) {
	for name, value := range map[string]string{
		"modelSpec": source.ModelSpec, "deviceClass": source.DeviceClass,
		"executionPath": source.ExecutionPath,
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
	if source.Host != nil {
		if err := validateHostExecutionContract(*source.Host); err != nil {
			return resolvedExecutionDeclaration{}, err
		}
	}
	if schemaVersion == executionFactsSchema && (source.Host == nil) != (source.Access == nil) {
		return resolvedExecutionDeclaration{}, errors.New("host and runtime access contracts must be supplied together")
	}
	if source.Access != nil {
		if err := validateRuntimeAccessContract(*source.Access); err != nil {
			return resolvedExecutionDeclaration{}, err
		}
	}
	return resolvedExecutionDeclaration{
		ModelSpec: source.ModelSpec, DeviceClass: source.DeviceClass, ExecutionPath: source.ExecutionPath,
		Runtime: runtime, Artifact: source.Artifact,
		Host: cloneHostExecutionContract(source.Host), Access: cloneRuntimeAccessContract(source.Access),
	}, nil
}

func decodeRuntimeDeclaration(source runtimeDeclarationSource) (runtimeDeclaration, error) {
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
	seenPackages := make(map[string]struct{}, len(declaration.Packages))
	for _, pkg := range declaration.Packages {
		if strings.TrimSpace(pkg.Name) == "" || strings.TrimSpace(pkg.Name) != pkg.Name ||
			strings.TrimSpace(pkg.Version) == "" || strings.TrimSpace(pkg.Version) != pkg.Version {
			return runtimeDeclaration{}, errors.New("runtime package name and version are required without surrounding whitespace")
		}
		if _, duplicate := seenPackages[pkg.Name]; duplicate {
			return runtimeDeclaration{}, fmt.Errorf("duplicate runtime package %q", pkg.Name)
		}
		seenPackages[pkg.Name] = struct{}{}
	}
	return declaration, nil
}

func validateHostExecutionContract(contract hostExecutionContract) error {
	if len(contract.Libraries) == 0 || len(contract.Devices) == 0 {
		return errors.New("host contract requires libraries and devices")
	}
	seenLibraries := make(map[string]struct{}, len(contract.Libraries))
	for _, library := range contract.Libraries {
		if strings.TrimSpace(library.Name) == "" || strings.TrimSpace(library.Name) != library.Name {
			return errors.New("host library name is required without surrounding whitespace")
		}
		if !cleanAbsolutePath(library.HostPath) || !cleanAbsolutePath(library.ContainerPath) {
			return errors.New("host library paths must be clean absolute paths")
		}
		if _, duplicate := seenLibraries[library.Name]; duplicate {
			return fmt.Errorf("duplicate host library %q", library.Name)
		}
		seenLibraries[library.Name] = struct{}{}
	}
	if strings.TrimSpace(contract.Driver.Name) == "" || strings.TrimSpace(contract.Driver.Name) != contract.Driver.Name ||
		strings.TrimSpace(contract.Driver.Version) == "" || strings.TrimSpace(contract.Driver.Version) != contract.Driver.Version ||
		!cleanAbsolutePath(contract.Driver.VersionPath) {
		return errors.New("host driver name, version path, and version are required")
	}
	seenDevices := make(map[string]struct{}, len(contract.Devices))
	for _, device := range contract.Devices {
		if !cleanAbsolutePath(device.Path) {
			return errors.New("host device must be a clean absolute path")
		}
		if _, duplicate := seenDevices[device.Path]; duplicate {
			return fmt.Errorf("duplicate host device %q", device.Path)
		}
		seenDevices[device.Path] = struct{}{}
	}
	if contract.Platform != nil {
		if !cleanAbsolutePath(contract.Platform.CompatiblePath) ||
			strings.TrimSpace(contract.Platform.CompatibleToken) == "" ||
			strings.TrimSpace(contract.Platform.CompatibleToken) != contract.Platform.CompatibleToken {
			return errors.New("host platform compatible path and token are required")
		}
	}
	resourceName := contract.Allocation.ResourceName
	if strings.TrimSpace(resourceName) != resourceName {
		return errors.New("NPU resource name has surrounding whitespace")
	}
	if resourceName != "" && (!strings.Contains(resourceName, "/") || len(k8svalidation.IsQualifiedName(resourceName)) != 0) {
		return errors.New("NPU resource name must be a qualified extended-resource name")
	}
	if contract.Allocation.ExclusivityClaimed && resourceName == "" {
		return errors.New("cannot claim NPU exclusivity without a Kubernetes resource")
	}
	return nil
}

func cloneHostExecutionContract(source *hostExecutionContract) *hostExecutionContract {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Libraries = append([]hostLibraryContract(nil), source.Libraries...)
	copy.Devices = append([]hostDeviceContract(nil), source.Devices...)
	if source.Platform != nil {
		platform := *source.Platform
		copy.Platform = &platform
	}
	return &copy
}

func validateRuntimeAccessContract(contract runtimeAccessContract) error {
	seen := make(map[int64]struct{}, len(contract.SupplementalGroups))
	for _, group := range contract.SupplementalGroups {
		if group < 1 || group > int64(^uint32(0)>>1) {
			return errors.New("runtime supplemental group must be a positive 32-bit ID")
		}
		if _, duplicate := seen[group]; duplicate {
			return fmt.Errorf("duplicate runtime supplemental group %d", group)
		}
		seen[group] = struct{}{}
	}
	return nil
}

func cloneRuntimeAccessContract(source *runtimeAccessContract) *runtimeAccessContract {
	if source == nil {
		return nil
	}
	copy := *source
	copy.SupplementalGroups = append([]int64(nil), source.SupplementalGroups...)
	return &copy
}

func cleanAbsolutePath(value string) bool {
	return artifactPath.MatchString(value) && path.Clean(value) == value
}

func validateArtifactSupply(artifact artifactSupply) error {
	if artifact.SchemaVersion != artifactSupplySchema {
		return fmt.Errorf("unsupported artifact supply schema %q", artifact.SchemaVersion)
	}
	for name, value := range map[string]string{
		"artifact image": artifact.Image, "artifact architecture": artifact.Architecture,
		"artifact path": artifact.Path,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if !cleanAbsolutePath(artifact.Path) {
		return errors.New("artifact path must be a clean absolute container path")
	}
	return nil
}
