package measurement

import api "github.com/lab-paper-code/chill/api/v1alpha1"

// MeasurementMutationResult describes native asynchronous admission, not completed work.
type MeasurementMutationResult struct {
	Request         string                        `json:"request"`
	Task            *api.BatchEnergyOptimization  `json:"task"`
	EvidenceDeleted bool                          `json:"evidenceDeleted,omitempty"`
	DeletedEvidence []MeasurementEvidenceLocation `json:"deletedEvidence,omitempty"`
}

// MeasurementEvidenceLocation is the public deletion receipt's logical wire location.
type MeasurementEvidenceLocation struct {
	Store string `json:"store"`
	Key   string `json:"key"`
}
