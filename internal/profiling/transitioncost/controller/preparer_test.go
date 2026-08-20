package controller

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const testDeviceType = "device-under-test"

func TestNewPreparerRequiresEveryResolver(t *testing.T) {
	catalogs := newRecordingDeviceClassReader(validDeviceClass())
	nodes := validNodeResolver()
	metrics := &recordingTransitionMetricResolver{}
	baselines := &recordingIdleBaselineReader{}
	actuation := validActuationPolicyResolver()
	tests := []struct {
		name      string
		catalogs  client.Reader
		nodes     profiler.NodeResolver
		metrics   profiler.MetricSourceResolver
		baselines IdleBaselineReader
		actuation profiler.ActuationPolicyResolver
	}{
		{name: "DeviceClass reader", nodes: nodes, metrics: metrics, baselines: baselines, actuation: actuation},
		{name: "Node resolver", catalogs: catalogs, metrics: metrics, baselines: baselines, actuation: actuation},
		{name: "metric resolver", catalogs: catalogs, nodes: nodes, baselines: baselines, actuation: actuation},
		{name: "baseline reader", catalogs: catalogs, nodes: nodes, metrics: metrics, actuation: actuation},
		{name: "actuation-policy resolver", catalogs: catalogs, nodes: nodes, metrics: metrics, baselines: baselines},
	}
	for _, test := range tests {
		if _, err := NewPreparer(test.catalogs, test.nodes, test.metrics, test.baselines, test.actuation, "observer"); err == nil {
			t.Fatalf("nil %s was accepted", test.name)
		}
	}
	if _, err := NewPreparer(catalogs, nodes, metrics, baselines, actuation, "NOT_A_NODE"); err == nil {
		t.Fatal("invalid observer Node name was accepted")
	}
}

func TestPreparerFreezesTransitionMaterialAndObservationInputs(t *testing.T) {
	measurement := validTransitionMeasurement()
	labels := map[string]string{"hostname": "meter-target", "device_type": testDeviceType}
	metrics := &recordingTransitionMetricResolver{result: profiler.ResolvedMetricSource{
		Kind: "edge-metrics", Endpoint: "http://meter.example:18766/prometheus?hostname=meter-target",
		PowerMetric: "wall_power_watts", MetricLabels: labels,
	}}
	lowReference := storedObjectReference(measurement.Spec.Modes[0].IdleBaseline)
	highReference := storedObjectReference(measurement.Spec.Modes[1].IdleBaseline)
	baselines := &recordingIdleBaselineReader{results: map[string]ResolvedIdleBaseline{
		"low":  {meanWallWatts: 8, reference: lowReference},
		"high": {meanWallWatts: 12, reference: highReference},
	}}
	catalogs := newRecordingDeviceClassReader(validDeviceClass())
	nodes := validNodeResolver()
	actuation := validActuationPolicyResolver()
	preparer := mustPreparerWith(t, catalogs, nodes, metrics, baselines, actuation)

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
	assertTransitionMaterialResolution(t, measurement, catalogs, nodes, actuation)
	for index, mode := range measurement.Spec.Modes {
		call := baselines.calls[index]
		wantState := idlebaseline.MeasurementState{
			TargetNode: measurement.Spec.TargetNode, ModelSpec: measurement.Spec.ModelSpec,
			DeviceClass: measurement.Spec.DeviceClass, ExecutionPath: measurement.Spec.ExecutionPath,
			PowerMode: mode.PowerMode,
		}
		if call.state != wantState || call.reference != storedObjectReference(mode.IdleBaseline) {
			t.Fatalf("baseline call %d = %+v, want state %+v and reference %+v",
				index, call, wantState, storedObjectReference(mode.IdleBaseline))
		}
	}

	assertPreparedSelection(t, prepared, measurement)
	if prepared.PowerSource.Kind != metrics.result.Kind ||
		prepared.PowerSource.Endpoint != metrics.result.Endpoint ||
		prepared.PowerSource.Metric != metrics.result.PowerMetric ||
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

	catalogs.class.Spec.PowerModes[1].Actuator.JetsonNVPModel.ID = 99
	assertPreparedDirection(t, prepared.Directions[0], "low", "high", "to-high", 2, highReference, 12)
	assertPreparedDirection(t, prepared.Directions[1], "high", "low", "to-low", 1, lowReference, 8)
}

func assertTransitionMaterialResolution(
	t *testing.T,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	catalogs *recordingDeviceClassReader,
	nodes *recordingNodeResolver,
	actuation *recordingActuationPolicyResolver,
) {
	t.Helper()
	if catalogs.calls != 1 || catalogs.keys[0] != (types.NamespacedName{Name: measurement.Spec.DeviceClass}) {
		t.Fatalf("DeviceClass reads = calls:%d keys:%v", catalogs.calls, catalogs.keys)
	}
	if nodes.calls != 1 || nodes.selection.Name != measurement.Spec.TargetNode ||
		nodes.selection.DeviceArchitecture != "arm64" ||
		nodes.selection.DeviceClassSelector["edge.dacs.io/device-class"] != measurement.Spec.DeviceClass {
		t.Fatalf("Node resolution = calls:%d selection:%+v", nodes.calls, nodes.selection)
	}
	if actuation.calls != 2 {
		t.Fatalf("actuation-policy calls = %d, want 2", actuation.calls)
	}
	for index, mode := range measurement.Spec.Modes {
		selection := actuation.selections[index]
		if selection.DeviceClass != measurement.Spec.DeviceClass || selection.PowerMode != mode.PowerMode ||
			selection.Actuator.Kind != profilingrun.ActuatorKindJetsonNVPModel ||
			selection.Actuator.JetsonNVPModel == nil || selection.Actuator.JetsonNVPModel.ID != int32(index+1) {
			t.Fatalf("actuation selection %d = %+v", index, selection)
		}
	}
}

func assertPreparedSelection(
	t *testing.T,
	prepared PreparedMeasurement,
	measurement *edgev1alpha1.PowerModeTransitionCost,
) {
	t.Helper()
	if prepared.Namespace != measurement.Namespace ||
		prepared.TargetNode != (edgev1alpha1.ProfilingRunObjectReference{Name: measurement.Spec.TargetNode, UID: "node-uid"}) ||
		prepared.ObserverNodeName != "observer" ||
		prepared.DeviceClass != measurement.Spec.DeviceClass ||
		prepared.ExecutionPath != measurement.Spec.ExecutionPath ||
		prepared.ServingService != measurement.Spec.ServingService ||
		prepared.ServingContainer != measurement.Spec.ServingContainer {
		t.Fatalf("prepared operational selection = %#v", prepared)
	}
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

func TestPreparerStopsBeforeObservationResolutionWhenTransitionMaterialFails(t *testing.T) {
	tests := []struct {
		name         string
		configure    func(*recordingDeviceClassReader, *recordingNodeResolver, *recordingActuationPolicyResolver, *edgev1alpha1.PowerModeTransitionCost)
		wantNode     int
		wantPolicies int
	}{
		{name: "DeviceClass read", configure: func(c *recordingDeviceClassReader, _ *recordingNodeResolver, _ *recordingActuationPolicyResolver, _ *edgev1alpha1.PowerModeTransitionCost) {
			c.class = nil
		}},
		{name: "undeclared mode", configure: func(_ *recordingDeviceClassReader, _ *recordingNodeResolver, _ *recordingActuationPolicyResolver, m *edgev1alpha1.PowerModeTransitionCost) {
			m.Spec.Modes[0].PowerMode = "missing"
		}},
		{name: "Node", wantNode: 1, configure: func(_ *recordingDeviceClassReader, n *recordingNodeResolver, _ *recordingActuationPolicyResolver, _ *edgev1alpha1.PowerModeTransitionCost) {
			n.err = errors.New("Node unavailable")
		}},
		{name: "policy resolver", wantNode: 1, wantPolicies: 1, configure: func(_ *recordingDeviceClassReader, _ *recordingNodeResolver, a *recordingActuationPolicyResolver, _ *edgev1alpha1.PowerModeTransitionCost) {
			a.err = errors.New("policy unavailable")
		}},
		{name: "configured policy name", wantNode: 1, wantPolicies: 1, configure: func(_ *recordingDeviceClassReader, _ *recordingNodeResolver, a *recordingActuationPolicyResolver, _ *edgev1alpha1.PowerModeTransitionCost) {
			wrong := a.results["low"]
			wrong.Policy.Name = "different-policy"
			a.results["low"] = wrong
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measurement := validTransitionMeasurement()
			catalogs := newRecordingDeviceClassReader(validDeviceClass())
			nodes := validNodeResolver()
			metrics := &recordingTransitionMetricResolver{}
			baselines := &recordingIdleBaselineReader{}
			actuation := validActuationPolicyResolver()
			test.configure(catalogs, nodes, actuation, measurement)
			_, err := mustPreparerWith(t, catalogs, nodes, metrics, baselines, actuation).
				Prepare(context.Background(), measurement)
			if err == nil || catalogs.calls != 1 || nodes.calls != test.wantNode ||
				actuation.calls != test.wantPolicies || metrics.calls != 0 || len(baselines.calls) != 0 {
				t.Fatalf("failure/calls = err:%v catalogs:%d nodes:%d policies:%d metrics:%d baselines:%d",
					err, catalogs.calls, nodes.calls, actuation.calls, metrics.calls, len(baselines.calls))
			}
		})
	}
}

func TestPreparerRejectsTargetLocalObserverBeforeResolution(t *testing.T) {
	catalogs := newRecordingDeviceClassReader(validDeviceClass())
	nodes := validNodeResolver()
	metrics := &recordingTransitionMetricResolver{}
	baselines := &recordingIdleBaselineReader{}
	actuation := validActuationPolicyResolver()
	preparer, err := NewPreparer(
		catalogs, nodes, metrics, baselines, actuation, validTransitionMeasurement().Spec.TargetNode,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparer.Prepare(context.Background(), validTransitionMeasurement()); err == nil {
		t.Fatal("target-local observer was accepted")
	}
	if catalogs.calls != 0 || nodes.calls != 0 || metrics.calls != 0 || len(baselines.calls) != 0 || actuation.calls != 0 {
		t.Fatal("target-local observer reached an external resolver")
	}
}

func TestPreparerRequiresExperimentPowerLabels(t *testing.T) {
	measurement := validTransitionMeasurement()
	tests := map[string]map[string]string{
		"missing labels":      nil,
		"missing hostname":    {"device_type": testDeviceType},
		"missing device type": {"hostname": "meter-target"},
		"extra label": {
			"hostname": "meter-target", "device_type": testDeviceType, "extra": "value",
		},
		"unclean device type": {"hostname": "meter-target", "device_type": " padded "},
	}
	for name, labels := range tests {
		t.Run(name, func(t *testing.T) {
			metrics := &recordingTransitionMetricResolver{result: profiler.ResolvedMetricSource{
				Kind: "edge-metrics", MetricLabels: labels,
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
	actuatorID int32,
	reference idlebaseline.StoredObjectReference,
	meanWallWatts float64,
) {
	t.Helper()
	contract := direction.FrozenContract
	if contract.SourceState.PowerMode != sourceMode || contract.TargetState.PowerMode != targetMode ||
		contract.SourceState.DeviceType != testDeviceType ||
		contract.TargetState.Model != "model-under-test" ||
		contract.TargetIdleBaseline.State != contract.TargetState ||
		contract.TargetIdleBaseline.MeanWallWatts != meanWallWatts ||
		direction.Actuator.Kind != edgev1alpha1.PowerModeActuatorJetsonNVPModel ||
		direction.Actuator.JetsonNVPModel == nil ||
		direction.Actuator.JetsonNVPModel.ID != actuatorID ||
		direction.ActuationPolicy.Policy.Name != actuationPolicy ||
		direction.ActuationPolicy.Policy.PowerMode != targetMode ||
		direction.ActuationPolicy.Policy.SettlingDurationSeconds != int(actuatorID)*10 ||
		direction.ActuationPolicy.Policy.MaxActuationDurationSeconds != 300 ||
		direction.ActuationPolicy.Source.Name != "profiler-facts" {
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
	state     idlebaseline.MeasurementState
	reference idlebaseline.StoredObjectReference
}

type recordingIdleBaselineReader struct {
	results map[string]ResolvedIdleBaseline
	err     error
	calls   []baselineReadCall
}

func (r *recordingIdleBaselineReader) Read(
	_ context.Context,
	state idlebaseline.MeasurementState,
	reference idlebaseline.StoredObjectReference,
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
	return mustPreparerWith(
		t,
		newRecordingDeviceClassReader(validDeviceClass()),
		validNodeResolver(),
		metrics,
		baselines,
		validActuationPolicyResolver(),
	)
}

func mustPreparerWith(
	t *testing.T,
	catalogs client.Reader,
	nodes profiler.NodeResolver,
	metrics profiler.MetricSourceResolver,
	baselines IdleBaselineReader,
	actuation profiler.ActuationPolicyResolver,
) *Preparer {
	t.Helper()
	preparer, err := NewPreparer(catalogs, nodes, metrics, baselines, actuation, "observer")
	if err != nil {
		t.Fatal(err)
	}
	return preparer
}

type recordingDeviceClassReader struct {
	class *edgev1alpha1.DeviceClass
	calls int
	keys  []types.NamespacedName
}

func (r *recordingDeviceClassReader) Get(
	ctx context.Context,
	key client.ObjectKey,
	object client.Object,
	options ...client.GetOption,
) error {
	r.calls++
	r.keys = append(r.keys, key)
	if r.class == nil {
		return errors.New("DeviceClass unavailable")
	}
	*object.(*edgev1alpha1.DeviceClass) = *r.class.DeepCopy()
	return nil
}

func (r *recordingDeviceClassReader) List(
	context.Context,
	client.ObjectList,
	...client.ListOption,
) error {
	return errors.New("unexpected DeviceClass list")
}

func newRecordingDeviceClassReader(class *edgev1alpha1.DeviceClass) *recordingDeviceClassReader {
	return &recordingDeviceClassReader{class: class}
}

type recordingNodeResolver struct {
	result    profilingrun.NodeSnapshot
	err       error
	calls     int
	selection profiler.NodeSelection
}

func (r *recordingNodeResolver) ResolveNode(
	_ context.Context,
	selection profiler.NodeSelection,
) (profilingrun.NodeSnapshot, error) {
	r.calls++
	r.selection = selection
	return r.result, r.err
}

func validNodeResolver() *recordingNodeResolver {
	return &recordingNodeResolver{result: profilingrun.NodeSnapshot{
		Name: "node-under-test", UID: "node-uid", ResourceVersion: "7",
		Architecture: "arm64", AllocatableCPU: "8",
	}}
}

type recordingActuationPolicyResolver struct {
	results    map[string]profiler.FrozenActuationPolicy
	err        error
	calls      int
	selections []profiler.ActuationSelection
}

func (r *recordingActuationPolicyResolver) ResolveActuationPolicy(
	_ context.Context,
	selection profiler.ActuationSelection,
) (profiler.FrozenActuationPolicy, error) {
	r.calls++
	r.selections = append(r.selections, selection)
	if r.err != nil {
		return profiler.FrozenActuationPolicy{}, r.err
	}
	policy, found := r.results[selection.PowerMode]
	if !found {
		return profiler.FrozenActuationPolicy{}, errors.New("unexpected actuation-policy mode")
	}
	return policy, nil
}

func validActuationPolicyResolver() *recordingActuationPolicyResolver {
	return &recordingActuationPolicyResolver{results: map[string]profiler.FrozenActuationPolicy{
		"low":  frozenActuationPolicy("to-low", "low", 10),
		"high": frozenActuationPolicy("to-high", "high", 20),
	}}
}

func frozenActuationPolicy(name, mode string, settlingSeconds int) profiler.FrozenActuationPolicy {
	return profiler.FrozenActuationPolicy{
		SchemaVersion: profiler.FrozenActuationPolicySchemaVersion,
		Source: profiler.ActuationPolicySource{
			Kind: profiler.ConfigMapActuationPolicySourceKind, Namespace: "chill-system",
			Name: "profiler-facts", UID: "facts-uid", ResourceVersion: "11", Key: "actuation-policies.yaml",
		},
		Policy: profiler.ActuationPolicy{
			SchemaVersion: profiler.ActuationPolicySchemaVersion,
			ActuationPolicySpec: profiler.ActuationPolicySpec{
				Name: name, DeviceClass: "class-under-test", PowerMode: mode,
				ActuatorKind:            profilingrun.ActuatorKindJetsonNVPModel,
				SettlingDurationSeconds: settlingSeconds, MaxActuationDurationSeconds: 300,
			},
		},
	}
}

func validDeviceClass() *edgev1alpha1.DeviceClass {
	return &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "class-under-test"},
		Spec: edgev1alpha1.DeviceClassSpec{
			Architecture: "arm64",
			NodeSelector: map[string]string{"edge.dacs.io/device-class": "class-under-test"},
			PowerModes: []edgev1alpha1.PowerMode{
				{Name: "low", Actuator: edgev1alpha1.PowerModeActuator{
					Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
					JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 1},
				}},
				{Name: "high", Actuator: edgev1alpha1.PowerModeActuator{
					Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
					JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 2},
				}},
			},
		},
	}
}

func validTransitionMetricResolver() *recordingTransitionMetricResolver {
	return &recordingTransitionMetricResolver{result: profiler.ResolvedMetricSource{
		Kind: "edge-metrics", Endpoint: "http://meter.example:18766/prometheus?hostname=meter-target",
		PowerMetric:  "wall_power_watts",
		MetricLabels: map[string]string{"hostname": "meter-target", "device_type": testDeviceType},
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
