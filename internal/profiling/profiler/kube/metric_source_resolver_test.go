package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testPowerEndpoint           = "http://edge-device-lattepanda.monitoring.svc.cluster.local:9102/prometheus"
	testPowerMetric             = "configured_wall_power_watts"
	testTransitionPowerEndpoint = "http://central-meter.example.test:18766/prometheus"
	testTransitionPowerMetric   = "experiment_transition_watts"
)

func TestKubernetesMetricSourceResolverResolvesNodeEntry(t *testing.T) {
	resolver := newMetricSourceResolver(t, validMetricSourceConfigMap())
	resolved, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	assertTestMetricSource(t, resolved, true)
	resolved.TransitionPower.Labels["rack"] = "caller-mutation"

	again, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	assertTestMetricSource(t, again, true)
	if again.TransitionPower.Labels["rack"] != "rack-9" {
		t.Fatal("caller mutation changed subsequently resolved transition-power labels")
	}
}

func TestKubernetesMetricSourceResolverPreservesLocalSourceWithoutTransitionPower(t *testing.T) {
	config := validMetricSourceConfigMap()
	config.Data["metric-sources.yaml"] = strings.Replace(
		config.Data["metric-sources.yaml"], testTransitionPowerYAML(), "", 1,
	)
	resolved, err := newMetricSourceResolver(t, config).ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	assertTestMetricSource(t, resolved, false)
}

func TestKubernetesMetricSourceResolverSelectsOnlyRequestedNodeEntry(t *testing.T) {
	base := validMetricSourceConfigMap()
	first := newMetricSourceResolver(t, base.DeepCopy())
	resolved, err := first.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}

	unrelated := base.DeepCopy()
	unrelated.Data["metric-sources.yaml"] += `  - node: jetsonx
    kind: edge-metrics
    endpoint: http://edge-device-jetsonx.monitoring.svc.cluster.local:9102/metrics
    powerMetric: shelly_power_total_watts
    thermalMetrics:
      observationAvailable: device_thermal_throttle_observation_available
      active: device_thermal_throttle_active
      cpuCoreThrottleEvents: device_thermal_cpu_core_throttle_event_count
      cpuPackageThrottleEvents: device_thermal_cpu_package_throttle_event_count
      latched: device_thermal_throttle_latched
      maximumTemperatureCelsius: device_thermal_max_celsius
`
	second, err := newMetricSourceResolver(t, unrelated).ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	assertTestMetricSource(t, second, true)
	if second.Endpoint != resolved.Endpoint ||
		second.TransitionPower.Endpoint != resolved.TransitionPower.Endpoint {
		t.Fatal("unrelated Node entry changed selected metric-source facts")
	}

	changed := base.DeepCopy()
	changed.Data["metric-sources.yaml"] = strings.Replace(
		changed.Data["metric-sources.yaml"], "edge-device-lattepanda", "edge-device-lattepanda-v2", 1,
	)
	third, err := newMetricSourceResolver(t, changed).ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if third.Endpoint == resolved.Endpoint {
		t.Fatal("changed selected entry was not observed")
	}
}

func TestKubernetesMetricSourceResolverFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*corev1.ConfigMap)
		match  string
		is     error
	}{
		{
			name:   "Node unavailable",
			mutate: func(config *corev1.ConfigMap) {},
			is:     ErrMetricSourceUnavailable,
		},
		{
			name: "retired schema",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], metricSourcesSchema, "chill.dacs.io/profiler-metric-sources.v1alpha2", 1,
				)
			},
			match: "unsupported Profiler metric-sources schema",
		},
		{
			name: "unsupported kind",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], edgemetrics.SourceKind, "Other", 1,
				)
			},
			match: "unsupported metric-source kind",
		},
		{
			name: "invalid power metric name",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], testPowerMetric, "not-valid", 1,
				)
			},
			match: "valid Prometheus metric name",
		},
		{
			name: "missing thermal mapping",
			mutate: func(config *corev1.ConfigMap) {
				start := strings.Index(config.Data["metric-sources.yaml"], "    thermalMetrics:")
				config.Data["metric-sources.yaml"] = config.Data["metric-sources.yaml"][:start]
			},
			match: "thermalMetrics.observationAvailable",
		},
		{
			name: "duplicate typed metric",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"],
					"configured_thermal_active", testPowerMetric, 1,
				)
			},
			match: "is duplicated",
		},
		{
			name: "endpoint credentials",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "http://", "http://user:pass@", 1,
				)
			},
			match: "without credentials",
		},
		{
			name: "empty endpoint path",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "/prometheus", "", 1,
				)
			},
			match: "clean nonempty path",
		},
		{
			name: "unclean endpoint path",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "/prometheus", "/nested/../prometheus", 1,
				)
			},
			match: "clean nonempty path",
		},
		{
			name: "invalid transition power metric name",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], testTransitionPowerMetric, "not-valid", 1,
				)
			},
			match: "validate transition power: powerMetric must be a valid Prometheus metric name",
		},
		{
			name: "empty transition power labels",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"],
					"      labels:\n        rack: rack-9\n        outlet: outlet-b\n", "      labels: {}\n", 1,
				)
			},
			match: "validate transition power: exact labels are required",
		},
		{
			name: "invalid transition power label name",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "        rack: rack-9", "        not-valid: rack-9", 1,
				)
			},
			match: `validate transition power: label name "not-valid" is invalid`,
		},
		{
			name: "invalid transition power endpoint",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], testTransitionPowerEndpoint,
					"http://user:pass@central-meter.example.test:18766/prometheus", 1,
				)
			},
			match: "validate transition power: edge-metrics endpoint must be an http URL without credentials",
		},
		{
			name: "duplicate Node",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] += `  - node: lattepanda
    kind: edge-metrics
    endpoint: http://duplicate.monitoring.svc:9102/metrics
    powerMetric: ` + testPowerMetric + `
    thermalMetrics:
      observationAvailable: configured_thermal_observation_available
      active: configured_thermal_active
      cpuCoreThrottleEvents: configured_thermal_cpu_core_events_total
      cpuPackageThrottleEvents: configured_thermal_cpu_package_events_total
      latched: configured_thermal_latched
      maximumTemperatureCelsius: configured_thermal_max_celsius
`
			},
			match: "duplicate metric source",
		},
		{
			name: "second YAML document",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] += "---\nschemaVersion: ignored\n"
			},
			match: "exactly one YAML document",
		},
		{
			name: "unknown field",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] += "unknown: value\n"
			},
			match: `unknown field "unknown"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validMetricSourceConfigMap()
			test.mutate(config)
			node := "lattepanda"
			if test.is != nil {
				node = "missing"
			}
			_, err := newMetricSourceResolver(t, config).ResolveMetricSource(context.Background(), node)
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("expected %v, got %v", test.is, err)
			}
			if test.match != "" && (err == nil || !strings.Contains(err.Error(), test.match)) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestKubernetesMetricSourceResolverPreservesNotFound(t *testing.T) {
	resolver := newMetricSourceResolver(t)
	_, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err == nil || !apierrors.IsNotFound(err) {
		t.Fatalf("expected ConfigMap NotFound cause, got %v", err)
	}
}

func TestNewKubernetesMetricSourceResolverValidatesDependencies(t *testing.T) {
	if _, err := NewKubernetesMetricSourceResolver(nil, metricSourceResolverOptions()); err == nil {
		t.Fatal("expected nil reader rejection")
	}
	options := metricSourceResolverOptions()
	options.ConfigMapKey = " "
	if _, err := NewKubernetesMetricSourceResolver(metricSourceFakeReader(t), options); err == nil {
		t.Fatal("expected invalid options rejection")
	}
}

func newMetricSourceResolver(t *testing.T, objects ...client.Object) *KubernetesMetricSourceResolver {
	t.Helper()
	resolver, err := NewKubernetesMetricSourceResolver(metricSourceFakeReader(t, objects...), metricSourceResolverOptions())
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func metricSourceFakeReader(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func metricSourceResolverOptions() MetricSourceResolverOptions {
	return MetricSourceResolverOptions{
		Namespace: "chill-system", ConfigMapName: "chill-profiler-facts", ConfigMapKey: "metric-sources.yaml",
	}
}

func validMetricSourceConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "chill-profiler-facts",
			UID: types.UID("config-uid"), ResourceVersion: "30",
		},
		Data: map[string]string{"metric-sources.yaml": `schemaVersion: chill.dacs.io/profiler-metric-sources.v1alpha3
sources:
  - node: lattepanda
    kind: edge-metrics
    endpoint: ` + testPowerEndpoint + `
    powerMetric: ` + testPowerMetric + `
` + testTransitionPowerYAML() + `    thermalMetrics:
      observationAvailable: configured_thermal_observation_available
      active: configured_thermal_active
      cpuCoreThrottleEvents: configured_thermal_cpu_core_events_total
      cpuPackageThrottleEvents: configured_thermal_cpu_package_events_total
      latched: configured_thermal_latched
      maximumTemperatureCelsius: configured_thermal_max_celsius
`},
	}
}

func testTransitionPowerYAML() string {
	return `    transitionPower:
      endpoint: ` + testTransitionPowerEndpoint + `
      powerMetric: ` + testTransitionPowerMetric + `
      labels:
        rack: rack-9
        outlet: outlet-b
`
}

func assertTestMetricSource(t *testing.T, resolved profiler.ResolvedMetricSource, wantTransitionPower bool) {
	t.Helper()
	if resolved.Kind != edgemetrics.SourceKind || resolved.Endpoint != testPowerEndpoint ||
		resolved.PowerMetric != testPowerMetric || resolved.ThermalMetrics != testThermalMetricContract() {
		t.Fatalf("unexpected resolved local metric source: %#v", resolved)
	}
	if !wantTransitionPower {
		if resolved.TransitionPower != nil {
			t.Fatalf("unexpected transition-power selector: %#v", resolved.TransitionPower)
		}
		return
	}
	if resolved.TransitionPower == nil ||
		resolved.TransitionPower.Endpoint != testTransitionPowerEndpoint ||
		resolved.TransitionPower.PowerMetric != testTransitionPowerMetric ||
		len(resolved.TransitionPower.Labels) != 2 ||
		resolved.TransitionPower.Labels["rack"] != "rack-9" ||
		resolved.TransitionPower.Labels["outlet"] != "outlet-b" {
		t.Fatalf("unexpected resolved transition-power selector: %#v", resolved.TransitionPower)
	}
}

func testThermalMetricContract() profilingrun.ThermalMetricContract {
	return profilingrun.ThermalMetricContract{
		ObservationAvailable:      "configured_thermal_observation_available",
		Active:                    "configured_thermal_active",
		CPUCoreThrottleEvents:     "configured_thermal_cpu_core_events_total",
		CPUPackageThrottleEvents:  "configured_thermal_cpu_package_events_total",
		Latched:                   "configured_thermal_latched",
		MaximumTemperatureCelsius: "configured_thermal_max_celsius",
	}
}
