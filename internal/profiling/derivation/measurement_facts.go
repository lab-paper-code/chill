package profilederivation

// MeasurementFacts is a memory/read projection, never part of a stored curve or result.
type MeasurementFacts struct {
	Request         MeasurementRequestReference `json:"request"`
	ResultReference RawEvidenceIdentity         `json:"resultReference"`
	Condition       CurvePlanSpec               `json:"condition"`
	PointPolicy     PointPolicy                 `json:"pointPolicy"`
	Batches         []MeasurementBatchFacts     `json:"batches"`
}

type MeasurementRequestReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type MeasurementPowerSource struct {
	Kind         string            `json:"kind"`
	NodeName     string            `json:"nodeName"`
	Endpoint     string            `json:"endpoint"`
	Metric       string            `json:"metric"`
	MetricLabels map[string]string `json:"metricLabels,omitempty"`
}

// MeasurementTrialQuality retains frozen capture settings and observed facts.
// A missing quality policy is unknown, not an invented passing threshold.
type MeasurementTrialQuality struct {
	TrialID                 string                `json:"trialId"`
	WorkloadMode            string                `json:"workloadMode"`
	Protocol                CurveProtocolContract `json:"protocol"`
	WindowSeconds           float64               `json:"windowSeconds"`
	SuccessfulSamples       int                   `json:"successfulSamples"`
	ReadFailures            int                   `json:"readFailures"`
	MaximumSampleGapSeconds float64               `json:"maximumSampleGapSeconds"`
	SourceTimestampAbsent   bool                  `json:"sourceTimestampAbsent"`
	PolicyPassed            *bool                 `json:"policyPassed"`
	PolicyReason            string                `json:"policyReason,omitempty"`
}

type MeasurementBatchFacts struct {
	Batch                          int                       `json:"batch"`
	P95Milliseconds                *float64                  `json:"p95Milliseconds"`
	LatencySampleCount             int                       `json:"latencySampleCount"`
	IdleWatts                      *float64                  `json:"idleWatts"`
	ActiveWatts                    *float64                  `json:"activeWatts"`
	TotalJoulesPerItem             *float64                  `json:"totalJoulesPerItem"`
	IncrementalJoulesPerItem       *float64                  `json:"incrementalJoulesPerItem"`
	Confidence                     *PointConfidence          `json:"confidence"`
	ActiveTrialCount               int                       `json:"activeTrialCount"`
	IdleTrialCount                 int                       `json:"idleTrialCount"`
	CompletedItems                 int                       `json:"completedItems"`
	CompletedCalls                 int                       `json:"completedCalls"`
	PowerSource                    MeasurementPowerSource    `json:"powerSource"`
	Quality                        []MeasurementTrialQuality `json:"quality"`
	Sources                        []RawEvidenceIdentity     `json:"sources"`
	MissingReasons                 []string                  `json:"missingReasons"`
	PhasePower                     []PhasePower              `json:"phasePower"`
	DispatchOccupancyNanosPerInput []float64                 `json:"dispatchOccupancyNanosPerInput"`
}
