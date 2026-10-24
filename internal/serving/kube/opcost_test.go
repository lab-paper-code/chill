package kube

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// opcostHarness has a Complete BatchEnergyOptimization "beo" with a prepared
// catalog profile, a wall meter and the cpuClass power modes. measured lists
// the measured requests; verdicts sets a step's verdict by name.
type opcostHarness struct {
	*admissionHarness
	evidence *memEvidence
	mu       sync.Mutex
	measured []MeasurementRequest
	verdicts map[string]string
}

func newOpcostHarness(t *testing.T) *opcostHarness {
	t.Helper()
	h, _, evidence := sequenceHarness(t)
	o := &opcostHarness{admissionHarness: h, evidence: evidence, verdicts: map[string]string{}}
	class := cpuClass()
	beo := beoRoot("beo", "node", catalogCompletePhase)
	h.create(class, beo)
	spec := measuredCPUPreparationFixture()
	profile := *spec.Instance.Profile
	profile.ID = "chill-system/beo/" + string(beo.UID)
	profile.Batches = []serving.BatchProfile{{Batch: 1, DurationNanos: []float64{27.79e6}}}
	spec.Instance.Profile = &profile
	h.controller.observer.SetProfiles([]serving.Profile{profile})
	h.controller.mu.Lock()
	h.controller.config.Preparations = map[string]PrepareSpec{profile.ID: spec}
	h.controller.config.Meters = []MeterDeclaration{{ID: "wall", Node: testNode, Scope: measurementWallScope}}
	for _, m := range class.Spec.PowerModes {
		h.controller.config.PowerPolicies = append(h.controller.config.PowerPolicies, nodeactuator.ActuationPolicySpec{Name: m.Name, DeviceClass: class.Name,
			PowerMode: m.Name, ActuatorKind: string(m.Actuator.Kind), SettlingDurationSeconds: 1, MaxActuationDurationSeconds: 60})
	}
	h.controller.mu.Unlock()
	h.controller.measureStep = func(ctx context.Context, _ SchedulerControllerConfig, request MeasurementRequest) (OperationMeasurement, evidencestore.Receipt, error) {
		o.mu.Lock()
		o.measured = append(o.measured, request)
		verdict := o.verdicts[request.Operation.ID[strings.LastIndex(request.Operation.ID, ".")+1:]]
		o.mu.Unlock()
		if verdict == "" {
			verdict = measurementAccepted
		}
		key := OperationMeasurementKey(request.Operation)
		record := OperationMeasurement{Verdict: verdict, Reason: "coverage"}
		if request.Operation.Prepare != nil {
			record.Result.Actual.Instances = []serving.InstanceState{{ID: "pod-uid/containerd://prepared", TargetID: request.Operation.Prepare.Instance.ID}}
		}
		return record, evidencestore.Receipt{Object: evidencestore.ObjectIdentity{Key: key}}, nil
	}
	return o
}

func beoRoot(name, node, phase string) *edge.BatchEnergyOptimization {
	o := &edge.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: name, UID: types.UID(name + "-uid"), CreationTimestamp: meta.NewTime(time.Now().Add(-time.Hour))}}
	o.Spec.State.TargetNode, o.Status.Phase = node, phase
	return o
}

func opcostRoot(name string, created time.Time, steps ...edge.OperationCostStep) *edge.OperationCostMeasurement {
	return &edge.OperationCostMeasurement{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: name, UID: types.UID(name + "-uid"), CreationTimestamp: meta.NewTime(created)},
		Spec: edge.OperationCostMeasurementSpec{Node: "node", Steps: steps, Observation: edge.MeasurementObservationSpec{IntervalMilliseconds: 1000, RequestTimeoutMilliseconds: 800,
			MaximumOperationSeconds: 900, MaximumGapMilliseconds: 3000,
			Timing: &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "1.0339932335", MaximumRequestSeconds: "0.248723980", MaximumReceiptGapSeconds: "0.325263383", Source: "completed 120s empirical observation"}}}}
}

func lane() []edge.OperationCostStep {
	const beo = "beo"
	idle := int32(120)
	return []edge.OperationCostStep{{Name: "up", Kind: edge.OperationCostStepPower, PowerMode: "max"}, {Name: "prepare", Kind: edge.OperationCostStepPrepare, BatchEnergyOptimization: beo},
		{Name: "idle", Kind: edge.OperationCostStepIdle, IdleDurationSeconds: &idle}, {Name: "remove", Kind: edge.OperationCostStepRemove, BatchEnergyOptimization: beo},
		{Name: "down", Kind: edge.OperationCostStepPower, PowerMode: "low"}}
}

func (o *opcostHarness) status(m *edge.OperationCostMeasurement) edge.OperationCostMeasurementStatus {
	o.t.Helper()
	if err := o.controller.store.client.Get(context.Background(), client.ObjectKeyFromObject(m), m); err != nil {
		o.t.Fatal(err)
	}
	return m.Status
}

func TestOperationCostRunsDeclaredStepsInOrder(t *testing.T) {
	o := newOpcostHarness(t)
	m := opcostRoot("cost", time.Now(), lane()...)
	o.create(m)
	o.admit()
	status := o.status(m)
	if status.Phase != edge.MeasurementPhaseComplete || len(status.Steps) != 5 || o.holder() != "" {
		t.Fatalf("status %+v holder %q results %+v", status, o.holder(), o.controller.State().Results)
	}
	for i, step := range status.Steps {
		want := fmt.Sprintf("%snode-uid/%s.%d.%s.json", OperationMeasurementPrefix, m.UID, i, lane()[i].Name)
		if step.Key != want || step.Verdict != measurementAccepted {
			t.Fatalf("step %d %+v, want key %s", i, step, want)
		}
	}
	up, prepare, idle, remove := o.measured[0].Operation, o.measured[1].Operation, o.measured[2], o.measured[3].Operation
	if up.PowerMode != maxMode || up.MaxActuationDuration != time.Minute {
		t.Fatalf("power step %+v", up)
	}
	if prepare.Prepare == nil || !strings.HasPrefix(prepare.Prepare.Instance.Profile.ID, "chill-system/beo/") || remove.Instance != "pod-uid/containerd://prepared" || idle.IdleDuration != 120*time.Second {
		t.Fatalf("prepare %+v remove %q idle %s", prepare.Prepare, remove.Instance, idle.IdleDuration)
	}
	if idle.OperationBudget != 900*time.Second || idle.Sampling.Duration != 906015961194*time.Nanosecond || idle.Sampling.MaxEvents != 909 || idle.Timing == nil || idle.Timing.ChangeIntervalP95Seconds != "1.0339932335" || idle.Coverage.MaximumGap != 3*time.Second {
		t.Fatalf("sampling %+v coverage %+v timing %+v budget %s", idle.Sampling, idle.Coverage, idle.Timing, idle.OperationBudget)
	}
}

func TestOperationCostStopsAtInsufficientStep(t *testing.T) {
	o := newOpcostHarness(t)
	o.verdicts["prepare"] = measurementInsufficient
	m := opcostRoot("cost", time.Now(), lane()...)
	o.create(m)
	o.admit()
	status := o.status(m)
	if status.Phase != edge.MeasurementPhaseInsufficient || status.Reason != "coverage" || len(status.Steps) != 2 || len(o.measured) != 2 {
		t.Fatalf("status %+v measured %d", status, len(o.measured))
	}
	sequence := o.evidence.sequence(t, string(m.UID))
	if sequence.Requested != 5 || len(sequence.Saved) != 2 || sequence.Reason != "coverage" {
		t.Fatalf("sequence %+v", sequence)
	}
}

func TestOperationCostRestartFailsTheCutMeasurement(t *testing.T) {
	o := newOpcostHarness(t)
	m := opcostRoot("cost", time.Now(), lane()...)
	m.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Now())}
	done := opcostRoot("done", time.Now(), lane()...)
	o.create(m, done)
	if err := o.controller.patchMeasurementStatus(context.Background(), done, func() { done.Status.Phase = edge.MeasurementPhaseComplete }); err != nil {
		t.Fatal(err)
	}
	saved := OperationMeasurementPrefix + "node-uid/" + string(m.UID) + ".0.up.json"
	o.evidence.objects[saved] = []byte("{}")
	for _, holder := range []string{string(m.UID), string(done.UID)} {
		node, err := o.kube.CoreV1().Nodes().Get(context.Background(), "node", meta.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: holder, Effect: core.TaintEffectNoSchedule}}
		if _, err = o.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		o.admit()
		if o.holder() != "" {
			t.Fatalf("%s not handed back", holder)
		}
	}
	if status := o.status(m); status.Phase != edge.MeasurementPhaseFailed || status.Reason != sequenceRestartReason {
		t.Fatalf("cut measurement %+v", status)
	}
	if sequence := o.evidence.sequence(t, string(m.UID)); sequence.Reason != sequenceRestartReason || len(sequence.Saved) != 1 || sequence.Saved[0] != saved {
		t.Fatalf("sequence %+v", sequence)
	}
	if status := o.status(done); status.Phase != edge.MeasurementPhaseComplete || len(o.measured) != 0 {
		t.Fatalf("complete measurement %+v measured %d", status, len(o.measured))
	}
}

func TestOperationCostRefusalsLetTheNextRootGo(t *testing.T) {
	prepare := func(beo string) edge.OperationCostStep {
		return edge.OperationCostStep{Name: "prepare", Kind: edge.OperationCostStepPrepare, BatchEnergyOptimization: beo}
	}
	for _, test := range []struct {
		name, want string
		step       edge.OperationCostStep
		issue      bool
	}{
		{name: "missing", step: prepare("absent"), want: "ProfileMissing absent NotFound"},
		{name: "running", step: prepare("running"), want: "ProfileMissing running NotComplete"},
		{name: "failed", step: prepare("failed"), want: "ProfileMissing failed Failed"},
		{name: "issue", step: prepare("beo"), issue: true, want: "ProfileMissing beo MeasurementEvidenceMissing"},
		{name: "other node", step: prepare("elsewhere"), want: "NodeMismatch elsewhere far"},
		{name: "power mode", step: edge.OperationCostStep{Name: "up", Kind: edge.OperationCostStepPower, PowerMode: "turbo"}, want: "PowerModeUnknown turbo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOpcostHarness(t)
			o.create(beoRoot("running", "far", ""), beoRoot("failed", "node", campaignFailedPhase), beoRoot("elsewhere", "far", catalogCompletePhase))
			o.controller.mu.Lock()
			if test.issue {
				o.controller.config.CatalogIssues = []CatalogIssue{{Source: types.NamespacedName{Namespace: "chill-system", Name: "beo"}, Code: "MeasurementEvidenceMissing"}}
			}
			o.controller.mu.Unlock()
			refused := opcostRoot("refused", time.Now().Add(-time.Minute), test.step)
			next := baselineRoot("next", time.Now())
			o.create(refused, next)
			o.admit()
			if got := o.admission(refused); got != "Refused: "+test.want {
				t.Fatalf("admission %q", got)
			}
			if status := o.status(refused); status.Phase != edge.MeasurementPhaseFailed || status.Reason != test.want {
				t.Fatalf("status %+v", status)
			}
			if o.holder() != string(next.UID) {
				t.Fatalf("next root not granted: holder %q", o.holder())
			}
		})
	}
}

// A profile the catalog has not read yet holds the Node's queue.
func TestOperationCostWaitsForTheCatalog(t *testing.T) {
	o := newOpcostHarness(t)
	o.controller.observer.SetProfiles(nil)
	m := opcostRoot("cost", time.Now().Add(-time.Minute), lane()...)
	next := baselineRoot("next", time.Now())
	o.create(m, next)
	o.admit()
	if o.holder() != "" || o.admission(m) != "" || o.admission(next) != "" {
		t.Fatalf("catalog lag did not hold the queue: holder %q", o.holder())
	}
}

func TestOperationCostContinuesAcceptedHighUncertaintySteps(t *testing.T) {
	o := newOpcostHarness(t)
	measure := o.controller.measureStep
	o.controller.measureStep = func(ctx context.Context, config SchedulerControllerConfig, request MeasurementRequest) (OperationMeasurement, evidencestore.Receipt, error) {
		record, receipt, err := measure(ctx, config, request)
		nominal, uncertainty := 1.0, 100.0
		record.WallJoules, record.EnergyUncertaintyJoules = &nominal, &uncertainty
		return record, receipt, err
	}
	m := opcostRoot("uncertain", time.Now(), lane()...)
	o.create(m)
	o.admit()
	if status := o.status(m); status.Phase != edge.MeasurementPhaseComplete || len(status.Steps) != len(lane()) || len(o.measured) != len(lane()) {
		t.Fatalf("valid high-relative-error step stopped the sequence: %+v", status)
	}
	if o.measured[3].Operation.Instance != "pod-uid/containerd://prepared" {
		t.Fatal("uncertain accepted prepare lost its actual physical remove target")
	}
}

func TestOperationCostMissingTimingFailsBeforePhysicalSteps(t *testing.T) {
	o := newOpcostHarness(t)
	m := opcostRoot("missing-timing", time.Now(), lane()...)
	m.Spec.Observation.Timing = nil
	o.create(m)
	o.admit()
	if status := o.status(m); status.Phase != edge.MeasurementPhaseFailed || len(o.measured) != 0 || o.holder() != "" {
		t.Fatalf("missing timing left an active or fabricated measurement: %+v", status)
	}
}

func TestOperationCostStopsOnActualOperationFailure(t *testing.T) {
	o := newOpcostHarness(t)
	measure := o.controller.measureStep
	o.controller.measureStep = func(ctx context.Context, config SchedulerControllerConfig, request MeasurementRequest) (OperationMeasurement, evidencestore.Receipt, error) {
		record, receipt, err := measure(ctx, config, request)
		if request.Operation.Kind == operationPrepare {
			record.Verdict = operationFailed
			err = fmt.Errorf("physical preparation failed")
		}
		return record, receipt, err
	}
	m := opcostRoot("physical-failure", time.Now(), lane()...)
	o.create(m)
	o.admit()
	if status := o.status(m); status.Phase != edge.MeasurementPhaseFailed || len(status.Steps) != 2 || len(o.measured) != 2 {
		t.Fatalf("actual failure did not terminate sequence: %+v", status)
	}
}
