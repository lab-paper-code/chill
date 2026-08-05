package profilingrun

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	candidateReportSchemaVersionV1Alpha2 = "chill.dacs.io/modelcompat-report.v1alpha2"
	candidateReportSchemaVersionV1Alpha3 = "chill.dacs.io/modelcompat-report.v1alpha3"
	candidateReportSchemaVersionV1Alpha4 = "chill.dacs.io/modelcompat-report.v1alpha4"
	candidateReportSchemaVersionV1Alpha5 = "chill.dacs.io/modelcompat-report.v1alpha5"
	CandidateReportSchemaVersion         = "chill.dacs.io/modelcompat-report.v1alpha6"
	candidateReportScope                 = "StaticCompatibility"
	candidateHostContractSchemaVersion   = "chill.dacs.io/accelerator-host-contract.v1alpha1"
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
	ExecutionPath  string          `json:"executionPath"`
	Artifact       string          `json:"artifact"`
	ArtifactDigest string          `json:"artifactDigest"`
	PowerMode      string          `json:"powerMode,omitempty"`
	Actuator       *ActuatorTarget `json:"actuator,omitempty"`
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

// CandidateArtifactCapability carries only runtime-consumed facts of one
// derived artifact. Full conversion provenance remains in ModelSpec content.
type CandidateArtifactCapability struct {
	TargetPlatform string  `json:"targetPlatform"`
	FixedBatch     int32   `json:"fixedBatch"`
	InputShape     []int32 `json:"inputShape,omitempty"`
	InputLayout    string  `json:"inputLayout,omitempty"`
	InputDataType  string  `json:"inputDataType,omitempty"`
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
	Digest        string `json:"digest"`
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
// separately from the physical host contract digest.
type CandidateReportRuntimeAccess struct {
	Privileged         bool    `json:"privileged"`
	SupplementalGroups []int64 `json:"supplementalGroups,omitempty"`
}

// VerifiedCandidateReport is constructed only after exact report bytes have
// been matched to one frozen intent. Downstream delivery code consumes its
// copied projection instead of decoding or re-validating the same bytes.
type VerifiedCandidateReport struct {
	report CandidateReport
}

type CandidateRuntimeDelivery struct {
	ArtifactCapability *CandidateArtifactCapability    `json:"artifactCapability,omitempty"`
	RuntimePackages    []CandidateReportRuntimePackage `json:"runtimePackages,omitempty"`
	HostContract       *CandidateReportHostContract    `json:"hostContract,omitempty"`
	RuntimeAccess      *CandidateReportRuntimeAccess   `json:"runtimeAccess,omitempty"`
}

func (r VerifiedCandidateReport) RuntimeDelivery() CandidateRuntimeDelivery {
	delivery := CandidateRuntimeDelivery{}
	if r.report.ArtifactCapability != nil {
		capability := *r.report.ArtifactCapability
		capability.InputShape = append([]int32(nil), capability.InputShape...)
		delivery.ArtifactCapability = &capability
	}
	delivery.RuntimePackages = append([]CandidateReportRuntimePackage(nil), r.report.RuntimePackages...)
	if r.report.HostContract != nil {
		contract := *r.report.HostContract
		contract.Libraries = append([]CandidateReportHostLibrary(nil), contract.Libraries...)
		contract.Devices = append([]CandidateReportHostDevice(nil), contract.Devices...)
		if contract.Platform != nil {
			platform := *contract.Platform
			contract.Platform = &platform
		}
		delivery.HostContract = &contract
	}
	if r.report.RuntimeAccess != nil {
		access := *r.report.RuntimeAccess
		access.SupplementalGroups = append([]int64(nil), access.SupplementalGroups...)
		delivery.RuntimeAccess = &access
	}
	return delivery
}

// CanonicalCandidateHostContract returns the order-normalized identity input
// without deciding whether the contract is admissible.
func CanonicalCandidateHostContract(contract CandidateReportHostContract) ([]byte, error) {
	canonical := contract
	canonical.Libraries = append([]CandidateReportHostLibrary(nil), contract.Libraries...)
	slices.SortFunc(canonical.Libraries, func(left, right CandidateReportHostLibrary) int {
		if value := cmp.Compare(left.Name, right.Name); value != 0 {
			return value
		}
		if value := cmp.Compare(left.HostPath, right.HostPath); value != 0 {
			return value
		}
		return cmp.Compare(left.ContainerPath, right.ContainerPath)
	})
	canonical.Devices = append([]CandidateReportHostDevice(nil), contract.Devices...)
	slices.SortFunc(canonical.Devices, func(left, right CandidateReportHostDevice) int {
		return cmp.Compare(left.Path, right.Path)
	})
	return json.Marshal(struct {
		SchemaVersion string                      `json:"schemaVersion"`
		Contract      CandidateReportHostContract `json:"contract"`
	}{SchemaVersion: candidateHostContractSchemaVersion, Contract: canonical})
}

// CandidateHostContractDigest identifies only the physical host contract. A
// nil contract returns an empty digest so CPU-only state retains its existing
// identity material.
func CandidateHostContractDigest(contract *CandidateReportHostContract) (string, error) {
	if contract == nil {
		return "", nil
	}
	if err := validateCandidateHostContract(*contract); err != nil {
		return "", err
	}
	return digestCanonicalCandidateHostContract(*contract)
}

func digestCanonicalCandidateHostContract(contract CandidateReportHostContract) (string, error) {
	payload, err := CanonicalCandidateHostContract(contract)
	if err != nil {
		return "", fmt.Errorf("canonicalize candidate host contract: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
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
	if report.SchemaVersion != CandidateReportSchemaVersion &&
		report.SchemaVersion != candidateReportSchemaVersionV1Alpha5 &&
		report.SchemaVersion != candidateReportSchemaVersionV1Alpha4 &&
		report.SchemaVersion != candidateReportSchemaVersionV1Alpha3 &&
		report.SchemaVersion != candidateReportSchemaVersionV1Alpha2 {
		return fmt.Errorf("unsupported candidate report schemaVersion %q", report.SchemaVersion)
	}
	var rawFields struct {
		Selection map[string]json.RawMessage `json:"selection"`
	}
	if err := json.Unmarshal(payload, &rawFields); err != nil {
		return fmt.Errorf("inspect candidate report fields: %w", err)
	}
	rawActuator, actuatorPresent := rawFields.Selection["actuator"]
	if actuatorPresent && bytes.Equal(bytes.TrimSpace(rawActuator), []byte("null")) {
		return errors.New("candidate report selection.actuator must be an object when present")
	}
	if report.SchemaVersion == candidateReportSchemaVersionV1Alpha2 &&
		(len(report.RuntimePackages) != 0 || report.HostContract != nil ||
			report.ArtifactCapability != nil || report.RuntimeAccess != nil) {
		return errors.New("v1alpha2 candidate report cannot contain runtime packages or a host contract")
	}
	if report.SchemaVersion == candidateReportSchemaVersionV1Alpha3 &&
		(report.ArtifactCapability != nil || report.RuntimeAccess != nil ||
			(report.HostContract != nil && report.HostContract.Platform != nil)) {
		return errors.New("v1alpha3 candidate report cannot contain artifact capability, host platform, or runtime access")
	}
	if report.SchemaVersion == candidateReportSchemaVersionV1Alpha4 && report.ArtifactCapability != nil &&
		(len(report.ArtifactCapability.InputShape) != 0 || report.ArtifactCapability.InputLayout != "" ||
			report.ArtifactCapability.InputDataType != "") {
		return errors.New("v1alpha4 candidate report cannot contain an artifact input contract")
	}
	if report.SchemaVersion != CandidateReportSchemaVersion && actuatorPresent {
		return errors.New("retained candidate report cannot contain a selected actuator")
	}
	if report.SchemaVersion != CandidateReportSchemaVersion && report.Selection.PowerMode != FixedPowerMode {
		return errors.New("retained candidate report cannot select an actuated power mode")
	}
	return nil
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
			report.ArtifactCapability.FixedBatch < 1 {
			return errors.New("candidate report artifact target platform and fixed batch are required")
		}
		for _, dimension := range report.ArtifactCapability.InputShape {
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
		if !digestPattern.MatchString(library.Digest) {
			return errors.New("candidate report host library digest must be a canonical SHA-256 digest")
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

func VerifyCandidateReport(payload []byte, intent Intent) (VerifiedCandidateReport, error) {
	sum := sha256.Sum256(payload)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != intent.Candidate.ReportDigest {
		return VerifiedCandidateReport{}, fmt.Errorf("candidate report digest mismatch: %s", actual)
	}
	report, err := DecodeCandidateReport(payload)
	if err != nil {
		return VerifiedCandidateReport{}, err
	}
	if intent.State.RuntimeFamily == RKNNLiteRuntimeFamily {
		if (report.SchemaVersion != CandidateReportSchemaVersion &&
			report.SchemaVersion != candidateReportSchemaVersionV1Alpha5) ||
			report.ArtifactCapability == nil ||
			report.ArtifactCapability.FixedBatch != int32(intent.Measurement.Batch) ||
			len(report.ArtifactCapability.InputShape) == 0 ||
			report.ArtifactCapability.InputShape[0] != report.ArtifactCapability.FixedBatch ||
			report.ArtifactCapability.InputLayout != RKNNLiteInputLayout ||
			report.ArtifactCapability.InputDataType != RKNNLiteInputDataType {
			return VerifiedCandidateReport{}, errors.New("candidate RKNN artifact capability does not match the measurement batch")
		}
		if report.HostContract == nil || report.RuntimeAccess == nil {
			return VerifiedCandidateReport{}, errors.New("candidate RKNN host and runtime access contracts are required")
		}
		if report.HostContract.Platform == nil {
			return VerifiedCandidateReport{}, errors.New("candidate RKNN host platform contract is required")
		}
	}
	hostContractDigest := ""
	if report.HostContract != nil {
		hostContractDigest, err = digestCanonicalCandidateHostContract(*report.HostContract)
		if err != nil {
			return VerifiedCandidateReport{}, err
		}
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
		{"host contract digest", intent.State.HostContractDigest, hostContractDigest},
	}
	checks = append(checks,
		struct{ name, want, got string }{"power mode", intent.State.PowerMode, report.Selection.PowerMode},
		struct{ name, want, got string }{"artifact image", intent.Images.Artifact, report.ArtifactSupply.Image},
		struct{ name, want, got string }{"artifact supply path", intent.State.ArtifactPath, report.ArtifactSupply.Path},
		struct{ name, want, got string }{"artifact supply digest", intent.State.ArtifactDigest, report.ArtifactSupply.Digest},
		struct{ name, want, got string }{"DeviceClass architecture", intent.TargetNode.Architecture, report.DeviceClassFacts.Architecture},
		struct{ name, want, got string }{"artifact architecture", intent.TargetNode.Architecture, report.ArtifactSupply.Architecture},
		struct{ name, want, got string }{"metric-observer image", intent.Images.MetricObserver, report.MetricObserverImage},
	)
	for _, check := range checks {
		if check.want != check.got {
			return VerifiedCandidateReport{}, fmt.Errorf("candidate %s mismatch", check.name)
		}
	}
	if intent.Actuation == nil {
		if report.Selection.Actuator != nil {
			return VerifiedCandidateReport{}, errors.New("candidate actuator target does not match fixed intent")
		}
	} else if report.Selection.Actuator == nil ||
		!actuatorTargetsEqual(*report.Selection.Actuator, intent.Actuation.Actuator) {
		return VerifiedCandidateReport{}, errors.New("candidate actuator target does not match Run intent")
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

func ValidateCandidateReport(payload []byte, intent Intent) error {
	_, err := VerifyCandidateReport(payload, intent)
	return err
}
