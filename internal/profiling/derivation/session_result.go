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
	MinimumSamples            int     `json:"minimumSamples"`
	// Percentile set MinimumSamples; it is also the confidence level of every
	// identification test in the derivation.
	Percentile float64 `json:"percentile"`
}

// ParameterStatus says whether a session identified a fitted value.
type ParameterStatus string

const (
	ParameterIdentified    ParameterStatus = "Identified"
	ParameterAtBound       ParameterStatus = "AtBound"
	ParameterNotIdentified ParameterStatus = "NotIdentified"
)

// SessionParameter is one derived value. Value is absent only when nothing
// was fitted; a fitted value that the levels cannot separate keeps its value
// and Range with status NotIdentified.
type SessionParameter struct {
	Status ParameterStatus `json:"status"`
	Value  *float64        `json:"value,omitempty"`
	// Range is the smallest and largest value over the hold-time candidates
	// whose fit residual lies within the level noise of the best one.
	Range  []float64 `json:"range,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// SessionPower predicts node watts as idle + step*awake + joulesPerInput*rate.
type SessionPower struct {
	IdleWatts      SessionParameter `json:"idleWatts"`
	AwakeStepWatts SessionParameter `json:"awakeStepWatts"`
	HoldSeconds    SessionParameter `json:"holdSeconds"`
	// PostLoad holds (seconds after load stopped, watts) samples.
	PostLoad [][2]float64 `json:"postLoad"`
}

// SessionBatch is one fixed-batch instance's measurement. ServiceNanos[i] and
// PrecedingIdleNanos[i] form one service record; parallel arrays keep the
// object at about 20 bytes per record.
type SessionBatch struct {
	Batch                  int              `json:"batch"`
	ServiceNanos           []int64          `json:"serviceNanos"`
	PrecedingIdleNanos     []int64          `json:"precedingIdleNanos"`
	DispatchOccupancyNanos []float64        `json:"dispatchOccupancyNanos"`
	JoulesPerInput         SessionParameter `json:"joulesPerInput"`
	PeakMemoryBytes        int64            `json:"peakMemoryBytes"`
	MemoryKind             string           `json:"memoryKind"`
	PrepareCostKey         string           `json:"prepareCostKey"`
	RemoveCostKey          string           `json:"removeCostKey"`
	Levels                 []SessionLevel   `json:"levels"`
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
}

// SessionLevel is a driver row with what the derivation measured over it.
// MeanWatts and AwakeFraction are absent when the window after the meter lag
// is shorter than the minimum power window.
type SessionLevel struct {
	DriverLevelRow
	InputRate     float64  `json:"inputRate"`
	MeanWatts     *float64 `json:"meanWatts,omitempty"`
	AwakeFraction *float64 `json:"awakeFraction,omitempty"`
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
