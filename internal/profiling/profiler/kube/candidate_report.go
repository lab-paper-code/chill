package kube

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
)

func compatibilityReasonCodes(result modelcompat.Result) string {
	codes := make([]string, len(result.Reasons))
	for index, reason := range result.Reasons {
		codes[index] = string(reason.Code)
	}
	return strings.Join(codes, ",")
}

type candidateReportInput struct {
	Model        *edgev1alpha1.ModelSpec
	DeviceClass  *edgev1alpha1.DeviceClass
	ConfigMap    *corev1.ConfigMap
	ConfigMapKey string
	Path         edgev1alpha1.ModelExecutionPath
	Artifact     edgev1alpha1.ModelArtifact
	PowerMode    string
	DeviceFacts  selectedDeviceFacts
	Declaration  resolvedExecutionDeclaration
	Result       modelcompat.Result
}

type builtCandidateReport struct {
	Bytes []byte
}

func buildCandidateReport(input candidateReportInput) (builtCandidateReport, error) {
	reasons := make([]profilingrun.CandidateReportReason, len(input.Result.Reasons))
	for index, reason := range input.Result.Reasons {
		reasons[index] = profilingrun.CandidateReportReason{Kind: string(reason.Kind), Code: string(reason.Code)}
	}
	hostContract := reportHostContract(input.Declaration.Host)
	report := profilingrun.CandidateReport{
		SchemaVersion: profilingrun.CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: profilingrun.CandidateReportInputs{
			ModelSpec: profilingrun.CandidateReportObjectInput{
				Name: input.Model.Name, UID: string(input.Model.UID), ResourceVersion: input.Model.ResourceVersion,
			},
			DeviceClass: profilingrun.CandidateReportObjectInput{
				Name: input.DeviceClass.Name, UID: string(input.DeviceClass.UID), ResourceVersion: input.DeviceClass.ResourceVersion,
			},
			RuntimeDeclaration: profilingrun.CandidateReportRuntimeInput{
				Namespace: input.ConfigMap.Namespace, Name: input.ConfigMap.Name, UID: string(input.ConfigMap.UID),
				ResourceVersion: input.ConfigMap.ResourceVersion, Key: input.ConfigMapKey, Image: input.Declaration.Runtime.Image,
			},
		},
		Selection: profilingrun.CandidateReportSelection{
			ExecutionPath: input.Path.Name, Artifact: input.Artifact.Name,
			PowerMode: input.PowerMode,
			Actuator:  cloneActuatorTarget(input.DeviceFacts.selectedActuator),
		},
		DeviceClassFacts: profilingrun.CandidateReportDeviceClassFacts{
			Architecture: input.DeviceClass.Spec.Architecture,
			NodeSelector: copyStringMap(input.DeviceFacts.nodeSelector),
			PowerModes:   append([]string(nil), input.DeviceFacts.powerModes...),
		},
		ArtifactSupply: profilingrun.CandidateReportArtifactSupply{
			Image: input.Artifact.Source.Image, Architecture: input.Artifact.Source.Architecture,
			Path: input.Artifact.Source.Path,
		},
		ArtifactCapability:  reportArtifactCapability(input.Artifact),
		RuntimePackages:     reportRuntimePackages(input.Declaration.Runtime.Packages),
		HostContract:        hostContract,
		RuntimeAccess:       reportRuntimeAccess(input.Declaration.Access),
		MetricObserverImage: input.Declaration.MetricObserverImage,
		Verdict:             string(input.Result.Verdict), Reasons: reasons,
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return builtCandidateReport{}, fmt.Errorf("encode static candidate report: %w", err)
	}
	return builtCandidateReport{Bytes: payload}, nil
}

func cloneActuatorTarget(source *profilingrun.ActuatorTarget) *profilingrun.ActuatorTarget {
	if source == nil {
		return nil
	}
	clone := *source
	if source.JetsonNVPModel != nil {
		target := *source.JetsonNVPModel
		clone.JetsonNVPModel = &target
	}
	if source.NvidiaLockedGPUClock != nil {
		target := *source.NvidiaLockedGPUClock
		if source.NvidiaLockedGPUClock.Mode != nil {
			mode := *source.NvidiaLockedGPUClock.Mode
			target.Mode = &mode
		}
		clone.NvidiaLockedGPUClock = &target
	}
	return &clone
}

func reportArtifactCapability(
	artifact edgev1alpha1.ModelArtifact,
) *profilingrun.CandidateArtifactCapability {
	derivation := artifact.Derivation
	if derivation == nil {
		return nil
	}
	capability := &profilingrun.CandidateArtifactCapability{
		TargetPlatform:     derivation.TargetPlatform,
		DeclaredBatchSizes: append([]int32(nil), derivation.DeclaredBatchSizes...),
	}
	if artifact.Input != nil {
		capability.InputSampleShape = append([]int32(nil), artifact.Input.InputSampleShape...)
	}
	return capability
}

func reportRuntimePackages(source []runtimePackage) []profilingrun.CandidateReportRuntimePackage {
	if len(source) == 0 {
		return nil
	}
	result := make([]profilingrun.CandidateReportRuntimePackage, len(source))
	for index, pkg := range source {
		result[index] = profilingrun.CandidateReportRuntimePackage{Name: pkg.Name, Version: pkg.Version}
	}
	return result
}

func reportHostContract(source *hostExecutionContract) *profilingrun.CandidateReportHostContract {
	if source == nil {
		return nil
	}
	result := &profilingrun.CandidateReportHostContract{
		Libraries: make([]profilingrun.CandidateReportHostLibrary, len(source.Libraries)),
		Driver: profilingrun.CandidateReportHostDriver{
			Name: source.Driver.Name, VersionPath: source.Driver.VersionPath, Version: source.Driver.Version,
		},
		Devices: make([]profilingrun.CandidateReportHostDevice, len(source.Devices)),
		Allocation: profilingrun.CandidateReportNPUAllocation{
			ResourceName: source.Allocation.ResourceName, ExclusivityClaimed: source.Allocation.ExclusivityClaimed,
		},
	}
	if source.Platform != nil {
		result.Platform = &profilingrun.CandidateReportHostPlatform{
			CompatiblePath:  source.Platform.CompatiblePath,
			CompatibleToken: source.Platform.CompatibleToken,
		}
	}
	for index, library := range source.Libraries {
		result.Libraries[index] = profilingrun.CandidateReportHostLibrary{
			Name: library.Name, HostPath: library.HostPath,
			ContainerPath: library.ContainerPath,
		}
	}
	for index, device := range source.Devices {
		result.Devices[index] = profilingrun.CandidateReportHostDevice{Path: device.Path}
	}
	return result
}

func reportRuntimeAccess(source *runtimeAccessContract) *profilingrun.CandidateReportRuntimeAccess {
	if source == nil {
		return nil
	}
	groups := append([]int64(nil), source.SupplementalGroups...)
	slices.Sort(groups)
	return &profilingrun.CandidateReportRuntimeAccess{
		Privileged: source.Privileged, SupplementalGroups: groups,
	}
}

func copyStringMap(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}
