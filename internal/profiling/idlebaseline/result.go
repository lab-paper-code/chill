// Package idlebaseline owns the stored result for the model-loaded-idle
// aggregate used as P_idle by transition-cost measurements.
package idlebaseline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

const ResultSchemaVersion = "chill.dacs.io/model-loaded-idle-baseline-result.v1alpha2"

type MeasurementState struct {
	TargetNode    string `json:"targetNode"`
	ModelSpec     string `json:"modelSpec"`
	DeviceClass   string `json:"deviceClass"`
	ExecutionPath string `json:"executionPath"`
	PowerMode     string `json:"powerMode"`
}

// StoredObjectReference identifies one retained evidence object.
type StoredObjectReference struct {
	Store         string `json:"store"`
	Key           string `json:"key"`
	SchemaVersion string `json:"schemaVersion"`
	Size          int64  `json:"size"`
}

// FinalEvidence binds each completed scientific TrialID to the final
// retained execution evidence from which that trial was evaluated.
type FinalEvidence map[string]StoredObjectReference

type resultRecord struct {
	SchemaVersion    string                         `json:"schemaVersion"`
	MeasurementState MeasurementState               `json:"measurementState"`
	IdleBaseline     profilederivation.IdleBaseline `json:"idleBaseline"`
	FinalEvidence    FinalEvidence                  `json:"finalEvidence"`
}

// Result is one initialized Complete aggregate result.
type Result struct {
	record resultRecord
}

// NewResult binds a freshly derived aggregate to its exact state and retained
// accepted final-evidence references. DeriveIdleBaseline owns numeric checks.
func NewResult(
	state MeasurementState,
	baseline profilederivation.IdleBaseline,
	accepted FinalEvidence,
) (Result, error) {
	if err := validateMeasurementState(state); err != nil {
		return Result{}, err
	}
	if err := validateFinalEvidence(baseline, accepted); err != nil {
		return Result{}, err
	}
	accepted = cloneFinalEvidence(accepted)
	baseline.Trials = append([]profilederivation.IdleTrial(nil), baseline.Trials...)
	return Result{record: resultRecord{
		SchemaVersion: ResultSchemaVersion, MeasurementState: state,
		IdleBaseline: baseline, FinalEvidence: accepted,
	}}, nil
}

// DecodeResult strict-decodes the current stored result and restores its
// aggregate at the untrusted storage boundary.
func DecodeResult(payload []byte) (Result, error) {
	var record resultRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Result{}, fmt.Errorf("decode model-loaded idle baseline result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Result{}, errors.New("model-loaded idle baseline result contains a trailing JSON value")
	}
	if record.SchemaVersion != ResultSchemaVersion {
		return Result{}, fmt.Errorf("unsupported model-loaded idle baseline result schema %q", record.SchemaVersion)
	}
	if err := validateMeasurementState(record.MeasurementState); err != nil {
		return Result{}, err
	}
	baseline, err := profilederivation.RestoreIdleBaseline(record.IdleBaseline)
	if err != nil {
		return Result{}, fmt.Errorf("restore model-loaded idle baseline: %w", err)
	}
	if err := validateFinalEvidence(baseline, record.FinalEvidence); err != nil {
		return Result{}, err
	}
	record.IdleBaseline = baseline
	record.FinalEvidence = cloneFinalEvidence(record.FinalEvidence)
	return Result{record: record}, nil
}

func (r Result) MarshalJSON() ([]byte, error) {
	if r.record.SchemaVersion != ResultSchemaVersion {
		return nil, errors.New("model-loaded idle baseline result is not initialized")
	}
	return json.Marshal(r.record)
}

func (r Result) SchemaVersion() string {
	if r.record.SchemaVersion != ResultSchemaVersion {
		return ""
	}
	return r.record.SchemaVersion
}

func (r Result) MeasurementState() MeasurementState { return r.record.MeasurementState }

func (r Result) IdleBaseline() profilederivation.IdleBaseline {
	baseline := r.record.IdleBaseline
	baseline.Trials = append([]profilederivation.IdleTrial(nil), baseline.Trials...)
	return baseline
}

func (r Result) FinalEvidence() FinalEvidence {
	return cloneFinalEvidence(r.record.FinalEvidence)
}

func validateFinalEvidence(
	baseline profilederivation.IdleBaseline,
	accepted FinalEvidence,
) error {
	if accepted == nil || len(accepted) != len(baseline.Trials) || len(accepted) < 3 {
		return errors.New("accepted final evidence does not match the idle trial IDs")
	}
	for _, trial := range baseline.Trials {
		reference, found := accepted[trial.TrialID]
		if !found || trial.TrialID == "" {
			return errors.New("accepted final evidence does not match the idle trial IDs")
		}
		if err := validateStoredObjectReference(reference); err != nil {
			return err
		}
	}
	return nil
}

func cloneFinalEvidence(source FinalEvidence) FinalEvidence {
	if source == nil {
		return nil
	}
	clone := make(FinalEvidence, len(source))
	for trialID, reference := range source {
		clone[trialID] = reference
	}
	return clone
}

func validateMeasurementState(state MeasurementState) error {
	values := []string{state.TargetNode, state.ModelSpec, state.DeviceClass, state.ExecutionPath, state.PowerMode}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return errors.New("model-loaded idle baseline measurement state is incomplete")
		}
	}
	return nil
}

func validateStoredObjectReference(reference StoredObjectReference) error {
	values := []string{reference.Store, reference.Key, reference.SchemaVersion}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return errors.New("accepted final evidence reference is incomplete")
		}
	}
	if reference.Size < 1 {
		return errors.New("accepted final evidence size must be positive")
	}
	return nil
}
