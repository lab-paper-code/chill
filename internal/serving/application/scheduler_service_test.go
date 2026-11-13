package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// cleanObservationDirectory models an explicitly clean predecessor, not crash recovery.
func cleanObservationDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "async-run.json"),
		[]byte(`{"started":"2026-11-05T00:00:00Z","clean":true,"gaps":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func withSchedulerEstimateFixture(settings serving.SchedulerSettings) serving.SchedulerSettings {
	settings.DefaultOperationCost = map[string]serving.OperationCostValue{
		"prepare": {Seconds: 852, Joules: 36371},
		"remove":  {Seconds: 13, Joules: 1014},
		"power":   {Seconds: 166, Joules: 2229},
	}
	settings.DefaultIdlePower = &serving.IdlePowerDefault{NodeWatts: 54, PerInstanceWatts: 8}
	settings.SLOPriorityStrategy, settings.WorkloadPriority = "excess", []string{}
	return settings
}

func TestSchedulerConfigurationRequiresActualEvidenceAndInventory(t *testing.T) {
	settings := withSchedulerEstimateFixture(serving.SchedulerSettings{
		ObservationWindow: time.Second,
		ControlInterval:   time.Second, ComparisonPeriod: time.Second,
		MaxReplicas:  1,
		MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
		CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
		PlanSearchStrategy: "v1", PlanSearchEvaluations: 500000, PlanExactFinalists: 16, PlanPolishActive: 4,
		CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
	})
	rawSettings, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{name: "explicit controls-off configuration", fields: `"reserved":{"node":{"memoryBytes":1024}}`, valid: true},
		{name: "retired reserved CPU", fields: `"reserved":{"node":{"cpus":[0]}}`},
		{name: "retired reserved CPU quota", fields: `"reserved":{"node":{"cpuCount":1,"cpuReserved":true}}`},
		{name: "retired CPU inventory", fields: `"cpuInventory":{"node":[0]}`},
		{name: "retired GPU inventory", fields: `"gpuInventory":{"node":["GPU-real"]}`},
		{name: "fake cost injection", fields: `"costs":[{"id":"fake","trials":[{"joules":0}]}]`},
		{name: "retired meters", fields: `"meters":[]`},
		{name: "invalid power polling", fields: `"powerObservation":{"sampling":{"Interval":0,` +
			`"Duration":1000000000,"RequestTimeout":1},"coverage":` +
			`{"MaximumBoundaryDistance":1,"MinimumCoverage":1,"MaximumGap":1}}`},
		{name: "power boundary distance at one minute", valid: true, fields: `"powerObservation":{"sampling":` +
			`{"Interval":1000000000,"Duration":1000000000,"RequestTimeout":1},"coverage":` +
			`{"MaximumBoundaryDistance":60000000000,"MinimumCoverage":1,"MaximumGap":1}}`},
		{name: "power boundary distance over one minute", fields: `"powerObservation":{"sampling":` +
			`{"Interval":1000000000,"Duration":1000000000,"RequestTimeout":1},"coverage":` +
			`{"MaximumBoundaryDistance":60000000001,"MinimumCoverage":1,"MaximumGap":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"settings":` + string(rawSettings) + `,` + tc.fields + `}`
			_, err := DecodeSchedulerConfiguration(raw, 1<<20)
			if (err == nil) != tc.valid {
				t.Fatalf("configuration valid=%t error=%v", tc.valid, err)
			}
		})
	}
	if _, err = DecodeSchedulerConfiguration(`{"settings":`+string(rawSettings)+`} {}`, 1<<20); err == nil {
		t.Fatal("trailing configuration accepted")
	}
	if _, err = DecodeSchedulerConfiguration(`{"settings":`+string(rawSettings)+`}`, 1); err == nil {
		t.Fatal("over-limit configuration accepted")
	}
	var omitted map[string]json.RawMessage
	if err = json.Unmarshal(rawSettings, &omitted); err != nil || string(omitted["completionMarginMS"]) != "0" {
		t.Fatalf("zero margin fixture: %s %v", rawSettings, err)
	}
	delete(omitted, "completionMarginMS")
	withoutMargin, err := json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSchedulerConfiguration(`{"settings":`+string(withoutMargin)+`}`, 1<<20); err == nil ||
		!strings.Contains(err.Error(), "completionMarginMS required") {
		t.Fatalf("omitted completionMarginMS accepted: %v", err)
	}
	// Each capacity and plan-search setting is required; an omitted one decodes as zero and fails validation.
	fields := []string{"capacityWindowNanos", "capacityWindows", "capacityPassingWindows", "capacityMinimumRequests",
		"planSearchWorkers"}
	for _, field := range fields {
		var without map[string]json.RawMessage
		if err = json.Unmarshal(rawSettings, &without); err != nil {
			t.Fatal(err)
		}
		delete(without, field)
		raw, err := json.Marshal(without)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeSchedulerConfiguration(`{"settings":`+string(raw)+`}`, 1<<20); err == nil {
			t.Fatalf("settings without %s accepted", field)
		}
	}
	// Dispatch occupancy is measured; a configured one is an unknown field.
	var with map[string]json.RawMessage
	if err = json.Unmarshal(rawSettings, &with); err != nil {
		t.Fatal(err)
	}
	with["dispatchOccupancy"] = json.RawMessage(`[]`)
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSchedulerConfiguration(`{"settings":`+string(raw)+`}`, 1<<20); err == nil {
		t.Fatal("settings with dispatchOccupancy accepted")
	}
}

func TestStaticRoutingRemainsReadableWithoutPhysicalApplication(t *testing.T) {
	config := schedulerTestConfiguration()
	config.StaticRouting = map[string]map[string]int{"current": {"unavailable-physical": 1}, "unrouted": {}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSchedulerConfiguration(string(raw), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	service := schedulerTestService(t, decoded)
	for range 2 {
		// A stored declaration is a GET projection, not an application/retry request.
		if service.configuration.Load().StaticRouting["current"]["unavailable-physical"] != 1 {
			t.Fatal("stored declaration lost")
		}
		routing, _ := service.execution.SchedulerRouting()
		if len(routing) != 0 || len(service.Controller.State().Results) != 0 {
			t.Fatal("stored declaration fabricated physical routing or an operation")
		}
	}
}

type schedulerPowerArchive struct{ directory string }

func (a schedulerPowerArchive) StreamImmutable(
	_ context.Context, location evidencestore.ObjectLocation, produce func(io.Writer) error,
) (evidencestore.Receipt, error) {
	output := filepath.Join(a.directory, location.Key)
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	err = produce(file)
	info, statErr := file.Stat()
	err = errors.Join(err, statErr, file.Sync(), file.Close())
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	identity := evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: info.Size()}
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        identity, Outcome: evidencestore.CreateOutcomeCreated,
	}, nil
}

func TestOperatingPowerReadsAcceptedConfigurationWhileServiceIsBusy(t *testing.T) {
	from := time.Date(2026, 11, 5, 2, 59, 59, 432000000, time.UTC)
	to := from.Add(time.Second)
	now := from
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err = observationLog.SchedulerRecord(observation.SchedulerObservation{
		At: from, Snapshot: &serving.SchedulerSnapshot{At: from},
	}); err != nil {
		t.Fatal(err)
	}
	policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MaximumGap: time.Second, MinimumCoverage: 1}
	configurations := make([]*SchedulerConfiguration, 0, 2)
	for index, name := range []string{"first", "replacement"} {
		node := serving.NodeReference{Name: name, UID: name + "-uid"}
		source := metricobserver.SourceIdentity{
			Kind: "edge-metrics", Endpoint: "http://meter/" + name, Metric: "wall_watts", NodeName: name,
		}
		meter := servingops.MeterDeclaration{Node: node, ID: name + "-wall", Scope: "node-exclusive-wall", Source: source}
		watts := float64((index + 1) * 10)
		power := metricobserver.Result{Source: source, StartedAt: from, EndedAt: to,
			Samples: []metricobserver.Sample{{ObservedAt: from, Watts: watts}, {ObservedAt: to, Watts: watts}},
		}
		if err = observationLog.SchedulerRecord(observation.SchedulerObservation{
			At: to, Power: &power, PowerCoverage: &policy, Node: &node, Meter: meter.ID, PowerScope: meter.Scope,
		}); err != nil {
			t.Fatal(err)
		}
		configurations = append(configurations, &SchedulerConfiguration{Meters: []servingops.MeterDeclaration{meter}})
	}
	now = to
	service := &SchedulerService{observationLog: observationLog}
	// Model a refresh or control Tick holding the owner lock throughout this read.
	service.Lock.Lock()
	defer service.Lock.Unlock()
	for index, config := range configurations {
		service.configuration.Store(config)
		type response struct {
			result observation.OperatingPowerWindow
			err    error
		}
		done := make(chan response, 1)
		go func() {
			result, err := service.OperatingPower(context.Background(), from, to, 10)
			done <- response{result: result, err: err}
		}()
		select {
		case got := <-done:
			wantJoules := float64((index + 1) * 10)
			if got.err != nil || !got.result.Complete || len(got.result.Nodes) != 1 ||
				got.result.Nodes[0].Node != config.Meters[0].Node || got.result.WholePoolJoules == nil ||
				*got.result.WholePoolJoules != wantJoules {
				t.Fatalf("power query lost accepted meter snapshot while owner busy: %+v %v", got.result, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("power query waited behind the owner refresh/control lock")
		}
	}
}

func TestRejectedSchedulerRefreshPreservesPublishedConfiguration(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "scheduler", Name: "configuration", ResourceVersion: "replacement"},
		Data:       map[string]string{"configuration": "{"},
	})
	service := &SchedulerService{
		kube: client, observer: &servingkube.Observer{}, resourceVersion: "accepted",
		opts: Options{
			ServingNamespace: "scheduler", SchedulerConfigMap: "configuration",
			SchedulerConfigKey: "configuration", MaxRequestBytes: 1024,
		},
	}
	accepted := &SchedulerConfiguration{Meters: []servingops.MeterDeclaration{{ID: "accepted-meter"}}}
	service.configuration.Store(accepted)
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("malformed replacement configuration was accepted")
	}
	if service.configuration.Load() != accepted || service.resourceVersion != "accepted" {
		t.Fatal("rejected refresh changed the published configuration")
	}
	if got := service.SchedulerMeters(); len(got) != 1 || got[0].ID != "accepted-meter" {
		t.Fatalf("settings projected a rejected meter declaration: %+v", got)
	}
}

func TestSchedulerPowerUsesExistingEvidenceEnvelopeBeforeSourceReads(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		var calls atomic.Int32
		secondRead := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 2 {
				close(secondRead)
			}
			_, _ = w.Write([]byte("# TYPE wall_watts gauge\nwall_watts{hostname=\"node\",device_type=\"fixture\"} 5\n"))
		}))
		log, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: cleanObservationDirectory(t), Store: "evidence",
			Retention: time.Hour, Writer: schedulerPowerArchive{directory: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		service := &SchedulerService{ctx: ctx, observationLog: log, opts: Options{MaxEvidenceBytes: 1}}
		if accepted {
			service.opts.MaxEvidenceBytes = 64 << 20
		}
		source := metricobserver.SourceIdentity{
			Kind: "edge-metrics", NodeName: "node", Endpoint: server.URL, Metric: "wall_watts",
			MetricLabels: map[string]string{"hostname": "node", "device_type": "fixture"}}
		start := time.Now().UTC()
		service.restartPower(SchedulerConfiguration{
			Meters: []servingops.MeterDeclaration{{
				ID: "meter", Node: serving.NodeReference{Name: "node", UID: "uid"},
				Scope: "node-exclusive-wall", Source: source,
			}},
			PowerObservation: &powerObservationConfig{
				Sampling: metricobserver.Request{
					Interval: 20 * time.Millisecond, Duration: 200 * time.Millisecond,
					RequestTimeout: 10 * time.Millisecond,
				},
				Coverage: powerwindow.Policy{
					MaximumBoundaryDistance: 100 * time.Millisecond,
					MaximumGap:              100 * time.Millisecond, MinimumCoverage: 1,
				},
			},
		})
		timedOut := false
		if accepted {
			select {
			case <-secondRead:
			case <-ctx.Done():
				timedOut = true
			}
		}
		cancel()
		service.powerCancel()
		service.powerWG.Wait()
		if timedOut {
			t.Error("admitted power schedule never read the source")
		}
		if !accepted && calls.Load() != 0 {
			t.Error("undersized evidence envelope read the source")
		}
		if accepted {
			events, _, _, _, readErr := log.SchedulerObservationPage(
				context.Background(), start, time.Now().UTC().Add(time.Second),
				10, 0, observation.SchedulerObservationSelection{},
			)
			if readErr != nil || len(events) != 1 || events[0].Power == nil || len(events[0].Power.Samples) == 0 {
				t.Errorf("power evidence lost: events=%+v error=%v", events, readErr)
			}
		}
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
		server.Close()
	}
}

// Defect 23: evaluation preflight reads the accepted settings; it must not wait behind
// refresh or Tick, which hold Lock for a whole capacity search.
func TestAcceptedSettingsReadWhileServiceIsBusy(t *testing.T) {
	service := &SchedulerService{}
	if _, ok := service.AcceptedSettings(); ok {
		t.Fatal("settings reported before any configuration was accepted")
	}
	accepted := &SchedulerConfiguration{Settings: serving.SchedulerSettings{ControlInterval: time.Minute}}
	service.configuration.Store(accepted)
	service.Lock.Lock()
	defer service.Lock.Unlock()
	done := make(chan serving.SchedulerSettings, 1)
	go func() {
		settings, _ := service.AcceptedSettings()
		done <- settings
	}()
	select {
	case settings := <-done:
		if settings.ControlInterval != time.Minute {
			t.Fatalf("settings differ from the accepted configuration: %+v", settings)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("settings read waited behind the owner refresh/control lock")
	}
}

// A measurement deletion holds the refresh lock through store I/O; the main
// loop's periodic refresh must return instead of waiting behind it.
func TestRefreshTickYieldsToHeldRefreshLock(t *testing.T) {
	service := &SchedulerService{}
	service.Profiles.Store(&ProfilesView{})
	service.RefreshLock.Lock()
	defer service.RefreshLock.Unlock()
	done := make(chan error, 1)
	go func() { done <- service.Refresh(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh tick waited behind a held refresh lock")
	}
}
