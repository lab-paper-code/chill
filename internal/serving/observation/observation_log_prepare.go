package observation

import (
	"slices"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Only owned encoded bytes cross FIFO admission. Preparation is synchronous, so
// callers still receive serializer errors before native dispatch or success.
type preparedObservationRecord []byte

func (l *ObservationLog) prepareReferences(refs []observationReference) ([]observationReference, error) {
	if len(refs) == 0 {
		return refs, nil
	}
	l.submitMu.Lock()
	err := l.submissionErrorLocked()
	for index := range refs {
		ref := &refs[index]
		entry := l.references[ref.kind+"/"+ref.key]
		if entry.source != nil && entry.source == ref.body {
			ref.encoded = entry.body
		}
	}
	l.submitMu.Unlock()
	if err != nil {
		return nil, err
	}
	for index := range refs {
		if refs[index].encoded == nil {
			l.config.Diagnostics.Mark(serving.DiagnosticMarshal, serving.DiagnosticBegin, "", 0)
			data, err := l.marshal(refs[index].body)
			l.config.Diagnostics.Mark(serving.DiagnosticMarshal, serving.DiagnosticEnd, "", 0)
			if err != nil {
				return nil, err
			}
			refs[index].encoded = data
		}
	}
	return refs, nil
}

func (l *ObservationLog) rowReferences(row *serving.RequestRow) []observationReference {
	var refs []observationReference
	l.mu.Lock()
	definition := l.retained[row.DefinitionID]
	l.mu.Unlock()
	if definition != nil {
		refs = append(refs, observationReference{kind: observationDefinitionCatalog, key: row.DefinitionID, body: definition})
	}
	if row.ProfileBody != nil && row.ProfileID != "" {
		refs = append(refs, observationReference{kind: observationProfileCatalog, key: row.ProfileID, body: row.ProfileBody})
	}
	return refs
}

func (l *ObservationLog) prepareRequestReferences(id string, event *serving.RequestEvent) ([]observationReference, error) {
	var snapshot serving.RequestRow
	l.requestMu.Lock()
	if row := l.open[id]; row != nil {
		snapshot = *row
	}
	l.requestMu.Unlock()
	if event != nil {
		// Fold only identity; mutable native progress is not read outside its owner.
		if event.Definition != "" {
			snapshot.DefinitionID = event.Definition
		}
		if assignment := event.Assignment; assignment != nil {
			snapshot.ProfileID = assignment.Profile
			if assignment.ProfileBody != nil {
				snapshot.ProfileBody = assignment.ProfileBody
			}
		}
	}
	return l.prepareReferences(l.rowReferences(&snapshot))
}

func sameReferenceSources(left, right []observationReference) bool {
	return slices.EqualFunc(left, right, func(a, b observationReference) bool {
		return a.kind == b.kind && a.key == b.key && a.body == b.body
	})
}
