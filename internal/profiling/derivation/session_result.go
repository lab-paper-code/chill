package profilederivation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
)

// SessionResultSchemaVersion identifies the one stored object of a profile
// measurement session, batch-energy-optimizations/<UID>/result.json.
const SessionResultSchemaVersion = "chill.dacs.io/batch-energy-optimization-result.v1alpha6"

// Level kinds a session runs.
const (
	LevelSaturation = "saturation"
	LevelMid        = "mid"
	LevelSparse     = "sparse"
)

// SessionResult is what one session measured for one configuration.
type SessionResult struct {
	SchemaVersion string           `json:"schemaVersion"`
	Condition     SessionCondition `json:"condition"`
	Settings      SessionSettings  `json:"settings"`
	Power         SessionPower     `json:"power"`
	Batches       []SessionBatch   `json:"batches"`
	// Stop is absent when the session reached the path's largest batch.
	Stop *SessionStop `json:"stop,omitempty"`
}

// SessionCondition is the configuration a session measured. It reuses the
// curve plan's contracts but not CurvePlanSpec itself: that spec is validated
// with the active and idle protocol contracts (fixed phase lengths, observer
// images) and the metric-source contract of the apparatus the session
// replaces, none of which a session has.
type SessionCondition struct {
	NodeName      string                          `json:"nodeName"`
	NodeUID       string                          `json:"nodeUID"`
	DeviceClass   string                          `json:"deviceClass"`
	PowerMode     string                          `json:"powerMode"`
	LogicalModel  string                          `json:"logicalModel"`
	RuntimeFamily string                          `json:"runtimeFamily"`
	Backend       string                          `json:"backend"`
	RuntimeImage  string                          `json:"runtimeImage"`
	Artifact      CurveArtifactContract           `json:"artifact"`
	Preprocessor  CurvePreprocessorContract       `json:"preprocessor"`
	Dataset       *CurveDatasetContract           `json:"dataset,omitempty"`
	Firstfloor    *modelcompat.FirstfloorSettings `json:"firstfloor,omitempty"`
	Actor         firstfloor.Config               `json:"actor"`
	RuntimeAccess *CurveRuntimeAccessContract     `json:"runtimeAccess,omitempty"`
}

// SessionSettings are the measurement request's settings, kept with the result.
type SessionSettings struct {
	MeterLagSeconds           float64 `json:"meterLagSeconds"`
	MinimumPowerWindowSeconds float64 `json:"minimumPowerWindowSeconds"`
	MidLevelRatio             float64 `json:"midLevelRatio"`
	SparseMeanGapSeconds      float64 `json:"sparseMeanGapSeconds"`
	LevelDurationLimitSeconds float64 `json:"levelDurationLimitSeconds"`
	PostLoadWindowSeconds     float64 `json:"postLoadWindowSeconds"`
	// MinimumSamples is the service record count the load generator ended
	// levels on.
	MinimumSamples int `json:"minimumSamples"`
	// ConfidenceLevel, in [0.5, 1), is the level of every statistical decision
	// the derivation makes.
	ConfidenceLevel float64 `json:"confidenceLevel"`
}

// ParameterStatus says whether a session identified a fitted value.
type ParameterStatus string

const (
	ParameterIdentified    ParameterStatus = "Identified"
	ParameterAtBound       ParameterStatus = "AtBound"
	ParameterNotIdentified ParameterStatus = "NotIdentified"
)

// SessionParameter is one derived value. Its value is readable only through
// Value, which refuses a NotIdentified parameter.
type SessionParameter struct {
	status ParameterStatus
	value  float64
	// bounds is the smallest and largest value the parameter takes over every
	// fit whose residual lies within the noise threshold of the best fit.
	bounds    [2]float64
	hasBounds bool
	reason    string
}

func identifiedParameter(value float64, bounds [2]float64) SessionParameter {
	return SessionParameter{status: ParameterIdentified, value: value, bounds: bounds, hasBounds: true}
}

func atBoundParameter() SessionParameter {
	return SessionParameter{status: ParameterAtBound, hasBounds: true}
}

func notIdentified(reason string) SessionParameter {
	return SessionParameter{status: ParameterNotIdentified, reason: reason}
}

func notIdentifiedWithin(reason string, bounds [2]float64) SessionParameter {
	return SessionParameter{status: ParameterNotIdentified, reason: reason, bounds: bounds, hasBounds: true}
}

// Status reports whether the session identified the parameter.
func (p SessionParameter) Status() ParameterStatus { return p.status }

// Value returns the parameter and true when it is Identified or AtBound.
func (p SessionParameter) Value() (float64, bool) {
	if p.status != ParameterIdentified && p.status != ParameterAtBound {
		return 0, false
	}
	return p.value, true
}

// Range returns the parameter's range over the fits within the noise threshold.
func (p SessionParameter) Range() ([2]float64, bool) { return p.bounds, p.hasBounds }

// Reason says why a parameter is not identified.
func (p SessionParameter) Reason() string { return p.reason }

type sessionParameterWire struct {
	Status ParameterStatus `json:"status"`
	Value  *float64        `json:"value,omitempty"`
	Range  *[2]float64     `json:"range,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

func (p SessionParameter) MarshalJSON() ([]byte, error) {
	wire := sessionParameterWire{Status: p.status, Reason: p.reason}
	if value, ok := p.Value(); ok {
		wire.Value = &value
	}
	if p.hasBounds {
		bounds := p.bounds
		wire.Range = &bounds
	}
	return json.Marshal(wire)
}

func (p *SessionParameter) UnmarshalJSON(raw []byte) error {
	var wire sessionParameterWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	switch {
	case wire.Status == ParameterNotIdentified && wire.Value == nil:
	case wire.Status == ParameterIdentified && wire.Value != nil:
	case wire.Status == ParameterAtBound && wire.Value != nil && *wire.Value == 0:
	default:
		return fmt.Errorf("parameter status %q does not match its value", wire.Status)
	}
	*p = SessionParameter{status: wire.Status, reason: wire.Reason}
	if wire.Value != nil {
		p.value = *wire.Value
	}
	if wire.Range != nil {
		p.bounds, p.hasBounds = *wire.Range, true
	}
	return nil
}

// SessionPower predicts node watts as idle + step*awake + joulesPerInput*rate,
// where awake is the fraction of time inside an execution or within the hold
// after one.
type SessionPower struct {
	IdleWatts      SessionParameter `json:"idleWatts"`
	AwakeStepWatts SessionParameter `json:"awakeStepWatts"`
	HoldSeconds    SessionParameter `json:"holdSeconds"`
	// MeterIntervalSeconds is the median spacing of the post-load samples.
	MeterIntervalSeconds float64 `json:"meterIntervalSeconds"`
	// SettledAtSeconds is where the settled idle mean starts, in seconds after
	// load stopped.
	SettledAtSeconds float64 `json:"settledAtSeconds"`
	// PostLoad holds (seconds after load stopped, watts) samples.
	PostLoad [][2]float64 `json:"postLoad"`
}

// SessionBatch is one fixed-batch instance's measurement. ServiceNanos[i] and
// PrecedingIdleNanos[i] form one service record; parallel arrays keep the
// object at about 21 bytes per record.
type SessionBatch struct {
	Batch                  int     `json:"batch"`
	ServiceNanos           []int64 `json:"serviceNanos"`
	PrecedingIdleNanos     []int64 `json:"precedingIdleNanos"`
	DispatchOccupancyNanos []float64 `json:"dispatchOccupancyNanos"`
	// SaturationInputRate is the saturation level's completed inputs per
	// second, absent when the batch has no saturation level.
	SaturationInputRate *float64         `json:"saturationInputRate,omitempty"`
	JoulesPerInput      SessionParameter `json:"joulesPerInput"`
	PeakMemoryBytes     int64            `json:"peakMemoryBytes"`
	MemoryKind          string           `json:"memoryKind"`
	PrepareCostKey      string           `json:"prepareCostKey"`
	RemoveCostKey       string           `json:"removeCostKey"`
	Levels              []SessionLevel   `json:"levels"`
}

// DriverLevelRow is the load driver's report of one level.
type DriverLevelRow struct {
	Kind             string    `json:"kind"`
	ID               string    `json:"id"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	SentInputs       int       `json:"sentInputs"`
	CompletedInputs  int       `json:"completedInputs"`
	CompletedBatches int       `json:"completedBatches"`
	EndReason        string    `json:"endReason"`
	// PredictedSeconds and LimitSeconds are present when the driver ended the
	// level as not measured because its predicted duration exceeded the limit.
	PredictedSeconds *float64 `json:"predictedSeconds,omitempty"`
	LimitSeconds     *float64 `json:"limitSeconds,omitempty"`
}

// SessionLevel is a driver row with what the derivation measured over it.
type SessionLevel struct {
	DriverLevelRow
	InputRate float64 `json:"inputRate"`
	// NativeStartOffsetNanos is the first native start minus the driver's
	// level start; the two clocks are never compared.
	NativeStartOffsetNanos *int64 `json:"nativeStartOffsetNanos,omitempty"`
	// MeanWatts is absent when the level's power window after the meter lag
	// is shorter than the minimum power window, or its executions after the
	// meter lag span no time. Only levels with MeanWatts enter the power fit.
	MeanWatts *float64 `json:"meanWatts,omitempty"`
	// MeanWattsStandardError accounts for the autocorrelation of the samples.
	MeanWattsStandardError *float64 `json:"meanWattsStandardError,omitempty"`
	PowerSamples           int      `json:"powerSamples"`
}

// StopClass is why the batch-size search ended before the path's largest batch.
type StopClass string

const (
	StopCapacityLimit StopClass = "CapacityLimit"
	StopNotServable   StopClass = "NotServable"
	StopNotMeasured   StopClass = "NotMeasured"
)

// SessionStop is the batch size the session did not measure and why.
type SessionStop struct {
	Batch  int       `json:"batch"`
	Class  StopClass `json:"class"`
	Reason string    `json:"reason"`
}

// DecodeSessionResult strict-decodes a stored session result.
func DecodeSessionResult(raw []byte) (SessionResult, error) {
	var result SessionResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return SessionResult{}, fmt.Errorf("decode session result: %w", err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return SessionResult{}, errors.New("session result contains a trailing JSON value")
	}
	if result.SchemaVersion != SessionResultSchemaVersion {
		return SessionResult{}, fmt.Errorf("unsupported session result schema %q", result.SchemaVersion)
	}
	for _, batch := range result.Batches {
		if len(batch.ServiceNanos) != len(batch.PrecedingIdleNanos) {
			return SessionResult{}, fmt.Errorf("batch %d has %d service durations and %d idle gaps", batch.Batch, len(batch.ServiceNanos), len(batch.PrecedingIdleNanos))
		}
	}
	return result, nil
}
