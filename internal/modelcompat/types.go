// Package modelcompat evaluates static model execution-path compatibility.
// It deliberately contains no Kubernetes or public API types.
package modelcompat

// Verdict is the aggregate result of one static compatibility evaluation.
type Verdict string

const (
	VerdictCompatible   Verdict = "Compatible"
	VerdictIncompatible Verdict = "Incompatible"
	VerdictUnknown      Verdict = "Unknown"
)

// ReasonKind distinguishes an explicit trusted conflict from an unknown fact.
type ReasonKind string

const (
	ReasonKindConflict ReasonKind = "Conflict"
	ReasonKindUnknown  ReasonKind = "Unknown"
)

// ReasonCode identifies one local fact that prevented a Compatible verdict.
type ReasonCode string

const (
	ReasonArtifactFormatMissing          ReasonCode = "ArtifactFormatMissing"
	ReasonRequiredRuntimeFamilyMissing   ReasonCode = "RequiredRuntimeFamilyMissing"
	ReasonRequiredBackendMissing         ReasonCode = "RequiredBackendMissing"
	ReasonDeviceArchitectureMissing      ReasonCode = "DeviceArchitectureMissing"
	ReasonArtifactRuntimeRelationUnknown ReasonCode = "ArtifactRuntimeRelationUnknown"
	ReasonRuntimeDeclarationMissing      ReasonCode = "RuntimeDeclarationMissing"
	ReasonRuntimeDeclarationUntrusted    ReasonCode = "RuntimeDeclarationUntrusted"
	ReasonRuntimeArchitectureMissing     ReasonCode = "RuntimeArchitectureMissing"
	ReasonRuntimeArchitectureMismatch    ReasonCode = "RuntimeArchitectureMismatch"
	ReasonRuntimeFamilyMissing           ReasonCode = "RuntimeFamilyMissing"
	ReasonRuntimeFamilyMismatch          ReasonCode = "RuntimeFamilyMismatch"
	ReasonRuntimeBackendsMissing         ReasonCode = "RuntimeBackendsMissing"
	ReasonRuntimeBackendUnavailable      ReasonCode = "RuntimeBackendUnavailable"
)

// Reason is one deterministic local compatibility outcome.
type Reason struct {
	Kind ReasonKind
	Code ReasonCode
}

// ArtifactRequirement contains the artifact fact used by static evaluation.
type ArtifactRequirement struct {
	Format string
}

// ExecutionPathRequirement contains the runtime path required by a ModelSpec.
type ExecutionPathRequirement struct {
	RuntimeFamily string
	Backend       string
}

// DeviceClassInfo contains the stable class fact used by static evaluation.
type DeviceClassInfo struct {
	Architecture string
}

// RuntimeDeclaration contains facts supplied by one immutable runtime image.
// Trusted means the caller accepted the declaration producer and identity; it
// is not a cryptographic-attestation claim.
type RuntimeDeclaration struct {
	Trusted      bool
	Architecture string
	Family       string
	Backends     []string
}

// Input is one exact static compatibility comparison.
type Input struct {
	Artifact    ArtifactRequirement
	Path        ExecutionPathRequirement
	DeviceClass DeviceClassInfo
	Runtime     *RuntimeDeclaration
}

// Result preserves the aggregate verdict and deterministic local reasons.
type Result struct {
	Verdict Verdict
	Reasons []Reason
}
