package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExecuteInventoryWritesTable(t *testing.T) {
	query := &fakeInventoryQuery{snapshot: testInventorySnapshot()}
	deps := dependencies{newInventoryQuery: func(opts inventoryOptions) (inventoryQuery, error) {
		if opts.kubeconfig != "/tmp/config" || opts.context != "edge" || opts.output != outputFormatTable {
			t.Fatalf("options = %#v", opts)
		}
		return query, nil
	}}
	var stdout strings.Builder

	err := execute(context.Background(), []string{
		"inventory", "--kubeconfig", "/tmp/config", "--context", "edge",
	}, &stdout, io.Discard, deps)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	for _, want := range []string{
		"READ_AT", "classes-rv", "models-rv", "CLASS", "orin", "arm64", "8Gi", "15W",
		"orin-1", "True", "true", "MODEL", "mobilenet", "canonical-onnx", "ort-cpu",
		"onnxruntime", "CPUExecutionProvider",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, stdout.String())
		}
	}
	if query.calls != 1 {
		t.Fatalf("query calls = %d", query.calls)
	}
}

func TestExecuteInventoryWritesStructuredJSON(t *testing.T) {
	deps := dependencies{newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) {
		return &fakeInventoryQuery{snapshot: testInventorySnapshot()}, nil
	}}
	var stdout strings.Builder
	if err := execute(context.Background(), []string{"inventory", "-o", "json"}, &stdout, io.Discard, deps); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	// The CLI must emit the shared Console view, not a shape of its own.
	var document operatorconsole.InventoryView
	if err := json.Unmarshal([]byte(stdout.String()), &document); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if document.DeviceClassesResourceVersion != "classes-rv" || document.ModelSpecsResourceVersion != "models-rv" ||
		len(document.Classes) != 1 ||
		document.Classes[0].UID != "class-uid" || document.Classes[0].NodeSelector["edge.dacs.io/device-class"] != "orin" ||
		len(document.Classes[0].PowerModes) != 1 || document.Classes[0].PowerModes[0].Actuator.JetsonNVPModel == nil ||
		document.Classes[0].PowerModes[0].Actuator.JetsonNVPModel.ID != 0 || len(document.Classes[0].Nodes) != 1 ||
		document.Classes[0].Nodes[0].Ready != "True" || !document.Classes[0].Nodes[0].SchedulingDisabled {
		t.Fatalf("document = %#v", document)
	}
	if len(document.Models) != 1 || document.Models[0].UID != "model-uid" ||
		len(document.Models[0].Spec.Artifacts) != 1 || document.Models[0].Spec.Artifacts[0].Format != "onnx" ||
		len(document.Models[0].Spec.ExecutionPaths) != 1 ||
		document.Models[0].Spec.ExecutionPaths[0].Runtime.Backend != "CPUExecutionProvider" {
		t.Fatalf("models = %#v", document.Models)
	}
}

func TestExecuteSystemWritesTable(t *testing.T) {
	query := &fakeSystemQuery{snapshot: testSystemSnapshot()}
	deps := dependencies{newSystemQuery: func(opts systemOptions) (systemQuery, error) {
		if opts.kubeconfig != "/tmp/config" || opts.context != "edge" || opts.output != outputFormatTable {
			t.Fatalf("options = %#v", opts)
		}
		return query, nil
	}}
	var stdout strings.Builder

	err := execute(context.Background(), []string{
		"system", "--kubeconfig", "/tmp/config", "--context", "edge",
	}, &stdout, io.Discard, deps)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	for _, want := range []string{
		"CHILLSYSTEMS_RV", "systems-rv", "chill-system", "NODE_DISCOVERY",
		"ProfilerAvailable", "True", "ProfilerReady", "profiler is available",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, stdout.String())
		}
	}
	if query.calls != 1 {
		t.Fatalf("query calls = %d", query.calls)
	}
}

func TestExecuteSystemWritesStructuredJSON(t *testing.T) {
	deps := dependencies{newSystemQuery: func(systemOptions) (systemQuery, error) {
		return &fakeSystemQuery{snapshot: testSystemSnapshot()}, nil
	}}
	var stdout strings.Builder
	if err := execute(context.Background(), []string{"system", "-o", "json"}, &stdout, io.Discard, deps); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	// The CLI must emit the shared Console view, not a shape of its own.
	var document operatorconsole.SystemsView
	if err := json.Unmarshal([]byte(stdout.String()), &document); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if document.ResourceVersion != "systems-rv" || len(document.Systems) != 1 ||
		document.Systems[0].UID != "system-uid" || !document.Systems[0].Spec.Profiler.Enabled ||
		document.Systems[0].Status.ObservedGeneration != 3 || len(document.Systems[0].Status.Conditions) != 1 {
		t.Fatalf("document = %#v", document)
	}
}

func TestExecuteProfilingRunsWritesTable(t *testing.T) {
	query := &fakeProfilingRunsQuery{snapshot: testProfilingRunsSnapshot()}
	deps := dependencies{newRunsQuery: func(opts profilingRunsOptions) (profilingRunsQuery, error) {
		if opts.namespace != "chill-system" || opts.allNamespaces || opts.output != outputFormatTable {
			t.Fatalf("options = %#v", opts)
		}
		return query, nil
	}}
	var stdout strings.Builder

	err := execute(context.Background(), []string{"runs", "-n", "chill-system"}, &stdout, io.Discard, deps)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	for _, want := range []string{
		"PROFILINGRUNS_RV", "runs-rv", "cpu-run", "yolov8n", "ort-cpu",
		"lattepanda", "ExecutionComplete", "False", "ExecutionPending",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, stdout.String())
		}
	}
	if query.calls != 1 {
		t.Fatalf("query calls = %d", query.calls)
	}
}

func TestExecuteProfilingRunsWritesStructuredJSON(t *testing.T) {
	deps := dependencies{newRunsQuery: func(opts profilingRunsOptions) (profilingRunsQuery, error) {
		if !opts.allNamespaces {
			t.Fatalf("options = %#v", opts)
		}
		snapshot := testProfilingRunsSnapshot()
		snapshot.Namespace = ""
		return &fakeProfilingRunsQuery{snapshot: snapshot}, nil
	}}
	var stdout strings.Builder
	if err := execute(context.Background(), []string{"runs", "-A", "-o", "json"}, &stdout, io.Discard, deps); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	// The CLI must emit the shared Console view, not a shape of its own.
	var document operatorconsole.ProfilingRunsView
	if err := json.Unmarshal([]byte(stdout.String()), &document); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if !document.AllNamespaces || document.ResourceVersion != "runs-rv" || len(document.Runs) != 1 ||
		document.Runs[0].UID != "run-uid" || document.Runs[0].Spec.Candidate.ModelSpec != "yolov8n" ||
		document.Runs[0].Status.ObservedGeneration != 1 || len(document.Runs[0].Status.Conditions) != 1 {
		t.Fatalf("document = %#v", document)
	}
}

func TestExecuteProfilingRunShowWritesReadableEvidencePassport(t *testing.T) {
	passport := testEvidencePassport()
	query := &fakeEvidencePassportQuery{passport: passport}
	deps := dependencies{newRunShowQuery: func(opts profilingRunShowOptions) (evidencePassportQuery, error) {
		if opts.namespace != "chill-system" || opts.name != "cpu-run" || opts.consoleService != "chill-console" {
			t.Fatalf("options = %#v", opts)
		}
		return query, nil
	}}
	var stdout strings.Builder
	args := []string{"runs", "show", "cpu-run", "-n", "chill-system"}
	if err := execute(context.Background(), args, &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"RUNTIME MEASUREMENT", "LATENCY", "POWER OBSERVATION", "THERMAL OBSERVATION",
		"WORKLOAD OBSERVATION", "SCIENTIFIC ADMISSION", "EVIDENCE LINEAGE",
		"188.009 items/s", "5.978 ms", "18.184 W", "Insufficient", "WorkloadWindowUnknown",
		"fastdds-test/fastdds@fastdds-uid",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, stdout.String())
		}
	}
	if query.calls != 1 {
		t.Fatalf("query calls = %d", query.calls)
	}
}

func TestExecuteProfilingRunShowWritesJSON(t *testing.T) {
	deps := dependencies{newRunShowQuery: func(profilingRunShowOptions) (evidencePassportQuery, error) {
		return &fakeEvidencePassportQuery{passport: testEvidencePassport()}, nil
	}}
	var stdout strings.Builder
	args := []string{"runs", "show", "cpu-run", "-o", "json"}
	if err := execute(context.Background(), args, &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	var got operatorconsole.EvidencePassport
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "EvidencePassport" || got.Run.Name != "cpu-run" || got.Measurement.Power.Watts == nil {
		t.Fatalf("passport = %#v", got)
	}
}

func TestExecuteRejectsInvalidUsageBeforeConstructingQuery(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"inventory", "extra"}, {"inventory", "-o", "yaml"},
		{"system", "extra"}, {"system", "-o", "yaml"},
		{"runs", "extra"}, {"runs", "-o", "yaml"}, {"runs", "-n", "chill-system", "-A"},
		{"runs", "show"}, {"runs", "show", "one", "two"}, {"runs", "show", "run", "-o", "yaml"},
	} {
		calls := 0
		deps := dependencies{
			newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) { calls++; return nil, nil },
			newSystemQuery:    func(systemOptions) (systemQuery, error) { calls++; return nil, nil },
			newRunsQuery:      func(profilingRunsOptions) (profilingRunsQuery, error) { calls++; return nil, nil },
			newPreflightQuery: func(preflightOptions) (batchEnergyOptimizationPreflightQuery, error) {
				calls++
				return nil, nil
			},
		}
		err := execute(context.Background(), args, io.Discard, io.Discard, deps)
		if !errors.Is(err, errUsage) || calls != 0 {
			t.Fatalf("execute(%v) error = %v, calls = %d", args, err, calls)
		}
	}
}

func TestExecuteHelpDoesNotConstructQuery(t *testing.T) {
	for _, args := range [][]string{
		{"--help"}, {"inventory", "--help"}, {"system", "--help"}, {"runs", "--help"}, {"preflight", "--help"},
	} {
		calls := 0
		deps := dependencies{
			newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) { calls++; return nil, nil },
			newSystemQuery:    func(systemOptions) (systemQuery, error) { calls++; return nil, nil },
			newRunsQuery:      func(profilingRunsOptions) (profilingRunsQuery, error) { calls++; return nil, nil },
			newPreflightQuery: func(preflightOptions) (batchEnergyOptimizationPreflightQuery, error) {
				calls++
				return nil, nil
			},
		}
		var output strings.Builder
		if err := execute(context.Background(), args, &output, &output, deps); err != nil {
			t.Fatalf("execute(%v) error = %v", args, err)
		}
		if output.Len() == 0 || calls != 0 {
			t.Fatalf("execute(%v) output = %q, calls = %d", args, output.String(), calls)
		}
	}
}

func TestExecutePreservesQueryFailure(t *testing.T) {
	want := errors.New("query failed")
	deps := dependencies{newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) {
		return &fakeInventoryQuery{err: want}, nil
	}}
	err := execute(context.Background(), []string{"inventory"}, io.Discard, io.Discard, deps)
	if !errors.Is(err, want) {
		t.Fatalf("execute() error = %v", err)
	}
}

func TestExecuteSystemPreservesQueryFailure(t *testing.T) {
	want := errors.New("query failed")
	deps := dependencies{newSystemQuery: func(systemOptions) (systemQuery, error) {
		return &fakeSystemQuery{err: want}, nil
	}}
	err := execute(context.Background(), []string{"system"}, io.Discard, io.Discard, deps)
	if !errors.Is(err, want) {
		t.Fatalf("execute() error = %v", err)
	}
}

func TestExecuteProfilingRunsPreservesQueryFailure(t *testing.T) {
	want := errors.New("query failed")
	deps := dependencies{newRunsQuery: func(profilingRunsOptions) (profilingRunsQuery, error) {
		return &fakeProfilingRunsQuery{err: want}, nil
	}}
	err := execute(context.Background(), []string{"runs", "-n", "chill-system"}, io.Discard, io.Discard, deps)
	if !errors.Is(err, want) {
		t.Fatalf("execute() error = %v", err)
	}
}

type fakeInventoryQuery struct {
	snapshot operatorconsole.InventorySnapshot
	err      error
	calls    int
}

type fakeSystemQuery struct {
	snapshot operatorconsole.SystemSnapshot
	err      error
	calls    int
}

type fakeProfilingRunsQuery struct {
	snapshot operatorconsole.ProfilingRunsSnapshot
	err      error
	calls    int
}

type fakeEvidencePassportQuery struct {
	passport operatorconsole.EvidencePassport
	err      error
	calls    int
}

func (q *fakeEvidencePassportQuery) Read(context.Context) (operatorconsole.EvidencePassport, error) {
	q.calls++
	return q.passport, q.err
}

func (q *fakeProfilingRunsQuery) Read(context.Context) (operatorconsole.ProfilingRunsSnapshot, error) {
	q.calls++
	return q.snapshot, q.err
}

func (q *fakeSystemQuery) Read(context.Context) (operatorconsole.SystemSnapshot, error) {
	q.calls++
	return q.snapshot, q.err
}

func (q *fakeInventoryQuery) Read(context.Context) (operatorconsole.InventorySnapshot, error) {
	q.calls++
	return q.snapshot, q.err
}

func testInventorySnapshot() operatorconsole.InventorySnapshot {
	return operatorconsole.InventorySnapshot{
		ReadAt:                       time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		DeviceClassesResourceVersion: "classes-rv",
		NodesResourceVersion:         "nodes-rv",
		ModelSpecsResourceVersion:    "models-rv",
		Classes: []deviceclass.InventoryClass{{
			Name: "orin", UID: "class-uid", ResourceVersion: "class-rv",
			Spec: edgev1alpha1.DeviceClassSpec{
				NodeSelector: map[string]string{"edge.dacs.io/device-class": "orin"},
				Architecture: "arm64", MemoryBytes: resource.MustParse("8Gi"),
				Accelerator: "jetson-orin", PowerModes: []edgev1alpha1.PowerMode{{
					Name: "15W", Actuator: edgev1alpha1.PowerModeActuator{
						Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
						JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0},
					},
				}},
			},
			Nodes: []deviceclass.InventoryNode{{
				Name: "orin-1", UID: "node-uid", ResourceVersion: "node-rv",
				Ready: corev1.ConditionTrue, SchedulingDisabled: true,
			}},
		}},
		Models: []edgev1alpha1.ModelSpec{{
			ObjectMeta: metav1.ObjectMeta{Name: "mobilenet", UID: "model-uid", ResourceVersion: "model-rv"},
			Spec: edgev1alpha1.ModelSpecSpec{
				Artifacts: []edgev1alpha1.ModelArtifact{{
					Name: "canonical-onnx", Format: "onnx",
				}},
				ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{
					Name: "ort-cpu", Artifact: "canonical-onnx",
					Runtime: edgev1alpha1.ModelRuntimeRequirement{Family: "onnxruntime", Backend: "CPUExecutionProvider"},
				}},
			},
		}},
	}
}

func testSystemSnapshot() operatorconsole.SystemSnapshot {
	return operatorconsole.SystemSnapshot{
		ReadAt:          time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		ResourceVersion: "systems-rv",
		Systems: []edgev1alpha1.ChillSystem{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "chill", UID: "system-uid", ResourceVersion: "system-rv", Generation: 3,
				CreationTimestamp: metav1.NewTime(time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)),
			},
			Spec: edgev1alpha1.ChillSystemSpec{
				ManagementNamespace: "chill-system",
				NodeDiscovery:       edgev1alpha1.ChillNodeDiscoverySpec{Enabled: true},
				DeviceDiscovery:     edgev1alpha1.ChillDeviceDiscoverySpec{Enabled: true},
				Profiler:            edgev1alpha1.ChillProfilerSpec{Enabled: true},
				EvidenceStore:       edgev1alpha1.ChillEvidenceStoreSpec{Enabled: true},
			},
			Status: edgev1alpha1.ChillSystemStatus{
				ObservedGeneration: 3,
				Conditions: []metav1.Condition{{
					Type: edgev1alpha1.ChillSystemConditionProfilerAvailable, Status: metav1.ConditionTrue,
					ObservedGeneration: 3, Reason: "ProfilerReady", Message: "profiler is available",
					LastTransitionTime: metav1.NewTime(time.Date(2026, 7, 20, 0, 1, 0, 0, time.UTC)),
				}},
			},
		}},
	}
}

func testProfilingRunsSnapshot() operatorconsole.ProfilingRunsSnapshot {
	return operatorconsole.ProfilingRunsSnapshot{
		ReadAt: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), ResourceVersion: "runs-rv",
		Namespace: "chill-system",
		Runs: []edgev1alpha1.ProfilingRun{{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "chill-system", Name: "cpu-run", UID: "run-uid", ResourceVersion: "run-rv", Generation: 1,
				CreationTimestamp: metav1.NewTime(time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)),
			},
			Spec: edgev1alpha1.ProfilingRunSpec{
				MeasurementStateSpec: edgev1alpha1.MeasurementStateSpec{
					Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
						ModelSpec: "yolov8n", DeviceClass: "lattepanda", ExecutionPath: "ort-cpu", PowerMode: "fixed",
					},
					TargetNode: "lattepanda",
					CPURequest: resource.MustParse("4"),
				},
				AdmissionPolicy: "single-run-v1",
				Measurement:     edgev1alpha1.ProfilingRunMeasurementSpec{Batch: 4},
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000, TimeoutMilliseconds: 200, DurationSeconds: 60,
				},
			},
			Status: edgev1alpha1.ProfilingRunStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{{
					Type: edgev1alpha1.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionFalse,
					ObservedGeneration: 1, Reason: "ExecutionPending", Message: "execution is not implemented",
					LastTransitionTime: metav1.NewTime(time.Date(2026, 7, 20, 0, 1, 0, 0, time.UTC)),
				}},
			},
		}},
	}
}

func testEvidencePassport() operatorconsole.EvidencePassport {
	return operatorconsole.EvidencePassport{
		APIVersion: operatorconsole.EvidencePassportAPIVersion,
		Kind:       "EvidencePassport",
		ReadAt:     time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC),
		Run: operatorconsole.EvidencePassportRun{
			Namespace: "chill-system", Name: "cpu-run", UID: "run-uid", Generation: 1,
			Spec: edgev1alpha1.ProfilingRunSpec{
				MeasurementStateSpec: edgev1alpha1.MeasurementStateSpec{
					Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
						ModelSpec: "yolov8n", DeviceClass: "lattepanda", ExecutionPath: "ort-cpu", PowerMode: "fixed",
					},
					TargetNode: "lattepanda",
				},
			},
			Conditions: []metav1.Condition{{
				Type:   edgev1alpha1.ProfilingRunConditionEvidenceAdmitted,
				Status: metav1.ConditionUnknown,
				Reason: "EvidenceInsufficient",
			}},
		},
		Evidence: operatorconsole.EvidencePassportLineage{
			FinalizedAt: time.Date(2026, 7, 20, 1, 1, 0, 0, time.UTC),
			Final: evidencestore.ObjectIdentity{
				Store: "chill-evidence", Key: "execution.json",
				Size: 9000,
			},
			Raw: evidencestore.ObjectIdentity{
				Store: "chill-evidence", Key: "raw.json",
				Size: 8000,
			},
		},
		Measurement: operatorconsole.EvidencePassportMeasurement{
			Runtime: operatorconsole.EvidencePassportRuntime{
				Family: "onnxruntime", Version: "1.16.0", Provider: "CPUExecutionProvider", Architecture: "x86_64",
				Batch: 1, WarmupIterations: 20, ElapsedSeconds: 30.001,
				CompletedCalls: 5630, CompletedItems: 5630, ThroughputItems: 188.009,
				Latency: operatorconsole.EvidencePassportLatency{Count: 5630, Mean: 5.319, P50: 5.179, P95: 5.978, P99: 7.889},
			},
			Power: operatorconsole.EvidencePassportPower{
				Source: operatorconsole.EvidencePassportPowerSource{
					Kind: "edge-metrics", NodeName: "lattepanda", Metric: "watts",
				},
				ObservationSeconds: 31, Samples: 31,
				Watts:                 &operatorconsole.EvidencePassportWatts{Minimum: 8.1, Mean: 18.184, Maximum: 19.7},
				SourceTimestampAbsent: true,
			},
			Thermal: operatorconsole.EvidencePassportThermal{
				Samples: 31, ObservationAvailableSamples: 31,
				CPUCoreThrottleEvents:     &operatorconsole.EvidencePassportCounter{Samples: 31, First: 7, Last: 7},
				MaximumTemperatureCelsius: &operatorconsole.EvidencePassportRange{Samples: 31, Minimum: 41, Maximum: 45},
			},
			Workload: &operatorconsole.EvidencePassportWorkload{
				NodeName: "lattepanda", Continuous: true, InitialActivePods: 3, FinalActivePods: 3,
				CoResidents: []operatorconsole.EvidencePassportWorkloadCoResident{
					{Namespace: "monitoring", Name: "edge-metrics", UID: "metrics-uid"},
					{Namespace: "fastdds-test", Name: "fastdds", UID: "fastdds-uid", Changed: true},
				},
			},
		},
		Admission: &edgev1alpha1.ProfilingRunAdmissionStatus{
			Verdict: "Insufficient", PolicyVersion: "wall-power-v2", ReasonCodes: []string{"WorkloadWindowUnknown"},
		},
	}
}
