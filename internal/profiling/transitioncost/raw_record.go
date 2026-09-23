package transitioncost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const RawRecordSchemaVersion = "chill.dacs.io/transition-cost-raw-observation.v1alpha4"

type rawRecord struct {
	SchemaVersion  string         `json:"schemaVersion"`
	RawObservation RawObservation `json:"rawObservation"`
}

// EncodeRawRecord encodes one observation without classifying or deriving it.
func EncodeRawRecord(raw RawObservation) ([]byte, error) {
	payload, err := json.Marshal(rawRecord{
		SchemaVersion:  RawRecordSchemaVersion,
		RawObservation: raw,
	})
	if err != nil {
		return nil, fmt.Errorf("encode transition raw record: %w", err)
	}
	return payload, nil
}

// DecodeRawRecord strictly decodes the sole supported stored contract.
// Scientific validation and classification belong to Derive.
func DecodeRawRecord(payload []byte) (RawObservation, error) {
	var record rawRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return RawObservation{}, fmt.Errorf("decode transition raw record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RawObservation{}, errors.New("transition raw record contains a trailing JSON value")
	}
	if record.SchemaVersion != RawRecordSchemaVersion {
		return RawObservation{}, fmt.Errorf(
			"unsupported transition raw record schemaVersion %q", record.SchemaVersion,
		)
	}
	return record.RawObservation, nil
}
