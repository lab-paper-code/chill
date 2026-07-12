package modelcompat

import "strings"

// Evaluate compares one artifact/path requirement with stable DeviceClass and
// accepted runtime-image declaration facts. Structurally malformed API input
// is rejected by the caller before this function is invoked.
func Evaluate(input Input) Result {
	var reasons []Reason
	formatPresent := strings.TrimSpace(input.Artifact.Format) != ""
	requiredFamilyPresent := strings.TrimSpace(input.Path.RuntimeFamily) != ""
	requiredBackendPresent := strings.TrimSpace(input.Path.Backend) != ""
	deviceArchitecturePresent := strings.TrimSpace(input.DeviceClass.Architecture) != ""

	if !formatPresent {
		reasons = addReason(reasons, unknown(ReasonArtifactFormatMissing))
	}
	if !requiredFamilyPresent {
		reasons = addReason(reasons, unknown(ReasonRequiredRuntimeFamilyMissing))
	}
	if formatPresent && requiredFamilyPresent &&
		!knownArtifactRuntimeRelation(input.Artifact.Format, input.Path.RuntimeFamily) {
		reasons = addReason(reasons, unknown(ReasonArtifactRuntimeRelationUnknown))
	}

	if input.Runtime == nil {
		reasons = addReason(reasons, unknown(ReasonRuntimeDeclarationMissing))
		return aggregate(reasons)
	}
	if !input.Runtime.Trusted {
		reasons = addReason(reasons, unknown(ReasonRuntimeDeclarationUntrusted))
		return aggregate(reasons)
	}

	if !deviceArchitecturePresent {
		reasons = addReason(reasons, unknown(ReasonDeviceArchitectureMissing))
	}
	if strings.TrimSpace(input.Runtime.Architecture) == "" {
		reasons = addReason(reasons, unknown(ReasonRuntimeArchitectureMissing))
	} else if deviceArchitecturePresent && input.Runtime.Architecture != input.DeviceClass.Architecture {
		reasons = addReason(reasons, conflict(ReasonRuntimeArchitectureMismatch))
	}

	familyComparable := false
	if strings.TrimSpace(input.Runtime.Family) == "" {
		reasons = addReason(reasons, unknown(ReasonRuntimeFamilyMissing))
	} else if requiredFamilyPresent && input.Runtime.Family != input.Path.RuntimeFamily {
		reasons = addReason(reasons, conflict(ReasonRuntimeFamilyMismatch))
	} else if requiredFamilyPresent {
		familyComparable = true
	}

	if !requiredBackendPresent {
		reasons = addReason(reasons, unknown(ReasonRequiredBackendMissing))
	}
	if familyComparable && requiredBackendPresent {
		switch {
		case input.Runtime.Backends == nil:
			reasons = addReason(reasons, unknown(ReasonRuntimeBackendsMissing))
		case !contains(input.Runtime.Backends, input.Path.Backend):
			reasons = addReason(reasons, conflict(ReasonRuntimeBackendUnavailable))
		}
	}

	return aggregate(reasons)
}

func knownArtifactRuntimeRelation(format, runtimeFamily string) bool {
	return format == "onnx" && runtimeFamily == "onnxruntime"
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func unknown(code ReasonCode) Reason {
	return Reason{Kind: ReasonKindUnknown, Code: code}
}

func conflict(code ReasonCode) Reason {
	return Reason{Kind: ReasonKindConflict, Code: code}
}

func addReason(reasons []Reason, reason Reason) []Reason {
	for _, existing := range reasons {
		if existing.Code == reason.Code {
			return reasons
		}
	}
	return append(reasons, reason)
}

func aggregate(reasons []Reason) Result {
	verdict := VerdictCompatible
	for _, reason := range reasons {
		switch reason.Kind {
		case ReasonKindConflict:
			return Result{Verdict: VerdictIncompatible, Reasons: reasons}
		case ReasonKindUnknown:
			verdict = VerdictUnknown
		}
	}
	return Result{Verdict: verdict, Reasons: reasons}
}
