package projection

import (
	"maps"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func MeasurementPowerSource(source metricobserver.SourceIdentity) profilederivation.MeasurementPowerSource {
	return profilederivation.MeasurementPowerSource{Kind: source.Kind, NodeName: source.NodeName, Endpoint: source.Endpoint, Metric: source.Metric, MetricLabels: maps.Clone(source.MetricLabels)}
}

func MeasurementQuality(config profilingrun.RunConfig, metrics metricobserver.Result, start, end time.Time, protocol profilederivation.CurveProtocolContract) profilederivation.MeasurementTrialQuality {
	return profilederivation.MeasurementTrialQuality{
		WorkloadMode: config.Measurement.WorkloadMode, Protocol: protocol,
		WindowSeconds: end.Sub(start).Seconds(), SuccessfulSamples: len(metrics.Samples), ReadFailures: len(metrics.Failures),
		MaximumSampleGapSeconds: metrics.Summary.MaximumSampleGapSeconds, SourceTimestampAbsent: metrics.Summary.SourceTimestampAbsent,
	}
}
