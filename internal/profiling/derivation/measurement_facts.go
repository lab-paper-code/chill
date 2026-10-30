package profilederivation

// MeasurementFacts is a memory/read projection, never part of a stored curve or result.
type MeasurementFacts struct {
	Request MeasurementRequestReference `json:"request"`
	ResultReference RawEvidenceIdentity `json:"resultReference"`
	Condition CurvePlanSpec `json:"condition"`
	PointPolicy PointPolicy `json:"pointPolicy"`
	Batches []MeasurementBatchFacts `json:"batches"`
}

type MeasurementRequestReference struct {
	Namespace string `json:"namespace"`
	Name string `json:"name"`
	UID string `json:"uid"`
}

type MeasurementPowerSource struct {
	Kind string `json:"kind"`
	NodeName string `json:"nodeName"`
	Endpoint string `json:"endpoint"`
	Metric string `json:"metric"`
	MetricLabels map[string]string `json:"metricLabels,omitempty"`
}

// MeasurementTrialQuality retains frozen capture settings and observed facts.
// A missing quality policy is unknown, not an invented passing threshold.
type MeasurementTrialQuality struct {
	TrialID string `json:"trialId"`
	WorkloadMode string `json:"workloadMode"`
	Protocol CurveProtocolContract `json:"protocol"`
	WindowSeconds float64 `json:"windowSeconds"`
	SuccessfulSamples int `json:"successfulSamples"`
	ReadFailures int `json:"readFailures"`
	MaximumSampleGapSeconds float64 `json:"maximumSampleGapSeconds"`
	SourceTimestampAbsent bool `json:"sourceTimestampAbsent"`
	PolicyPassed *bool `json:"policyPassed"`
}

type MeasurementBatchFacts struct {
	Batch int `json:"batch"`
	P95Milliseconds *float64 `json:"p95Milliseconds"`
	LatencySampleCount int `json:"latencySampleCount"`
	IdleWatts *float64 `json:"idleWatts"`
	ActiveWatts *float64 `json:"activeWatts"`
	TotalJoulesPerItem *float64 `json:"totalJoulesPerItem"`
	IncrementalJoulesPerItem *float64 `json:"incrementalJoulesPerItem"`
	Confidence *PointConfidence `json:"confidence"`
	ActiveTrialCount int `json:"activeTrialCount"`
	IdleTrialCount int `json:"idleTrialCount"`
	CompletedItems int `json:"completedItems"`
	CompletedCalls int `json:"completedCalls"`
	PowerSource MeasurementPowerSource `json:"powerSource"`
	Quality []MeasurementTrialQuality `json:"quality"`
	Sources []RawEvidenceIdentity `json:"sources"`
	MissingReasons []string `json:"missingReasons"`
}

// MeasurementPointFacts shares the same pooled client percentile and point CI
// used by Curve.Statistics. The caller transfers its disposable latency vector.
func MeasurementPointFacts(point Point, baseline IdleBaseline, latencies []float64, completedItems, completedCalls int) MeasurementBatchFacts {
	row := MeasurementBatchFacts{
		Batch: point.Batch, LatencySampleCount: len(latencies),
		ActiveTrialCount: len(point.Trials), IdleTrialCount: len(baseline.Trials),
		CompletedItems: completedItems, CompletedCalls: completedCalls,
		Quality: []MeasurementTrialQuality{}, Sources: []RawEvidenceIdentity{}, MissingReasons: []string{},
	}
	if len(latencies) > 0 {
		p95 := percentileInPlace(latencies, .95)
		row.P95Milliseconds = &p95
	} else {
		row.MissingReasons = append(row.MissingReasons, "ClientLatencyNotRecorded")
	}
	if len(point.Trials) == 0 {
		row.MissingReasons = append(row.MissingReasons, "StatisticsNotRecorded")
		return row
	}
	var total, watts float64
	for _, trial := range point.Trials {
		total += trial.ActiveWindowWallJoulesPerItem
		watts += trial.ActiveWindowWallJoulesPerItem / trial.IdleBaselineSecondsPerItem
	}
	total /= float64(len(point.Trials))
	watts /= float64(len(point.Trials))
	idle, incremental := baseline.ModelLoadedIdleMeanWallWatts, point.MeanIncrementalWorkJoulesPerItem
	confidence := clonePointConfidence(point.Confidence)
	row.TotalJoulesPerItem, row.ActiveWatts, row.IdleWatts = &total, &watts, &idle
	row.IncrementalJoulesPerItem, row.Confidence = &incremental, &confidence
	return row
}

// MeasurementBatches reads the already-derived curve without a second derivation.
func (c Curve) MeasurementBatches() []MeasurementBatchFacts {
	var rows []MeasurementBatchFacts
	for _, pass := range c.passes {
		for _, point := range pass.points {
			var latencies []float64
			for _, trial := range point.Trials {
				latencies = append(latencies, trial.ClientLatencyMilliseconds...)
			}
			calls := len(latencies)
			rows = append(rows, MeasurementPointFacts(point, c.baselines[point.StateIdentity], latencies, calls*point.Batch, calls))
		}
	}
	return rows
}
