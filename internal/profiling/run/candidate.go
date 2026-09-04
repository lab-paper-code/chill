package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	CandidateReportSchemaVersion = "chill.dacs.io/modelcompat-report.v1alpha10"
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
	Image        string `json:"image,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	Path         string `json:"path,omitempty"`
}

// CandidateArtifactCapability carries only runtime-consumed facts of one
// derived artifact. Full conversion provenance remains in ModelSpec content.
type CandidateArtifactCapability struct {
	TargetPlatform     string  `json:"targetPlatform"`
	DeclaredBatchSizes []int32 `json:"declaredBatchSizes"`
	InputSampleShape   []int32 `json:"inputSampleShape"`
}

// CandidateReportRuntimePackage is one image-owned package fact from the
// exact runtime declaration producer.
type CandidateReportRuntimePackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// CandidateReportHostContract freezes the expected mutable host ABI, device
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

// CandidateReportRuntimeAccess freezes Kubernetes delivery requirements
// separately from the physical host contract.
type CandidateReportRuntimeAccess struct {
	Privileged         bool    `json:"privileged"`
	SupplementalGroups []int64 `json:"supplementalGroups,omitempty"`
}

// VerifiedCandidateReport is constructed only after one decoded report has
// been matched semantically to a RunConfig. Downstream delivery code
// consumes its copied projection instead of decoding or validating it again.
type VerifiedCandidateReport struct {
	report CandidateReport
}

func (r VerifiedCandidateReport) Record() CandidateReport {
	result := r.report
	result.DeviceClassFacts.NodeSelector = cloneStringMap(r.report.DeviceClassFacts.NodeSelector)
	result.DeviceClassFacts.PowerModes = append([]string(nil), r.report.DeviceClassFacts.PowerModes...)
	result.Reasons = append([]CandidateReportReason(nil), r.report.Reasons...)
	result.RuntimePackages = append([]CandidateReportRuntimePackage(nil), r.report.RuntimePackages...)
	result.HostContract = cloneCandidateHostContract(r.report.HostContract)
	if r.report.ArtifactCapability != nil {
		capability := *r.report.ArtifactCapability
		capability.DeclaredBatchSizes = append([]int32(nil), r.report.ArtifactCapability.DeclaredBatchSizes...)
		capability.InputSampleShape = append([]int32(nil), r.report.ArtifactCapability.InputSampleShape...)
		result.ArtifactCapability = &capability
	}
	if r.report.RuntimeAccess != nil {
		access := *r.report.RuntimeAccess
		access.SupplementalGroups = append([]int64(nil), r.report.RuntimeAccess.SupplementalGroups...)
		result.RuntimeAccess = &access
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

type CandidateRuntimeDelivery struct {
	ArtifactCapability *CandidateArtifactCapability    `json:"artifactCapability,omitempty"`
	RuntimePackages    []CandidateReportRuntimePackage `json:"runtimePackages,omitempty"`
	HostContract       *CandidateReportHostContract    `json:"hostContract,omitempty"`
	RuntimeAccess      *CandidateReportRuntimeAccess   `json:"runtimeAccess,omitempty"`
}

func (r VerifiedCandidateReport) RuntimeDelivery() CandidateRuntimeDelivery {
	return r.report.RuntimeDelivery()
}

// RuntimeDelivery returns only the concrete material required to execute the
// selected model variant. It is copied into RunConfig so execution no longer
// depends on a retained candidate-report document.
func (r CandidateReport) RuntimeDelivery() CandidateRuntimeDelivery {
	delivery := CandidateRuntimeDelivery{}
	if r.ArtifactCapability != nil {
		capability := *r.ArtifactCapability
		capability.DeclaredBatchSizes = append([]int32(nil), capability.DeclaredBatchSizes...)
		capability.InputSampleShape = append([]int32(nil), capability.InputSampleShape...)
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
		delivery.RuntimeAccess = &access
	}
	return delivery
}

type CandidateReportReason struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

// DecodeCandidateReport strictly decodes exactly one current JSON object.
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
		"artifact supply path":                report.ArtifactSupply.Path,
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
	if report.HostContract != nil {
		if err := validateCandidateHostContract(*report.HostContract); err != nil {
			return err
		}
	}
	if report.ArtifactCapability != nil {
		if strings.TrimSpace(report.ArtifactCapability.TargetPlatform) == "" ||
			strings.TrimSpace(report.ArtifactCapability.TargetPlatform) != report.ArtifactCapability.TargetPlatform ||
			len(report.ArtifactCapability.DeclaredBatchSizes) == 0 {
			return errors.New("candidate report artifact target platform and declared batch sizes are required")
		}
		for _, declaredBatchSize := range report.ArtifactCapability.DeclaredBatchSizes {
			if declaredBatchSize < 1 {
				return errors.New("candidate report artifact declared batch sizes must be positive")
			}
		}
		for _, dimension := range report.ArtifactCapability.InputSampleShape {
			if dimension < 1 {
				return errors.New("candidate report artifact input shape dimensions must be positive")
			}
		}
	}
	if report.SchemaVersion == CandidateReportSchemaVersion &&
		(report.HostContract == nil) != (report.RuntimeAccess == nil) {
		return errors.New("candidate report host and runtime access contracts must be supplied together")
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

func validateCandidateRuntimeAccess(contract CandidateReportRuntimeAccess) error {
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

func VerifyCandidateReport(payload []byte, config RunConfig) (VerifiedCandidateReport, error) {
	report, err := DecodeCandidateReport(payload)
	if err != nil {
		return VerifiedCandidateReport{}, err
	}
	return VerifyCandidateReportRecord(report, config)
}

func VerifyCandidateReportRecord(report CandidateReport, config RunConfig) (VerifiedCandidateReport, error) {
	if report.SchemaVersion != CandidateReportSchemaVersion {
		return VerifiedCandidateReport{}, fmt.Errorf("unsupported candidate report schemaVersion %q", report.SchemaVersion)
	}
	if err := validateInternalCandidateReport(report); err != nil {
		return VerifiedCandidateReport{}, err
	}
	if config.State.RuntimeFamily == RKNNLiteRuntimeFamily {
		if report.SchemaVersion != CandidateReportSchemaVersion || report.ArtifactCapability == nil {
			return VerifiedCandidateReport{}, errors.New("candidate RKNN artifact capability is required")
		}
		if len(report.ArtifactCapability.InputSampleShape) == 0 {
			return VerifiedCandidateReport{}, errors.New("candidate report artifact input shape is required")
		}
		if !slices.Contains(report.ArtifactCapability.DeclaredBatchSizes, int32(config.Measurement.Batch)) {
			return VerifiedCandidateReport{}, errors.New(
				"candidate RKNN artifact does not declare the measurement batch as a supported batch size",
			)
		}
		if report.HostContract == nil || report.RuntimeAccess == nil {
			return VerifiedCandidateReport{}, errors.New("candidate RKNN host and runtime access contracts are required")
		}
		if report.HostContract.Platform == nil {
			return VerifiedCandidateReport{}, errors.New("candidate RKNN host platform contract is required")
		}
	}
	checks := []struct{ name, want, got string }{
		{"verdict", config.Candidate.Verdict, report.Verdict},
		{"ModelSpec name", config.State.Model, report.Inputs.ModelSpec.Name},
		{"DeviceClass name", config.State.DeviceClass, report.Inputs.DeviceClass.Name},
		{"runtime image", config.Images.Runtime, report.Inputs.RuntimeDeclaration.Image},
		{"execution path", config.Candidate.ExecutionPath, report.Selection.ExecutionPath},
		{"artifact", config.State.Artifact, report.Selection.Artifact},
	}
	checks = append(checks,
		struct{ name, want, got string }{"power mode", config.State.PowerMode, report.Selection.PowerMode},
		struct{ name, want, got string }{"artifact image", config.Images.Artifact, report.ArtifactSupply.Image},
		struct{ name, want, got string }{"artifact supply path", config.State.ArtifactPath, report.ArtifactSupply.Path},
		struct{ name, want, got string }{"DeviceClass architecture", config.TargetNode.Architecture, report.DeviceClassFacts.Architecture},
		struct{ name, want, got string }{"artifact architecture", config.TargetNode.Architecture, report.ArtifactSupply.Architecture},
		struct{ name, want, got string }{"metric-observer image", config.Images.MetricObserver, report.MetricObserverImage},
	)
	for _, check := range checks {
		if check.want != check.got {
			return VerifiedCandidateReport{}, fmt.Errorf("candidate %s mismatch", check.name)
		}
	}
	if !reflect.DeepEqual(config.Candidate.Inputs, report.Inputs) {
		return VerifiedCandidateReport{}, errors.New("candidate source objects do not match RunConfig")
	}
	if !reflect.DeepEqual(config.State.HostContract, report.HostContract) {
		return VerifiedCandidateReport{}, errors.New("candidate host contract does not match RunConfig")
	}
	if config.Actuation == nil {
		if report.Selection.Actuator != nil {
			return VerifiedCandidateReport{}, errors.New("candidate actuator target does not match fixed RunConfig")
		}
	} else if report.Selection.Actuator == nil ||
		!actuatorTargetsEqual(*report.Selection.Actuator, config.Actuation.Actuator) {
		return VerifiedCandidateReport{}, errors.New("candidate actuator target does not match RunConfig")
	}
	return VerifiedCandidateReport{report: report}, nil
}

func actuatorTargetsEqual(left, right ActuatorTarget) bool {
	if left.Kind != right.Kind {
		return false
	}
	switch left.Kind {
	case ActuatorKindJetsonNVPModel:
		return left.JetsonNVPModel != nil && right.JetsonNVPModel != nil &&
			left.JetsonNVPModel.ID == right.JetsonNVPModel.ID &&
			left.NvidiaLockedGPUClock == nil && right.NvidiaLockedGPUClock == nil
	case ActuatorKindNvidiaLockedGPUClock:
		if left.NvidiaLockedGPUClock == nil || right.NvidiaLockedGPUClock == nil ||
			left.JetsonNVPModel != nil || right.JetsonNVPModel != nil {
			return false
		}
		leftClock := left.NvidiaLockedGPUClock
		rightClock := right.NvidiaLockedGPUClock
		if leftClock.MinMHz != rightClock.MinMHz || leftClock.MaxMHz != rightClock.MaxMHz ||
			(leftClock.Mode == nil) != (rightClock.Mode == nil) {
			return false
		}
		return leftClock.Mode == nil || *leftClock.Mode == *rightClock.Mode
	default:
		return false
	}
}

func ValidateCandidateReport(payload []byte, config RunConfig) error {
	_, err := VerifyCandidateReport(payload, config)
	return err
}
