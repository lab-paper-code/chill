package controller

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	batchsaturation "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testDeviceType = "device-under-test"

func TestNewPreparerRequiresBothResolvers(t *testing.T) {
	metrics := &recordingTransitionMetricResolver{}
	baselines := &recordingIdleBaselineReader{}
	if _, err := NewPreparer(nil, baselines); err == nil {
		t.Fatal("nil metric resolver was accepted")
	}
	if _, err := NewPreparer(metrics, nil); err == nil {
		t.Fatal("nil baseline reader was accepted")
	}
}

func TestPreparerResolvesOneSourceAndTwoDirectedBaselines(t *testing.T) {
	measurement := validTransitionMeasurement()
	labels := map[string]string{"hostname": "meter-target", "device_type": testDeviceType}
	metrics := &recordingTransitionMetricResolver{result: profiler.ResolvedMetricSource{
		Kind: "edge-metrics",
		TransitionPower: &profiler.ResolvedTransitionPower{
			Endpoint:    "http://meter.example:18766/prometheus?hostname=meter-target",
			PowerMetric: "wall_power_watts", Labels: labels,
		},
	}}
	lowReference := storedObjectReference(measurement.Spec.Modes[0].IdleBaseline)
	highReference := storedObjectReference(measurement.Spec.Modes[1].IdleBaseline)
	baselines := &recordingIdleBaselineReader{results: map[string]ResolvedIdleBaseline{
		"low":  {meanWallWatts: 8, reference: lowReference},
		"high": {meanWallWatts: 12, reference: highReference},
	}}
	preparer := mustPreparer(t, metrics, baselines)

	prepared, err := preparer.Prepare(context.Background(), measurement)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.calls != 1 || metrics.node != measurement.Spec.TargetNode {
		t.Fatalf("metric resolution = calls:%d node:%q", metrics.calls, metrics.node)
	}
	if len(baselines.calls) != 2 {
		t.Fatalf("baseline calls = %d, want 2", len(baselines.calls))
	}
	for index, mode := range measurement.Spec.Modes {
		call := baselines.calls[index]
		wantState := batchsaturation.ResultMeasurementState{
			TargetNode: measurement.Spec.TargetNode, ModelSpec: measurement.Spec.ModelSpec,
			DeviceClass: measurement.Spec.DeviceClass, ExecutionPath: measurement.Spec.ExecutionPath,
			PowerMode: mode.PowerMode,
		}
		if call.state != wantState || call.reference != storedObjectReference(mode.IdleBaseline) {
			t.Fatalf("baseline call %d = %+v, want state %+v and reference %+v",
				index, call, wantState, storedObjectReference(mode.IdleBaseline))
		}
	}

	if prepared.Namespace != measurement.Namespace || prepared.TargetNode != measurement.Spec.TargetNode ||
		prepared.ServingService != measurement.Spec.ServingService ||
		prepared.ServingContainer != measurement.Spec.ServingContainer {
		t.Fatalf("prepared operational selection = %#v", prepared)
	}
	if prepared.PowerSource.Kind != metrics.result.Kind ||
		prepared.PowerSource.Endpoint != metrics.result.TransitionPower.Endpoint ||
		prepared.PowerSource.Metric != metrics.result.TransitionPower.PowerMetric ||
		prepared.PowerSource.NodeName != "" || prepared.PowerSource.ThermalMetrics != nil ||
		len(prepared.PowerSource.MetricLabels) != 2 ||
		prepared.PowerSource.MetricLabels["device_type"] != testDeviceType {
		t.Fatalf("prepared power source = %#v", prepared.PowerSource)
	}
	labels["device_type"] = "caller-mutation"
	if prepared.PowerSource.MetricLabels["device_type"] != testDeviceType {
		t.Fatal("prepared power labels alias resolver-owned labels")
	}

	if prepared.FactPollInterval != 200*time.Millisecond ||
		prepared.FactObservationTimeout != 900*time.Second ||
		prepared.BoundaryMappingUncertainty != 250*time.Millisecond ||
		prepared.PowerRequest != (metricobserver.Request{
			Interval: 500 * time.Millisecond, RequestTimeout: 100 * time.Millisecond,
			Duration: 900*time.Second + 500*time.Millisecond, MaxEvents: 1803,
		}) {
		t.Fatalf("prepared observation schedule = power:%+v poll:%v timeout:%v mapping:%v",
			prepared.PowerRequest, prepared.FactPollInterval,
			prepared.FactObservationTimeout, prepared.BoundaryMappingUncertainty)
	}
	wantPolicy := transitioncost.Policy{
		MaximumBoundaryDistance: 100 * time.Millisecond, MinimumCoverage: .95,
		MaximumSampleGap: time.Second, MaximumBoundaryUncertainty: 300 * time.Millisecond,
		MaximumReadyObservationDelay: 3 * time.Second,
	}
	if prepared.Policy != wantPolicy {
		t.Fatalf("prepared policy = %+v, want %+v", prepared.Policy, wantPolicy)
	}

	assertPreparedDirection(t, prepared.Directions[0], "low", "high", "to-high", highReference, 12)
	assertPreparedDirection(t, prepared.Directions[1], "high", "low", "to-low", lowReference, 8)
}

func TestPreparerRejectsOnlyUnusableShapeBeforeResolution(t *testing.T) {
	metrics := &recordingTransitionMetricResolver{}
	baselines := &recordingIdleBaselineReader{}
	preparer := mustPreparer(t, metrics, baselines)
	if _, err := preparer.Prepare(context.Background(), nil); err == nil {
		t.Fatal("nil measurement was accepted")
	}
	invalid := validTransitionMeasurement()
	invalid.Spec.Modes = invalid.Spec.Modes[:1]
	if _, err := preparer.Prepare(context.Background(), invalid); err == nil {
		t.Fatal("one-mode measurement was accepted")
	}
	invalid = validTransitionMeasurement()
	invalid.Spec.ObservationPolicy.MinimumCoverage = "not-a-number"
	if _, err := preparer.Prepare(context.Background(), invalid); err == nil {
		t.Fatal("unparseable minimum coverage was accepted")
	}
	if metrics.calls != 0 || len(baselines.calls) != 0 {
		t.Fatal("unusable input reached an external resolver")
	}
}

func TestPreparerRequiresExperimentTransitionPowerLabels(t *testing.T) {
	measurement := validTransitionMeasurement()
	tests := map[string]*profiler.ResolvedTransitionPower{
		"missing transition source": nil,
		"missing hostname": {
			Labels: map[string]string{"device_type": testDeviceType},
		},
		"missing device type": {
			Labels: map[string]string{"hostname": "meter-target"},
		},
		"extra label": {
			Labels: map[string]string{
				"hostname": "meter-target", "device_type": testDeviceType, "extra": "value",
			},
		},
		"unclean device type": {
			Labels: map[string]string{"hostname": "meter-target", "device_type": " padded "},
		},
	}
	for name, transitionPower := range tests {
		t.Run(name, func(t *testing.T) {
			metrics := &recordingTransitionMetricResolver{result: profiler.ResolvedMetricSource{
				Kind: "edge-metrics", TransitionPower: transitionPower,
			}}
			baselines := &recordingIdleBaselineReader{}
			_, err := mustPreparer(t, metrics, baselines).Prepare(context.Background(), measurement)
			if err == nil {
				t.Fatal("invalid transition power source was accepted")
			}
			if metrics.calls != 1 || len(baselines.calls) != 0 {
				t.Fatalf("resolver calls = metrics:%d baselines:%d", metrics.calls, len(baselines.calls))
			}
		})
	}
}

func TestPreparerStopsAtResolverFailure(t *testing.T) {
	measurement := validTransitionMeasurement()
	metrics := &recordingTransitionMetricResolver{err: errors.New("metric unavailable")}
	baselines := &recordingIdleBaselineReader{}
	if _, err := mustPreparer(t, metrics, baselines).Prepare(context.Background(), measurement); err == nil {
		t.Fatal("metric resolver failure was ignored")
	}
	if metrics.calls != 1 || len(baselines.calls) != 0 {
		t.Fatalf("resolver calls = metrics:%d baselines:%d", metrics.calls, len(baselines.calls))
	}

	metrics = validTransitionMetricResolver()
	baselines = &recordingIdleBaselineReader{err: errors.New("baseline unavailable")}
	if _, err := mustPreparer(t, metrics, baselines).Prepare(context.Background(), measurement); err == nil {
		t.Fatal("baseline resolver failure was ignored")
	}
	if metrics.calls != 1 || len(baselines.calls) != 1 {
		t.Fatalf("resolver calls = metrics:%d baselines:%d", metrics.calls, len(baselines.calls))
	}
}

func TestRetainedEventCountRejectsIntOverflow(t *testing.T) {
	if _, err := retainedEventCount(time.Duration(math.MaxInt64), time.Nanosecond); err == nil {
		t.Fatal("overflowing retained event count was accepted")
	}
}

func assertPreparedDirection(
	t *testing.T,
	direction PreparedDirection,
	sourceMode, targetMode, actuationPolicy string,
	reference batchsaturation.StoredObjectReference,
	meanWallWatts float64,
) {
	t.Helper()
	contract := direction.FrozenContract
	if contract.SourceState.PowerMode != sourceMode || contract.TargetState.PowerMode != targetMode ||
		contract.SourceState.DeviceType != testDeviceType ||
		contract.TargetState.Model != "model-under-test" ||
		contract.TargetIdleBaseline.State != contract.TargetState ||
		contract.TargetIdleBaseline.MeanWallWatts != meanWallWatts ||
		direction.ActuationPolicy != actuationPolicy {
		t.Fatalf("prepared direction = %+v", direction)
	}
	if direction.TargetIdleBaselineResult != reference ||
		contract.TargetIdleBaseline.EvidenceReference != reference.Key {
		t.Fatalf("baseline authority = full:%+v compact:%q, want %+v",
			direction.TargetIdleBaselineResult, contract.TargetIdleBaseline.EvidenceReference, reference)
	}
}

type recordingTransitionMetricResolver struct {
	result profiler.ResolvedMetricSource
	err    error
	calls  int
	node   string
}

func (r *recordingTransitionMetricResolver) ResolveMetricSource(
	_ context.Context,
	node string,
) (profiler.ResolvedMetricSource, error) {
	r.calls++
	r.node = node
	return r.result, r.err
}

type baselineReadCall struct {
	state     batchsaturation.ResultMeasurementState
	reference batchsaturation.StoredObjectReference
}

type recordingIdleBaselineReader struct {
	results map[string]ResolvedIdleBaseline
	err     error
	calls   []baselineReadCall
}

func (r *recordingIdleBaselineReader) Read(
	_ context.Context,
	state batchsaturation.ResultMeasurementState,
	reference batchsaturation.StoredObjectReference,
) (ResolvedIdleBaseline, error) {
	r.calls = append(r.calls, baselineReadCall{state: state, reference: reference})
	if r.err != nil {
		return ResolvedIdleBaseline{}, r.err
	}
	baseline, ok := r.results[state.PowerMode]
	if !ok {
		return ResolvedIdleBaseline{}, errors.New("unexpected baseline mode")
	}
	return baseline, nil
}

func mustPreparer(
	t *testing.T,
	metrics profiler.MetricSourceResolver,
	baselines IdleBaselineReader,
) *Preparer {
	t.Helper()
	preparer, err := NewPreparer(metrics, baselines)
	if err != nil {
		t.Fatal(err)
	}
	return preparer
}

func validTransitionMetricResolver() *recordingTransitionMetricResolver {
	return &recordingTransitionMetricResolver{result: profiler.ResolvedMetricSource{
		Kind: "edge-metrics",
		TransitionPower: &profiler.ResolvedTransitionPower{
			Endpoint:    "http://meter.example:18766/prometheus?hostname=meter-target",
			PowerMetric: "wall_power_watts",
			Labels:      map[string]string{"hostname": "meter-target", "device_type": testDeviceType},
		},
	}}
}

func validTransitionMeasurement() *edgev1alpha1.PowerModeTransitionCost {
	return &edgev1alpha1.PowerModeTransitionCost{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measurements"},
		Spec: edgev1alpha1.PowerModeTransitionCostSpec{
			TargetNode: "node-under-test", DeviceClass: "class-under-test",
			ModelSpec: "model-under-test", ExecutionPath: "path-under-test",
			Modes: []edgev1alpha1.PowerModeTransitionCostModeSpec{
				{PowerMode: "low", ActuationPolicy: "to-low", IdleBaseline: baselineAPIReference("low")},
				{PowerMode: "high", ActuationPolicy: "to-high", IdleBaseline: baselineAPIReference("high")},
			},
			ServingService: "serving", ServingContainer: "runtime",
			ObservationPolicy: edgev1alpha1.PowerModeTransitionCostObservationPolicySpec{
				PowerIntervalMilliseconds: 500, PowerTimeoutMilliseconds: 100,
				MaximumTransitionDurationSeconds: 900, FactPollIntervalMilliseconds: 200,
				MaximumBoundaryDistanceMilliseconds: 100, MinimumCoverage: "0.95",
				MaximumSampleGapMilliseconds: 1000, MaximumBoundaryUncertaintyMilliseconds: 300,
				BoundaryMappingUncertaintyMilliseconds: 250, MaximumReadyObservationDelayMilliseconds: 3000,
			},
		},
	}
}

func baselineAPIReference(mode string) edgev1alpha1.ProfilingRunEvidenceReference {
	return edgev1alpha1.ProfilingRunEvidenceReference{
		Store: "evidence", Key: "baselines/" + mode + "/result.json",
		SchemaVersion: "chill.dacs.io/batch-saturation-result.v1alpha3", Size: 2048,
	}
}
