package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// memEvidence is an in-memory evidence store.
type memEvidence struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (m *memEvidence) PutImmutable(_ context.Context, identity evidencestore.ObjectIdentity, r io.Reader) (evidencestore.Receipt, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[identity.Key] = raw
	return evidencestore.Receipt{Object: identity}, nil
}

func (m *memEvidence) List(_ context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var identities []evidencestore.ObjectIdentity
	for key, raw := range m.objects {
		if strings.HasPrefix(key, prefix) {
			identities = append(identities, evidencestore.ObjectIdentity{Store: "evidence", Key: key, Size: int64(len(raw))})
		}
	}
	return identities, nil
}

func (m *memEvidence) sequence(t *testing.T, id string) OperationSequence {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var sequence OperationSequence
	if err := json.Unmarshal(m.objects[OperationSequencePrefix+id+".json"], &sequence); err != nil {
		t.Fatalf("sequence record %s: %v", id, err)
	}
	return sequence
}

// writeLog records, in API order, every taint write and serving Pod create
// and delete on the Node.
type writeLog struct {
	mu     sync.Mutex
	events []string
}

func (l *writeLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

// overlaps replays the log: a serving Pod created while the Node is tainted,
// or a taint added while a serving Pod exists, is an overlap.
func (l *writeLog) overlaps() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var bad []string
	tainted, pods := false, 0
	for i, e := range l.events {
		switch e {
		case "taint+":
			if pods > 0 {
				bad = append(bad, fmt.Sprintf("%d taint added with %d serving Pods", i, pods))
			}
			tainted = true
		case "taint-":
			tainted = false
		case "pod+":
			if tainted {
				bad = append(bad, fmt.Sprintf("%d serving Pod created on a tainted Node", i))
			}
			pods++
		case "pod-":
			pods--
		}
	}
	return bad
}

// podsInKube creates Pods in the Kubernetes fake, which the Node checks read.
type podsInKube struct {
	client.Client
	create func(*core.Pod) error
}

func (c podsInKube) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if pod, ok := obj.(*core.Pod); ok {
		return c.create(pod)
	}
	return c.Client.Create(ctx, obj, opts...)
}

// sequenceHarness keeps serving Pods in the Kubernetes fake only, logs the
// writes and stops a prepare right after its Pod is created.
func sequenceHarness(t *testing.T) (*admissionHarness, *writeLog, *memEvidence) {
	t.Helper()
	h := newAdmissionHarness(t)
	log := &writeLog{}
	evidence := &memEvidence{objects: map[string][]byte{}}
	h.kube.PrependReactor("patch", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
		if strings.Contains(string(action.(ktesting.PatchAction).GetPatch()), profilingrun.MeasurementTaintKey) {
			log.add("taint+")
		} else {
			log.add("taint-")
		}
		return false, nil, nil
	})
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	h.kube.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		log.mu.Lock()
		defer log.mu.Unlock()
		err := h.kube.Tracker().Delete(pods, action.GetNamespace(), action.(ktesting.DeleteAction).GetName())
		if err == nil {
			log.events = append(log.events, "pod-")
		}
		return true, nil, err
	})
	h.kube.PrependReactor("create", "services", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("stop after Pod creation")
	})
	h.controller.store.client = podsInKube{Client: h.controller.store.client, create: func(pod *core.Pod) error {
		log.mu.Lock()
		defer log.mu.Unlock()
		err := h.kube.Tracker().Create(pods, pod, pod.Namespace)
		if err == nil {
			log.events = append(log.events, "pod+")
		}
		return err
	}}
	h.controller.mu.Lock()
	h.controller.config.Evidence, h.controller.config.EvidenceStore = evidence, "evidence"
	h.controller.config.MeterSource = func(metricobserver.SourceIdentity) (metricobserver.Source, error) {
		return nil, errors.New("no meter in this test")
	}
	h.controller.state.Snapshot = serving.SchedulerSnapshot{
		Workloads: []*serving.Definition{{Name: "w", UID: "w-uid"}},
		Nodes:     []serving.SchedulerNode{{Node: serving.NodeReference{Name: "node", UID: "node-uid"}, CPUs: []int{0, 1, 2, 3}, Ready: true}},
	}
	spec := measuredCPUPreparationFixture()
	h.controller.config.Preparations = map[string]PrepareSpec{spec.Instance.Profile.ID: spec}
	h.controller.mu.Unlock()
	return h, log, evidence
}

var testNode = serving.NodeReference{Name: "node", UID: "node-uid"}

func manualPrepare() ManualOperation {
	return ManualOperation{Kind: operationPrepare, WorkloadUID: "w-uid", ProfileID: measuredCPUPreparationFixture().Instance.Profile.ID, Node: testNode}
}

func errorCode(err error) string {
	var domain *serving.Error
	if errors.As(err, &domain) {
		return domain.Code
	}
	return ""
}

// Power change before measurement admission: a pending power change keeps the
// Node from a measurement; once granted, serving power changes are refused.
func TestOrderPowerChangeThenAdmission(t *testing.T) {
	h, _, _ := sequenceHarness(t)
	pending := &edge.NodeActuation{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "power"},
		Spec: edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}, Deadline: meta.NewTime(time.Now().Add(time.Hour))}}
	h.create(pending)
	if _, err := h.controller.ExecuteManual(context.Background(), manualPrepare()); errorCode(err) != "NodeBusy" {
		t.Fatalf("manual prepare admitted over a pending power change: %v", err)
	}
	pending.Status.Phase = edge.NodeActuationPhaseFailed
	if err := h.controller.store.client.Update(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	h.create(baselineRoot("idle", time.Now()))
	h.admit()
	if h.holder() == "" {
		t.Fatal("not granted")
	}
	request := &edge.NodeActuation{Spec: edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}}}
	if err := h.controller.operations.ChangePower(context.Background(), testNode, request); !errors.Is(err, ErrNodeInMeasurement) {
		t.Fatalf("serving power change on a measured Node: %v", err)
	}
	if _, err := h.controller.ExecuteManual(context.Background(), manualPrepare()); errorCode(err) != "NodeBusy" {
		t.Fatalf("manual prepare on a measured Node: %v", err)
	}
}

// Tick during a measurement: the measured Node is a measurement Node in the
// snapshot and launch starts nothing on a Node a run holds.
func TestOrderTickDuringMeasurement(t *testing.T) {
	h, _, _ := sequenceHarness(t)
	h.create(baselineRoot("idle", time.Now()))
	h.admit()
	snapshot, err := h.controller.capture(context.Background(), time.Now(), h.controller.config)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != 1 || !snapshot.Nodes[0].Measurement {
		t.Fatalf("measured Node not marked: %+v", snapshot.Nodes)
	}
	release := make(chan struct{})
	h.controller.startNodeRun(Operation{ID: "held", Kind: operationCostKind, Node: testNode}, SchedulerControllerConfig{},
		func(context.Context, *OperationResult) error { <-release; return nil })
	defer close(release)
	h.controller.launch(serving.SchedulerCandidate{ID: "target", Actions: []serving.Action{{ID: "remove", Kind: operationRemove, Node: testNode, Instance: "p/c"}}}, h.controller.config)
	h.controller.mu.Lock()
	_, launched := h.controller.runs["remove"]
	h.controller.mu.Unlock()
	if launched {
		t.Fatal("launch acted on a Node a measurement run holds")
	}
}

// Measurement end before a power change: hand-back waits for the power change
// in flight, serving power changes stay refused until the taint is removed.
func TestOrderMeasurementEndThenPowerChange(t *testing.T) {
	h, _, _ := sequenceHarness(t)
	root := baselineRoot("idle", time.Now())
	h.create(root)
	h.admit()
	pending := &edge.NodeActuation{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "power"},
		Spec: edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}, Deadline: meta.NewTime(time.Now().Add(time.Hour))}}
	h.create(pending)
	h.complete(root)
	if err := h.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if h.holder() == "" {
		t.Fatal("hand-back did not wait for the power change in flight")
	}
	if err := h.controller.operations.admittedNode(context.Background(), testNode); !errors.Is(err, ErrNodeInMeasurement) {
		t.Fatalf("serving power change admitted before the taint was removed: %v", err)
	}
	pending.Status.Phase = edge.NodeActuationPhaseReady
	if err := h.controller.store.client.Update(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	h.admit()
	if h.holder() != "" {
		t.Fatal("taint kept after the power change settled")
	}
	if err := h.controller.operations.admittedNode(context.Background(), testNode); err != nil {
		t.Fatalf("serving power change refused after hand-back: %v", err)
	}
}

func TestManualOperationGuards(t *testing.T) {
	h, _, _ := sequenceHarness(t)
	h.controller.mu.Lock()
	h.controller.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: "p/c", Node: testNode, WorkloadUID: "foreign-workload"}, {ID: "q/c", Node: testNode}}
	h.controller.state.Snapshot.Routing = map[string]map[string]int32{"w-uid": {"q/c": 1}}
	h.controller.config.Settings.AutonomousWorkloads = []string{"auto"}
	h.controller.mu.Unlock()
	for name, request := range map[string]ManualOperation{
		"shared":     {Kind: operationRemove, WorkloadUID: "w-uid", Node: testNode, Instance: "p/c"},
		"routed":     {Kind: operationRemove, WorkloadUID: "w-uid", Node: testNode, Instance: "q/c"},
		"autonomous": {Kind: operationRemove, WorkloadUID: "auto", Node: testNode, Instance: "q/c"},
		"unknown":    {Kind: operationPrepare, WorkloadUID: "w-uid", ProfileID: "missing", Node: testNode},
	} {
		if _, err := h.controller.ExecuteManual(context.Background(), request); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	result, err := h.controller.ExecuteManual(context.Background(), manualPrepare())
	if err != nil || !strings.Contains(result.Error, "stop after Pod creation") {
		t.Fatalf("prepare did not reach Pod creation: %+v %v", result, err)
	}
	pod, err := h.kube.CoreV1().Pods("chill-system").Get(context.Background(), measuredCPUPreparationFixture().Name, meta.GetOptions{})
	if err != nil || len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].UID != "w-uid" {
		t.Fatalf("prepared Pod %+v %v", pod, err)
	}
	if _, err = h.controller.ExecuteManual(context.Background(), manualPrepare()); errorCode(err) != "OperationConflict" {
		t.Fatalf("second prepare on an occupied Node: %v", err)
	}
}

// Tens of concurrent admissions, hand-backs and manual
// prepares on one Node never let a serving Pod and the measurement taint
// coexist: every path goes through the single writer.
func TestConcurrentRequestsNeverOverlapMeasurementAndServing(t *testing.T) {
	h, log, _ := sequenceHarness(t)
	for i := range 5 {
		h.create(baselineRoot(fmt.Sprintf("idle-%d", i), time.Now().Add(time.Duration(i)*time.Second)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	worker := func(n int, body func()) {
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ctx.Err() == nil {
					body()
				}
			}()
		}
	}
	worker(10, func() { _, _ = h.controller.ExecuteManual(ctx, manualPrepare()) })
	worker(2, func() { _ = h.controller.Admit(ctx) })
	// Finish granted measurements, queue new ones and remove prepared
	// instances, as their owners would.
	queued := 5
	worker(1, func() {
		queued++
		_ = h.controller.store.client.Create(ctx, baselineRoot(fmt.Sprintf("idle-%d", queued), time.Now()))
		var baselines edge.ModelLoadedIdleBaselineList
		if h.controller.store.client.List(ctx, &baselines) == nil {
			for i := range baselines.Items {
				b := &baselines.Items[i]
				if granted, _, _ := profilingrun.Admission(b.Annotations); granted && b.Status.Phase == "" {
					b.Status.Phase = edge.ModelLoadedIdleBaselinePhaseComplete
					_ = h.controller.store.client.Update(ctx, b)
				}
			}
		}
		if pods, err := h.kube.CoreV1().Pods("chill-system").List(ctx, meta.ListOptions{}); err == nil {
			for _, p := range pods.Items {
				_ = h.kube.CoreV1().Pods("chill-system").Delete(ctx, p.Name, meta.DeleteOptions{})
			}
		}
		time.Sleep(time.Millisecond)
	})
	wg.Wait()
	h.controller.wg.Wait()
	counts := map[string]int{}
	for _, e := range log.events {
		counts[e]++
	}
	if counts["taint+"] == 0 || counts["pod+"] == 0 {
		t.Fatalf("both paths must run: %v", counts)
	}
	if bad := log.overlaps(); len(bad) > 0 {
		t.Fatalf("measurement and serving overlapped: %v (events %v) %v", bad, counts, log.events[:20])
	}
}
