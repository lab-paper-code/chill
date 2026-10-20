package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSchedulerConfigurationRequiresActualEvidenceAndInventory(t *testing.T) {
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second,
		MaxObservationAge: time.Second, MaxReplicas: 1,
		MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
		CapacityMinimumRequests: 1, MaxCPUs: 2,
	}
	rawSettings, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{name: "explicit controls-off configuration", fields: `"cpuInventory":{"node":[0,2]},` +
			`"gpuInventory":{"node":["GPU-real"]},"reserved":{"node":{"cpus":[2]}}`, valid: true},
		{name: "duplicate physical CPU", fields: `"cpuInventory":{"node":[0,0]}`},
		{name: "negative physical CPU", fields: `"cpuInventory":{"node":[-1]}`},
		{name: "unobserved reserved CPU", fields: `"cpuInventory":{"node":[0]},"reserved":{"node":{"cpus":[2]}}`},
		{name: "duplicate GPU identity", fields: `"gpuInventory":{"node":["GPU-real","GPU-real"]}`},
		{name: "fake cost injection", fields: `"costs":[{"id":"fake","trials":[{"joules":0}]}]`},
		{name: "invalid meter scope", fields: `"meters":[{"id":"wall","node":{"name":"node","uid":"uid"},` +
			`"scope":"inferred","source":{"nodeName":"node"}}]`},
		{name: "invalid power polling", fields: `"powerObservation":{"sampling":{"Interval":0,` +
			`"Duration":1000000000,"RequestTimeout":1,"MaxEvents":10},"coverage":` +
			`{"MaximumBoundaryDistance":1,"MinimumCoverage":1,"MaximumGap":1}}`},
		{name: "power boundary distance at one minute", valid: true, fields: `"powerObservation":{"sampling":` +
			`{"Interval":1000000000,"Duration":1000000000,"RequestTimeout":1,"MaxEvents":10},"coverage":` +
			`{"MaximumBoundaryDistance":60000000000,"MinimumCoverage":1,"MaximumGap":1}}`},
		{name: "power boundary distance over one minute", fields: `"powerObservation":{"sampling":` +
			`{"Interval":1000000000,"Duration":1000000000,"RequestTimeout":1,"MaxEvents":10},"coverage":` +
			`{"MaximumBoundaryDistance":60000000001,"MinimumCoverage":1,"MaximumGap":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"settings":` + string(rawSettings) + `,` + tc.fields + `}`
			_, err := decodeSchedulerConfiguration(raw, 1<<20)
			if (err == nil) != tc.valid {
				t.Fatalf("configuration valid=%t error=%v", tc.valid, err)
			}
		})
	}
	if _, err = decodeSchedulerConfiguration(`{"settings":`+string(rawSettings)+`} {}`, 1<<20); err == nil {
		t.Fatal("trailing configuration accepted")
	}
	if _, err = decodeSchedulerConfiguration(`{"settings":`+string(rawSettings)+`}`, 1); err == nil {
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
	if _, err = decodeSchedulerConfiguration(`{"settings":`+string(withoutMargin)+`}`, 1<<20); err == nil ||
		!strings.Contains(err.Error(), "completionMarginMS required") {
		t.Fatalf("omitted completionMarginMS accepted: %v", err)
	}
	// Each capacity setting is required; an omitted one decodes as zero and fails validation.
	fields := []string{"capacityWindowNanos", "capacityWindows", "capacityPassingWindows", "capacityMinimumRequests"}
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
		if _, err = decodeSchedulerConfiguration(`{"settings":`+string(raw)+`}`, 1<<20); err == nil {
			t.Fatalf("settings without %s accepted", field)
		}
	}
}

func TestStaticRoutingAcknowledgementDoesNotReapply(t *testing.T) {
	for _, test := range []struct {
		name          string
		declared      bool
		appliedRV     string
		wantAppliedRV string
	}{
		{name: "completed revision is not reapplied", declared: true, appliedRV: "42", wantAppliedRV: "42"},
		{name: "declaration removal clears acknowledgement", appliedRV: "41"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &schedulerService{resourceVersion: "42", staticAppliedRV: test.appliedRV}
			config := &schedulerConfiguration{}
			if test.declared {
				config.StaticRouting = map[string]map[string]int{"current": {"physical": 1}}
			}
			service.configuration.Store(config)
			// No controller is installed: these cases must not attempt application.
			service.applyStaticRouting(context.Background())
			if service.staticAppliedRV != test.wantAppliedRV {
				t.Fatalf("static application acknowledgement = %q, want %q", service.staticAppliedRV, test.wantAppliedRV)
			}
		})
	}
}

type schedulerPowerArchive struct{ directory string }

func (a schedulerPowerArchive) PutImmutable(
	_ context.Context, identity evidencestore.ObjectIdentity, input io.Reader,
) (evidencestore.Receipt, error) {
	output := filepath.Join(a.directory, identity.Key)
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	n, err := io.Copy(file, input)
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil || n != identity.Size {
		return evidencestore.Receipt{}, errors.Join(err, errors.New("archive incomplete"))
	}
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        identity, Outcome: evidencestore.CreateOutcomeCreated,
	}, nil
}

func TestOperatingPowerReadsAcceptedConfigurationWhileServiceIsBusy(t *testing.T) {
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence",
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	from := time.Now().UTC()
	to := from.Add(time.Second)
	if err = observationLog.SchedulerRecord(serving.SchedulerObservation{
		At: from, Snapshot: &serving.SchedulerSnapshot{At: from},
	}); err != nil {
		t.Fatal(err)
	}
	policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MaximumGap: time.Second, MinimumCoverage: 1}
	configurations := make([]*schedulerConfiguration, 0, 2)
	for index, name := range []string{"first", "replacement"} {
		node := serving.NodeReference{Name: name, UID: name + "-uid"}
		source := metricobserver.SourceIdentity{
			Kind: "edge-metrics", Endpoint: "http://meter/" + name, Metric: "wall_watts", NodeName: name,
		}
		meter := servingkube.MeterDeclaration{Node: node, ID: name + "-wall", Scope: "node-exclusive-wall", Source: source}
		watts := float64((index + 1) * 10)
		power := metricobserver.Result{Source: source, StartedAt: from, EndedAt: to,
			Samples: []metricobserver.Sample{{ObservedAt: from, Watts: watts}, {ObservedAt: to, Watts: watts}},
		}
		if err = observationLog.SchedulerRecord(serving.SchedulerObservation{
			At: to, Power: &power, PowerCoverage: &policy, Node: &node, Meter: meter.ID, PowerScope: meter.Scope,
		}); err != nil {
			t.Fatal(err)
		}
		configurations = append(configurations, &schedulerConfiguration{Meters: []servingkube.MeterDeclaration{meter}})
	}
	service := &schedulerService{observationLog: observationLog}
	// Model a refresh or control Tick holding the owner lock throughout this read.
	service.lock.Lock()
	defer service.lock.Unlock()
	for index, config := range configurations {
		service.configuration.Store(config)
		type response struct {
			result serving.OperatingPowerWindow
			err    error
		}
		done := make(chan response, 1)
		go func() {
			result, err := service.operatingPower(from, to, 10)
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
	service := &schedulerService{
		kube: client, observer: &servingkube.Observer{}, resourceVersion: "accepted",
		opts: options{
			servingNamespace: "scheduler", schedulerConfigMap: "configuration",
			schedulerConfigKey: "configuration", maxRequestBytes: 1024,
		},
	}
	accepted := &schedulerConfiguration{Meters: []servingkube.MeterDeclaration{{ID: "accepted-meter"}}}
	service.configuration.Store(accepted)
	if err := service.refresh(context.Background()); err == nil {
		t.Fatal("malformed replacement configuration was accepted")
	}
	if service.configuration.Load() != accepted || service.resourceVersion != "accepted" {
		t.Fatal("rejected refresh changed the published configuration")
	}
}
