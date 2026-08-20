package transitioncost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const RawRecordSchemaVersion = "chill.dacs.io/transition-cost-raw-observation.v1alpha1"

type rawRecord struct {
	SchemaVersion  string         `json:"schemaVersion"`
	FrozenContract FrozenContract `json:"frozenContract"`
	Policy         Policy         `json:"policy"`
	RawObservation RawObservation `json:"rawObservation"`
}

// EncodeRawRecord classifies and encodes one raw observation using only the
// current stored contract. Typed scientific outcomes are retained, not turned
// into encoding errors.
func EncodeRawRecord(
	frozen FrozenContract,
	raw RawObservation,
	policy Policy,
) ([]byte, Result, error) {
	result, err := Derive(frozen, raw, policy)
	if err != nil {
		return nil, Result{}, err
	}
	payload, err := json.Marshal(rawRecord{
		SchemaVersion:  RawRecordSchemaVersion,
		FrozenContract: result.FrozenContract(),
		Policy:         policy,
		RawObservation: result.RawObservation(),
	})
	if err != nil {
		return nil, result, fmt.Errorf("encode transition raw record: %w", err)
	}
	return payload, result, nil
}

// DecodeRawRecord strictly decodes the sole supported stored contract and
// independently derives its terminal scientific result.
func DecodeRawRecord(payload []byte) (Result, error) {
	var record rawRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Result{}, fmt.Errorf("decode transition raw record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Result{}, errors.New("transition raw record contains a trailing JSON value")
	}
	if record.SchemaVersion != RawRecordSchemaVersion {
		return Result{}, fmt.Errorf(
			"unsupported transition raw record schemaVersion %q", record.SchemaVersion,
		)
	}
	result, err := Derive(record.FrozenContract, record.RawObservation, record.Policy)
	if err != nil {
		return Result{}, fmt.Errorf("derive transition raw record: %w", err)
	}
	return result, nil
}
