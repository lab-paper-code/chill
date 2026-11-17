package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	"k8s.io/apimachinery/pkg/api/resource"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	CandidateReportSchemaVersion = "chill.dacs.io/modelcompat-report.v1alpha13"
	candidateReportScope         = "StaticCompatibility"
)

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
	ArtifactCapability  *CandidateArtifactCapability    `json:"artifactCapability,omitempty"`
	RuntimePackages     []CandidateReportRuntimePackage `json:"runtimePackages,omitempty"`
	HostContract        *CandidateReportHostContract    `json:"hostContract,omitempty"`
	RuntimeAccess       *CandidateReportRuntimeAccess   `json:"runtimeAccess,omitempty"`
	Firstfloor          *modelcompat.FirstfloorSettings `json:"firstfloor,omitempty"`
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
}

type CandidateReportRuntimeInput struct {
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name,omitempty"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Key             string `json:"key,omitempty"`
	Image           string `json:"image"`
}

type CandidateReportSelection struct {
	ExecutionPath string          `json:"executionPath"`
	Artifact      string          `json:"artifact"`
	PowerMode     string          `json:"powerMode,omitempty"`
	Actuator      *ActuatorTarget `json:"actuator,omitempty"`
}

type CandidateReportDeviceClassFacts struct {
	Architecture string            `json:"architecture,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	PowerModes   []string          `json:"powerModes,omitempty"`
}

type CandidateReportArtifactSupply struct {
	Image        string   `json:"image,omitempty"`
	Architecture string   `json:"architecture,omitempty"`
	Files        []string `json:"files,omitempty"`
}

// CandidateArtifactCapability freezes derivation facts and, for RKNN artifacts
// only, the declared input tensors. Preprocessor artifacts define every other
// format's inputs. Conversion provenance remains in ModelSpec content.
type CandidateArtifactCapability struct {
	TargetPlatform     string                    `json:"targetPlatform,omitempty"`
	DeclaredBatchSizes []int32                   `json:"declaredBatchSizes,omitempty"`
	Tensors            []modelcompat.InputTensor `json:"tensors,omitempty"`
}

// CandidateReportRuntimePackage is one image-owned package fact from the
// exact runtime declaration producer.
type CandidateReportRuntimePackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// CandidateReportHostContract records the expected mutable host ABI, device
// visibility, and Kubernetes allocation claims separately from image-owned
// runtime facts. It is not a Run-time observation.
type CandidateReportHostContract struct {
	Libraries  []CandidateReportHostLibrary `json:"libraries"`
	Driver     CandidateReportHostDriver    `json:"driver"`
	Devices    []CandidateReportHostDevice  `json:"devices"`
	Platform   *CandidateReportHostPlatform `json:"platform,omitempty"`
	Allocation CandidateReportNPUAllocation `json:"allocation"`
}

type CandidateReportHostLibrary struct {
	Name          string `json:"name"`
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
}

type CandidateReportHostDriver struct {
	Name        string `json:"name"`
	VersionPath string `json:"versionPath"`
	Version     string `json:"version"`
}

type CandidateReportHostDevice struct {
	Path string `json:"path"`
}

type CandidateReportHostPlatform struct {
	CompatiblePath  string `json:"compatiblePath"`
	CompatibleToken string `json:"compatibleToken"`
}

// CandidateReportNPUAllocation states what Kubernetes requests and whether
// exclusivity is claimed. An empty ResourceName explicitly means no extended
// resource allocation is configured.
type CandidateReportNPUAllocation struct {
	ResourceName       string `json:"resourceName"`
	ExclusivityClaimed bool   `json:"exclusivityClaimed"`
}

// CandidateReportRuntimeAccess records Kubernetes delivery requirements
// separately from the physical host contract.
type CandidateReportRuntimeAccess struct {
	Privileged         bool              `json:"privileged"`
	SupplementalGroups []int64           `json:"supplementalGroups,omitempty"`
	Resources          map[string]string `json:"resources,omitempty"`
	GPUUUID            string            `json:"gpuUUID,omitempty"`
}

type CandidateRuntimeDelivery struct {
	ArtifactCapability *CandidateArtifactCapability    `json:"artifactCapability,omitempty"`
	RuntimePackages    []CandidateReportRuntimePackage `json:"runtimePackages,omitempty"`
	HostContract       *CandidateReportHostContract    `json:"hostContract,omitempty"`
	RuntimeAccess      *CandidateReportRuntimeAccess   `json:"runtimeAccess,omitempty"`
}

// RuntimeDelivery returns only the concrete material required to execute the
// selected model variant. It is copied into RunConfig so execution no longer
// depends on a retained candidate-report document.
func (r CandidateReport) RuntimeDelivery() CandidateRuntimeDelivery {
	delivery := CandidateRuntimeDelivery{}
	if r.ArtifactCapability != nil {
		capability := *r.ArtifactCapability
		capability.DeclaredBatchSizes = append([]int32(nil), capability.DeclaredBatchSizes...)
		capability.Tensors = modelcompat.CloneInputTensors(capability.Tensors)
		delivery.ArtifactCapability = &capability
	}
	delivery.RuntimePackages = append([]CandidateReportRuntimePackage(nil), r.RuntimePackages...)
	if r.HostContract != nil {
		contract := *r.HostContract
		contract.Libraries = append([]CandidateReportHostLibrary(nil), contract.Libraries...)
		contract.Devices = append([]CandidateReportHostDevice(nil), contract.Devices...)
		if contract.Platform != nil {
			platform := *contract.Platform
			contract.Platform = &platform
		}
		delivery.HostContract = &contract
	}
	if r.RuntimeAccess != nil {
		access := *r.RuntimeAccess
		access.SupplementalGroups = append([]int64(nil), access.SupplementalGroups...)
		access.Resources = maps.Clone(r.RuntimeAccess.Resources)
		delivery.RuntimeAccess = &access
	}
	return delivery
}

func validateArtifactCapability(capability *CandidateArtifactCapability) error {
	if capability == nil {
		return errors.New("artifact capability is required")
	}
	if len(capability.Tensors) != 0 {
		if err := modelcompat.ValidateInputTensors(capability.Tensors); err != nil {
			return err
		}
	}
	if capability.TargetPlatform == "" && len(capability.DeclaredBatchSizes) == 0 {
		return nil
	}
	if strings.TrimSpace(capability.TargetPlatform) == "" ||
		strings.TrimSpace(capability.TargetPlatform) != capability.TargetPlatform ||
		len(capability.DeclaredBatchSizes) == 0 {
		return errors.New("artifact target platform and declared batch sizes must be supplied together")
	}
	seen := make(map[int32]struct{}, len(capability.DeclaredBatchSizes))
	for _, batch := range capability.DeclaredBatchSizes {
		if batch < 1 {
			return errors.New("artifact declared batch sizes must be positive")
		}
		if _, duplicate := seen[batch]; duplicate {
			return errors.New("artifact declared batch sizes must be unique")
		}
		seen[batch] = struct{}{}
	}
	return nil
}

type CandidateReportReason struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

// DecodeCandidateReport decodes one current JSON report.
func DecodeCandidateReport(payload []byte) (CandidateReport, error) {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
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
	if err := validateCandidateReportGeneration(payload, report); err != nil {
		return CandidateReport{}, err
	}
	if report.Scope != candidateReportScope {
		return CandidateReport{}, fmt.Errorf("unsupported candidate report scope %q", report.Scope)
	}
	if err := validateInternalCandidateReport(report); err != nil {
		return CandidateReport{}, err
	}
	return report, nil
}

func validateCandidateReportGeneration(payload []byte, report CandidateReport) error {
	if report.SchemaVersion != CandidateReportSchemaVersion {
		return fmt.Errorf("unsupported candidate report schemaVersion %q", report.SchemaVersion)
	}
	var rawFields struct {
		Selection map[string]json.RawMessage `json:"selection"`
	}
	if err := json.Unmarshal(payload, &rawFields); err != nil {
		return fmt.Errorf("inspect candidate report fields: %w", err)
	}
	rawActuator, actuatorPresent := rawFields.Selection["actuator"]
	if actuatorPresent {
		var actuator *ActuatorTarget
		if err := json.Unmarshal(rawActuator, &actuator); err != nil {
			return fmt.Errorf("decode candidate report selection.actuator: %w", err)
		}
		if actuator == nil {
			return errors.New("candidate report selection.actuator must be an object when present")
		}
	}
	return nil
}

func validateInternalCandidateReport(report CandidateReport) error {
	required := map[string]string{
		"ModelSpec name":                      report.Inputs.ModelSpec.Name,
		"ModelSpec UID":                       report.Inputs.ModelSpec.UID,
		"ModelSpec resourceVersion":           report.Inputs.ModelSpec.ResourceVersion,
		"DeviceClass name":                    report.Inputs.DeviceClass.Name,
		"DeviceClass UID":                     report.Inputs.DeviceClass.UID,
		"DeviceClass resourceVersion":         report.Inputs.DeviceClass.ResourceVersion,
		"runtime declaration namespace":       report.Inputs.RuntimeDeclaration.Namespace,
		"runtime declaration name":            report.Inputs.RuntimeDeclaration.Name,
		"runtime declaration UID":             report.Inputs.RuntimeDeclaration.UID,
		"runtime declaration resourceVersion": report.Inputs.RuntimeDeclaration.ResourceVersion,
		"runtime declaration key":             report.Inputs.RuntimeDeclaration.Key,
		"runtime declaration image":           report.Inputs.RuntimeDeclaration.Image,
		"selection executionPath":             report.Selection.ExecutionPath,
		"selection artifact":                  report.Selection.Artifact,
		"selection powerMode":                 report.Selection.PowerMode,
		"DeviceClass architecture":            report.DeviceClassFacts.Architecture,
		"artifact supply image":               report.ArtifactSupply.Image,
		"artifact supply architecture":        report.ArtifactSupply.Architecture,
		"artifact supply files":               strings.Join(report.ArtifactSupply.Files, ","),
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
	if report.Selection.PowerMode == FixedPowerMode {
		if report.Selection.Actuator != nil {
			return errors.New("fixed candidate selection cannot contain an actuator target")
		}
	} else {
		if report.Selection.Actuator == nil {
			return errors.New("actuated candidate selection requires an actuator target")
		}
		if err := report.Selection.Actuator.Validate(); err != nil {
			return fmt.Errorf("validate candidate actuator target: %w", err)
		}
	}
	if report.Verdict == "Compatible" && len(report.Reasons) != 0 {
		return errors.New("compatible candidate report must not contain reasons")
	}
	if err := validateCandidateRuntimePackages(report.RuntimePackages); err != nil {
		return err
	}
	if report.Firstfloor != nil {
		if err := modelcompat.ValidateFirstfloorRuntimeSettings(report.Firstfloor); err != nil {
			return err
		}
	}
	if report.HostContract != nil {
		if err := validateCandidateHostContract(*report.HostContract); err != nil {
			return err
		}
	}
	if err := validateArtifactCapability(report.ArtifactCapability); err != nil {
		return fmt.Errorf("candidate report artifact capability: %w", err)
	}
	if report.HostContract != nil && report.RuntimeAccess == nil {
		return errors.New("candidate report host contract requires runtime access")
	}
	if report.RuntimeAccess != nil {
		if err := validateCandidateRuntimeAccess(*report.RuntimeAccess); err != nil {
			return err
		}
	}
	return nil
}

func validateCandidateRuntimePackages(packages []CandidateReportRuntimePackage) error {
	seen := make(map[string]struct{}, len(packages))
	for _, pkg := range packages {
		if strings.TrimSpace(pkg.Name) == "" || strings.TrimSpace(pkg.Name) != pkg.Name ||
			strings.TrimSpace(pkg.Version) == "" || strings.TrimSpace(pkg.Version) != pkg.Version {
			return errors.New("candidate report runtime package name and version are required without surrounding whitespace")
		}
		if _, duplicate := seen[pkg.Name]; duplicate {
			return fmt.Errorf("candidate report duplicate runtime package %q", pkg.Name)
		}
		seen[pkg.Name] = struct{}{}
	}
	return nil
}

func validateCandidateHostContract(contract CandidateReportHostContract) error {
	if len(contract.Libraries) == 0 || len(contract.Devices) == 0 {
		return errors.New("candidate report host contract requires libraries and devices")
	}
	seenLibraries := make(map[string]struct{}, len(contract.Libraries))
	for _, library := range contract.Libraries {
		if strings.TrimSpace(library.Name) == "" || strings.TrimSpace(library.Name) != library.Name {
			return errors.New("candidate report host library name is required without surrounding whitespace")
		}
		if !validArtifactPath(library.HostPath) || !validArtifactPath(library.ContainerPath) {
			return errors.New("candidate report host library paths must be clean absolute paths")
		}
		if _, duplicate := seenLibraries[library.Name]; duplicate {
			return fmt.Errorf("candidate report duplicate host library %q", library.Name)
		}
		seenLibraries[library.Name] = struct{}{}
	}
	if strings.TrimSpace(contract.Driver.Name) == "" || strings.TrimSpace(contract.Driver.Name) != contract.Driver.Name ||
		strings.TrimSpace(contract.Driver.Version) == "" || strings.TrimSpace(contract.Driver.Version) != contract.Driver.Version ||
		!validArtifactPath(contract.Driver.VersionPath) {
		return errors.New("candidate report host driver name, version path, and version are required")
	}
	seenDevices := make(map[string]struct{}, len(contract.Devices))
	for _, device := range contract.Devices {
		if !validArtifactPath(device.Path) {
			return errors.New("candidate report host device must be a clean absolute path")
		}
		if _, duplicate := seenDevices[device.Path]; duplicate {
			return fmt.Errorf("candidate report duplicate host device %q", device.Path)
		}
		seenDevices[device.Path] = struct{}{}
	}
	if contract.Platform != nil {
		if !validArtifactPath(contract.Platform.CompatiblePath) ||
			strings.TrimSpace(contract.Platform.CompatibleToken) == "" ||
			strings.TrimSpace(contract.Platform.CompatibleToken) != contract.Platform.CompatibleToken {
			return errors.New("candidate report host platform compatible path and token are required")
		}
	}
	resourceName := contract.Allocation.ResourceName
	if strings.TrimSpace(resourceName) != resourceName {
		return errors.New("candidate report NPU resource name has surrounding whitespace")
	}
	if resourceName != "" && (!strings.Contains(resourceName, "/") || len(k8svalidation.IsQualifiedName(resourceName)) != 0) {
		return errors.New("candidate report NPU resource name must be a qualified extended-resource name")
	}
	if contract.Allocation.ExclusivityClaimed && resourceName == "" {
		return errors.New("candidate report cannot claim NPU exclusivity without a Kubernetes resource")
	}
	return nil
}

// ValidateRuntimeAccess validates declared accelerator delivery at the boundary.
func ValidateRuntimeAccess(contract CandidateReportRuntimeAccess) error {
	return validateCandidateRuntimeAccess(contract)
}

func validateCandidateRuntimeAccess(contract CandidateReportRuntimeAccess) error {
	if strings.TrimSpace(contract.GPUUUID) != contract.GPUUUID ||
		(contract.GPUUUID != "" && (!strings.HasPrefix(contract.GPUUUID, "GPU-") || len(contract.GPUUUID) <= 4)) {
		return errors.New("runtime GPU UUID must name an explicit physical GPU without surrounding whitespace")
	}
	for name, value := range contract.Resources {
		quantity, err := resource.ParseQuantity(value)
		if !strings.Contains(name, "/") || len(k8svalidation.IsQualifiedName(name)) != 0 ||
			err != nil || quantity.Sign() <= 0 {
			return errors.New("runtime resources must have qualified names and positive whole-device quantities")
		}
		count, exact := quantity.AsInt64()
		if !exact || count < 1 {
			return errors.New("runtime resources must allocate whole devices")
		}
	}
	seen := make(map[int64]struct{}, len(contract.SupplementalGroups))
	for _, group := range contract.SupplementalGroups {
		if group < 1 || group > int64(^uint32(0)>>1) {
			return errors.New("candidate report runtime supplemental group must be a positive 32-bit ID")
		}
		if _, duplicate := seen[group]; duplicate {
			return fmt.Errorf("candidate report duplicate runtime supplemental group %d", group)
		}
		seen[group] = struct{}{}
	}
	return nil
}
