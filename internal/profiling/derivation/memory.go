package profilederivation

import "strings"

// MemoryBasis identifies the retained observation selected for a cell, not an
// inferred startup bound. It belongs to the derived read view, never raw evidence.
type MemoryBasis struct {
	Observation string              `json:"observation"`
	Evidence    RawEvidenceIdentity `json:"evidence"`
}

// MemoryObservation contains a verified runtime counter. For a window-only
// runtime, verification already binds Bytes to the maximum retained sample.
type MemoryObservation struct {
	Bytes uint64
	Basis MemoryBasis
}

func ObservedMemory(bytes uint64, source, observation string, evidence RawEvidenceIdentity) MemoryObservation {
	if strings.HasSuffix(source, ":measurement-window") {
		observation = "window"
	}
	return MemoryObservation{Bytes: bytes, Basis: MemoryBasis{Observation: observation, Evidence: evidence}}
}

// MaxMemory selects only supplied observations. Callers supply one cell's active
// trials and the same profile's loaded-idle trials, never another batch/profile.
// On ties the first observation retains its lineage (active before idle).
func MaxMemory(first, second MemoryObservation) MemoryObservation {
	if second.Bytes > first.Bytes {
		return second
	}
	return first
}
