package profilederivation

// MemoryBasis identifies the retained observation selected for a cell, not an
// inferred startup bound. It belongs to the derived read view, never raw evidence.
type MemoryBasis struct {
	Observation string              `json:"observation"`
	Evidence    RawEvidenceIdentity `json:"evidence"`
}
