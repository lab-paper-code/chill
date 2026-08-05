package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
)

func catalogContentDigest(name, uid string, generation int64, spec any) (string, error) {
	return canonicalDigest(struct {
		Name       string `json:"name"`
		UID        string `json:"uid"`
		Generation int64  `json:"generation"`
		Spec       any    `json:"spec"`
	}{Name: name, UID: uid, Generation: generation, Spec: spec})
}

func canonicalDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func compatibilityReasonCodes(result modelcompat.Result) string {
	codes := make([]string, len(result.Reasons))
	for index, reason := range result.Reasons {
		codes[index] = string(reason.Code)
	}
	return strings.Join(codes, ",")
}

type candidateReportInput struct {
	Model             *edgev1alpha1.ModelSpec
	DeviceClass       *edgev1alpha1.DeviceClass
	ConfigMap         *corev1.ConfigMap
	ConfigMapKey      string
	ModelDigest       string
	DeviceDigest      string
	DeclarationDigest string
	Path              edgev1alpha1.ModelExecutionPath
	Artifact          edgev1alpha1.ModelArtifact
	PowerMode         string
	DeviceFacts       selectedDeviceFacts
	Declaration       resolvedExecutionDeclaration
	Result            modelcompat.Result
}

func buildCandidateReport(input candidateReportInput) ([]byte, error) {
	reasons := make([]profilingrun.CandidateReportReason, len(input.Result.Reasons))
	for index, reason := range input.Result.Reasons {
		reasons[index] = profilingrun.CandidateReportReason{Kind: string(reason.Kind), Code: string(reason.Code)}
	}
	report := profilingrun.CandidateReport{
		SchemaVersion: profilingrun.CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: profilingrun.CandidateReportInputs{
			ModelSpec: profilingrun.CandidateReportObjectInput{
				Name: input.Model.Name, UID: string(input.Model.UID), ResourceVersion: input.Model.ResourceVersion,
				ContentDigest: input.ModelDigest,
			},
			DeviceClass: profilingrun.CandidateReportObjectInput{
				Name: input.DeviceClass.Name, UID: string(input.DeviceClass.UID), ResourceVersion: input.DeviceClass.ResourceVersion,
				ContentDigest: input.DeviceDigest,
			},
			RuntimeDeclaration: profilingrun.CandidateReportRuntimeInput{
				Namespace: input.ConfigMap.Namespace, Name: input.ConfigMap.Name, UID: string(input.ConfigMap.UID),
				ResourceVersion: input.ConfigMap.ResourceVersion, Key: input.ConfigMapKey,
				ContentDigest: input.DeclarationDigest, Image: input.Declaration.Runtime.Image,
			},
		},
		Selection: profilingrun.CandidateReportSelection{
			ExecutionPath: input.Path.Name, Artifact: input.Artifact.Name,
			ArtifactDigest: input.Artifact.Digest, PowerMode: input.PowerMode,
		},
		DeviceClassFacts: profilingrun.CandidateReportDeviceClassFacts{
			Architecture: input.DeviceClass.Spec.Architecture,
			NodeSelector: copyStringMap(input.DeviceFacts.nodeSelector),
			PowerModes:   append([]string(nil), input.DeviceFacts.powerModes...),
		},
		ArtifactSupply: profilingrun.CandidateReportArtifactSupply{
			Image: input.Declaration.Artifact.Image, Architecture: input.Declaration.Artifact.Architecture,
			Path: input.Declaration.Artifact.Path, Digest: input.Declaration.Artifact.Digest,
		},
		RuntimePackages:     reportRuntimePackages(input.Declaration.Runtime.Packages),
		HostContract:        reportHostContract(input.Declaration.Host),
		MetricObserverImage: input.Declaration.MetricObserverImage,
		Verdict:             string(input.Result.Verdict), Reasons: reasons,
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("encode static candidate report: %w", err)
	}
	return payload, nil
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
	for index, library := range source.Libraries {
		result.Libraries[index] = profilingrun.CandidateReportHostLibrary{
			Name: library.Name, HostPath: library.HostPath,
			ContainerPath: library.ContainerPath, Digest: library.Digest,
		}
	}
	for index, device := range source.Devices {
		result.Devices[index] = profilingrun.CandidateReportHostDevice{Path: device.Path}
	}
	return result
}

func copyStringMap(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}
