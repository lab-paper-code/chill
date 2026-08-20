package batchsaturation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

const resultRecordSchemaVersion = "chill.dacs.io/batch-saturation-result.v1alpha3"

// TerminalOutcome is the stable terminal outcome published by a measurement
// method.
type TerminalOutcome string

// StoredObjectReference identifies one immutable object and the schema used to
// decode it.
type StoredObjectReference struct {
	Store         string `json:"store"`
	Key           string `json:"key"`
	SchemaVersion string `json:"schemaVersion"`
	Size          int64  `json:"size"`
}

// RunReference identifies one owned ProfilingRun without importing Kubernetes
// API types into the pure package.
type RunReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// ResultMeasurement is the wire projection of a trial measurement.
type ResultMeasurement struct {
	WorkloadMode WorkloadMode `json:"workloadMode"`
	Batch        int          `json:"batch"`
}

// ResultMeasurementState names the experimental state shared by every trial
// contributing to one terminal result.
type ResultMeasurementState struct {
	TargetNode    string `json:"targetNode"`
	ModelSpec     string `json:"modelSpec"`
	DeviceClass   string `json:"deviceClass"`
	ExecutionPath string `json:"executionPath"`
	PowerMode     string `json:"powerMode"`
}

// AdmittedTrialReference locates one admitted Run and its retained evidence.
type AdmittedTrialReference struct {
	Run         RunReference          `json:"run"`
	Measurement ResultMeasurement     `json:"measurement"`
	Evidence    StoredObjectReference `json:"evidence"`
}

// ExcludedTrialReference identifies one non-admitted attempt and the reasons
// that kept it out of the derived result.
type ExcludedTrialReference struct {
	Run         RunReference      `json:"run"`
	Measurement ResultMeasurement `json:"measurement"`
	Attempt     int               `json:"attempt"`
	Reasons     []string          `json:"reasons"`
}

// ResultRecordSpec is the validated payload of one terminal result record.
type ResultRecordSpec struct {
	Outcome          TerminalOutcome          `json:"outcome"`
	MeasurementState ResultMeasurementState   `json:"measurementState"`
	CurvePlan        StoredObjectReference    `json:"curvePlan"`
	AdmittedTrials   []AdmittedTrialReference `json:"admittedTrials"`
	ExcludedTrials   []ExcludedTrialReference `json:"excludedTrials"`
	Detail           json.RawMessage          `json:"detail"`
}

type resultRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	ResultRecordSpec
}

// ResultRecord can only be constructed from a structurally valid result
// projection. Detail is not schema-interpreted; its canonical JSON encoding is
// stable and preserves the decoded value rather than arbitrary caller bytes.
type ResultRecord struct {
	record resultRecord
}

// NewResultRecord validates and orders one terminal result projection.
func NewResultRecord(spec ResultRecordSpec) (ResultRecord, error) {
	spec = cloneResultRecordSpec(spec)
	normalizeResultRecordSpec(&spec)
	if err := validateResultRecordSpec(spec); err != nil {
		return ResultRecord{}, err
	}
	return ResultRecord{record: resultRecord{
		SchemaVersion:    resultRecordSchemaVersion,
		ResultRecordSpec: spec,
	}}, nil
}

// DecodeResultRecord strict-decodes exactly one supported result record.
func DecodeResultRecord(raw []byte) (ResultRecord, error) {
	var record resultRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return ResultRecord{}, fmt.Errorf("decode batch-saturation result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ResultRecord{}, errors.New("batch-saturation result contains a trailing JSON value")
	}
	if record.SchemaVersion != resultRecordSchemaVersion {
		return ResultRecord{}, fmt.Errorf(
			"unsupported batch-saturation result schema %q", record.SchemaVersion,
		)
	}
	return NewResultRecord(record.ResultRecordSpec)
}

// CanonicalJSON returns the deterministic wire representation of the record.
func (r ResultRecord) CanonicalJSON() ([]byte, error) {
	if err := r.requireInitialized(); err != nil {
		return nil, err
	}
	return json.Marshal(r.record)
}

// SchemaVersion returns the exact result-record schema identifier.
func (r ResultRecord) SchemaVersion() string {
	if r.requireInitialized() != nil {
		return ""
	}
	return r.record.SchemaVersion
}

// Spec returns an isolated copy of the validated record payload.
func (r ResultRecord) Spec() ResultRecordSpec {
	return cloneResultRecordSpec(r.record.ResultRecordSpec)
}

// SemanticallyEqual compares validated fields and the JSON value carried by
// Detail without treating an object's whitespace or member order as identity.
func (r ResultRecord) SemanticallyEqual(other ResultRecord) bool {
	if r.requireInitialized() != nil || other.requireInitialized() != nil {
		return false
	}
	left := r.Spec()
	right := other.Spec()
	leftDetail := left.Detail
	rightDetail := right.Detail
	left.Detail = nil
	right.Detail = nil
	return reflect.DeepEqual(left, right) && equalJSONValues(leftDetail, rightDetail)
}

func (r ResultRecord) requireInitialized() error {
	if r.record.SchemaVersion != resultRecordSchemaVersion {
		return errors.New("batch-saturation result is not initialized")
	}
	return nil
}

func normalizeResultRecordSpec(spec *ResultRecordSpec) {
	if spec.AdmittedTrials == nil {
		spec.AdmittedTrials = []AdmittedTrialReference{}
	}
	if spec.ExcludedTrials == nil {
		spec.ExcludedTrials = []ExcludedTrialReference{}
	}
	for index := range spec.ExcludedTrials {
		sort.Strings(spec.ExcludedTrials[index].Reasons)
	}
	sort.Slice(spec.AdmittedTrials, func(i, j int) bool {
		return admittedTrialLess(spec.AdmittedTrials[i], spec.AdmittedTrials[j])
	})
	sort.Slice(spec.ExcludedTrials, func(i, j int) bool {
		return excludedTrialLess(spec.ExcludedTrials[i], spec.ExcludedTrials[j])
	})
}

func validateResultRecordSpec(spec ResultRecordSpec) error {
	if !cleanResultValue(string(spec.Outcome)) {
		return errors.New("batch-saturation result outcome is required without surrounding whitespace")
	}
	if err := validateResultMeasurementState(spec.MeasurementState); err != nil {
		return err
	}
	if err := validateStoredObjectReference("curve plan", spec.CurvePlan); err != nil {
		return err
	}
	seenRuns := make(map[string]struct{}, len(spec.AdmittedTrials)+len(spec.ExcludedTrials))
	for _, trial := range spec.AdmittedTrials {
		if err := validateRunReference(trial.Run); err != nil {
			return fmt.Errorf("validate admitted Run reference: %w", err)
		}
		if err := validateResultMeasurement(trial.Measurement); err != nil {
			return fmt.Errorf("validate admitted Run %q measurement: %w", trial.Run.Name, err)
		}
		if err := validateStoredObjectReference("admitted Run evidence", trial.Evidence); err != nil {
			return fmt.Errorf("validate admitted Run %q: %w", trial.Run.Name, err)
		}
		if err := recordRunUID(seenRuns, trial.Run); err != nil {
			return err
		}
	}
	for _, trial := range spec.ExcludedTrials {
		if err := validateRunReference(trial.Run); err != nil {
			return fmt.Errorf("validate excluded Run reference: %w", err)
		}
		if err := validateResultMeasurement(trial.Measurement); err != nil {
			return fmt.Errorf("validate excluded Run %q measurement: %w", trial.Run.Name, err)
		}
		if trial.Attempt < 1 {
			return fmt.Errorf("excluded Run %q attempt must be positive", trial.Run.Name)
		}
		if len(trial.Reasons) == 0 {
			return fmt.Errorf("excluded Run %q requires an exclusion reason", trial.Run.Name)
		}
		for _, reason := range trial.Reasons {
			if !cleanResultValue(reason) {
				return fmt.Errorf("excluded Run %q has an invalid exclusion reason", trial.Run.Name)
			}
		}
		if err := recordRunUID(seenRuns, trial.Run); err != nil {
			return err
		}
	}
	if !json.Valid(spec.Detail) {
		return errors.New("batch-saturation result detail must contain exactly one JSON value")
	}
	return nil
}

func validateResultMeasurementState(state ResultMeasurementState) error {
	for name, value := range map[string]string{
		"target Node": state.TargetNode, "ModelSpec": state.ModelSpec,
		"DeviceClass": state.DeviceClass, "execution path": state.ExecutionPath,
		"power mode": state.PowerMode,
	} {
		if !cleanResultValue(value) {
			return fmt.Errorf("batch-saturation result measurement state %s is required without surrounding whitespace", name)
		}
	}
	return nil
}

func validateStoredObjectReference(name string, reference StoredObjectReference) error {
	for field, value := range map[string]string{
		"store": reference.Store, "key": reference.Key, "schema version": reference.SchemaVersion,
	} {
		if !cleanResultValue(value) {
			return fmt.Errorf("%s %s is required without surrounding whitespace", name, field)
		}
	}
	if reference.Size < 1 {
		return fmt.Errorf("%s size must be positive", name)
	}
	return nil
}

func validateRunReference(reference RunReference) error {
	if !cleanResultValue(reference.Namespace) || !cleanResultValue(reference.Name) ||
		!cleanResultValue(reference.UID) {
		return errors.New("Run namespace, name, and UID are required without surrounding whitespace")
	}
	return nil
}

func validateResultMeasurement(measurement ResultMeasurement) error {
	return Measurement(measurement).Validate()
}

func recordRunUID(seen map[string]struct{}, reference RunReference) error {
	if _, exists := seen[reference.UID]; exists {
		return fmt.Errorf("ProfilingRun UID %q appears more than once", reference.UID)
	}
	seen[reference.UID] = struct{}{}
	return nil
}

func admittedTrialLess(left, right AdmittedTrialReference) bool {
	if left.Measurement.WorkloadMode != right.Measurement.WorkloadMode {
		return left.Measurement.WorkloadMode < right.Measurement.WorkloadMode
	}
	if left.Measurement.Batch != right.Measurement.Batch {
		return left.Measurement.Batch < right.Measurement.Batch
	}
	return left.Run.Name < right.Run.Name
}

func excludedTrialLess(left, right ExcludedTrialReference) bool {
	if left.Measurement.WorkloadMode != right.Measurement.WorkloadMode {
		return left.Measurement.WorkloadMode < right.Measurement.WorkloadMode
	}
	if left.Measurement.Batch != right.Measurement.Batch {
		return left.Measurement.Batch < right.Measurement.Batch
	}
	if left.Attempt != right.Attempt {
		return left.Attempt < right.Attempt
	}
	return left.Run.Name < right.Run.Name
}

func cloneResultRecordSpec(spec ResultRecordSpec) ResultRecordSpec {
	copy := spec
	copy.Detail = bytes.Clone(spec.Detail)
	copy.AdmittedTrials = append([]AdmittedTrialReference(nil), spec.AdmittedTrials...)
	copy.ExcludedTrials = append([]ExcludedTrialReference(nil), spec.ExcludedTrials...)
	for index := range copy.ExcludedTrials {
		copy.ExcludedTrials[index].Reasons = append([]string(nil), spec.ExcludedTrials[index].Reasons...)
	}
	return copy
}

func equalJSONValues(left, right []byte) bool {
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, errors.New("trailing JSON value")
		}
		return value, nil
	}
	leftValue, leftErr := decode(left)
	rightValue, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftValue, rightValue)
}

func cleanResultValue(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value
}
