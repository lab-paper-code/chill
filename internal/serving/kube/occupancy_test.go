package kube

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// occupancyRows are the final rows of a B=1 instance whose batches start
// every gap after a warmup of slower batches; skip drops the rows of batch IDs.
func occupancyRows(n int, gap time.Duration, skip ...uint64) []serving.RequestRow {
	at := time.Unix(1000, 0)
	var rows []serving.RequestRow
	start := at
	for id := uint64(1); id <= uint64(n); id++ {
		step := gap
		if id <= 10 {
			step = 2 * gap // warmup
		}
		start = start.Add(step)
		if slices.Contains(skip, id) {
			continue
		}
		received := start.Add(-gap)
		running, queued, executed := int64(gap), int64(time.Millisecond), int64(gap)+int64(27790*time.Microsecond)
		rows = append(rows, serving.RequestRow{Outcome: "completed", BatchID: id, ReceivedAt: received, RunningNanos: &running, QueuedNanos: &queued, ExecutedNanos: &executed})
	}
	return rows
}

func TestMeasureOccupancyReproducesRasp5(t *testing.T) {
	gap := time.Duration(math.Round(1e9 / 26.88))
	rows := occupancyRows(400, gap, 50, 51, 52)
	// Warmup covers the ten slower batches.
	measure := func(rows []serving.RequestRow, warmup, duration time.Duration) DispatchOccupancyRecord {
		w := DispatchOccupancyRecord{Batch: 1, ProfileMeanNanos: 27.79e6}
		measureOccupancy(&w, rows, warmup, duration)
		return w
	}
	w := measure(rows, 20*gap+gap/2, 300*gap)
	if math.Abs(w.Throughput-26.88) > 0.01 || math.Abs(float64(w.PerRequestNanos)/1e6-9.41) > 0.01 {
		t.Fatalf("X %.3f d %d ns", w.Throughput, w.PerRequestNanos)
	}
	if w.Batches < 290 || w.IdleFraction != 0 {
		t.Fatalf("window %+v", w)
	}
	// Requests that arrive after the batch ended leave the instance idle.
	for i := range rows {
		late := int64(32790 * time.Microsecond)
		rows[i].QueuedNanos = &late
	}
	// Each batch then waits 5 ms after it ends for its next request; the
	// skipped batches 50-52 leave 296 of the 300 pairs.
	if w = measure(rows, 20*gap, 300*gap); math.Abs(w.IdleFraction-296*5e6/(300*float64(gap))) > 1e-4 {
		t.Fatalf("idle fraction %v", w.IdleFraction)
	}
	if w = measure(rows[:3], 0, time.Minute); w.Batches != 3 {
		t.Fatalf("short window %+v", w)
	}
}

// A 140 s ChillServer stall inside the window (d9 rasp5) is one idle pair
// among thousands, but 140 s of the 360 s window: the time fraction catches it.
func TestMeasureOccupancyCountsIdleTime(t *testing.T) {
	gap := time.Duration(math.Round(1e9 / 29.2))
	rows := occupancyRows(12000, gap)
	for i := range rows {
		if rows[i].BatchID > 5000 {
			rows[i].ReceivedAt = rows[i].ReceivedAt.Add(140 * time.Second)
		}
	}
	w := DispatchOccupancyRecord{Batch: 1, ProfileMeanNanos: 27.79e6}
	measureOccupancy(&w, rows, time.Minute, 6*time.Minute)
	if want := (140*time.Second - 27790*time.Microsecond + time.Millisecond).Seconds() / 360; math.Abs(w.IdleFraction-want) > 1e-6 {
		t.Fatalf("idle fraction %v, want %v (batches %d)", w.IdleFraction, want, w.Batches)
	}
}

func occupancyRoot(name string, created time.Time, b int32) *edge.DispatchOccupancyMeasurement {
	return &edge.DispatchOccupancyMeasurement{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: name, CreationTimestamp: meta.NewTime(created)},
		Spec: edge.DispatchOccupancyMeasurementSpec{Node: "node", BatchEnergyOptimization: "beo", Workload: "plc", Batch: b, RateFactor: "1.2",
			WarmupSeconds: 60, DurationSeconds: 360, MinimumBatches: 120, MaximumIdleFraction: "0.01", Workers: 128}}
}

// occupancyHarness adds the Workload "plc" with its Dataset and Preprocessor,
// the load generator configuration and a gateway Node "gw".
func occupancyHarness(t *testing.T) *opcostHarness {
	t.Helper()
	o := newOpcostHarness(t)
	gw := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "gw", UID: "gw-uid"}}
	if _, err := o.kube.CoreV1().Nodes().Create(context.Background(), gw, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	o.create(&edge.Dataset{ObjectMeta: meta.ObjectMeta{Name: "vqa"}, Spec: edge.DatasetSpec{Source: edge.DatasetSource{Image: "data:1", Path: "/records"}, Fields: []edge.RawField{{Name: "image"}, {Name: "question"}}}},
		&edge.Preprocessor{ObjectMeta: meta.ObjectMeta{Name: "pre"}, Spec: edge.PreprocessorSpec{Input: edge.RawInputContract{Fields: []edge.RawField{{Name: "image"}}}}},
		&edge.Workload{ObjectMeta: meta.ObjectMeta{Name: "plc"}, Spec: edge.WorkloadSpec{ModelRef: edge.CatalogReference{Name: "m"}, PreprocessorRef: edge.CatalogReference{Name: "pre"}, DatasetRef: &edge.CatalogReference{Name: "vqa"}}})
	o.controller.mu.Lock()
	o.controller.config.LoadGenerator = LoadGeneratorConfig{Image: "load:1", ServiceAccount: "chill-load-generator", Namespace: "chill-system",
		Endpoint: "https://chill-serving-entrypoint.chill-system.svc:8080", CASecret: "serving-tls", NodeName: "gw"}
	o.controller.mu.Unlock()
	return o
}

func TestOccupancyRefusesUnmeasuredBatchAndWorkloads(t *testing.T) {
	for _, test := range []struct {
		name, want string
		batch      int32
		workload   string
		setup      func(*opcostHarness)
	}{
		{name: "batch", batch: 3, want: "BatchUnmeasured 3"},
		{name: "workload", batch: 1, workload: "absent", want: "WorkloadMissing absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := occupancyHarness(t)
			if test.setup != nil {
				o.controller.mu.Lock()
				test.setup(o)
				o.controller.mu.Unlock()
			}
			m := occupancyRoot("occ", time.Now(), test.batch)
			if test.workload != "" {
				m.Spec.Workload = test.workload
			}
			o.create(m)
			o.admit()
			if got := o.admission(m); got != "Refused: "+test.want {
				t.Fatalf("admission %q", got)
			}
			if m.Status.Phase != edge.MeasurementPhaseFailed || m.Status.Reason != test.want {
				t.Fatalf("status %+v", m.Status)
			}
		})
	}
}

// Roots on the load Node wait while an occupancy load runs, and an occupancy
// waits while the load Node is measured.
func TestOccupancyIsolatesTheLoadNode(t *testing.T) {
	o := occupancyHarness(t)
	gw, err := o.kube.CoreV1().Nodes().Get(context.Background(), "gw", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gw.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "busy", Effect: core.TaintEffectNoSchedule}}
	if _, err = o.kube.CoreV1().Nodes().Update(context.Background(), gw, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	// A run holds gw so its taint stays while the occupancy is judged.
	o.controller.startNodeRun(Operation{ID: "gw-run", Kind: handBackKind, Node: serving.NodeReference{Name: "gw", UID: "gw-uid"}}, SchedulerControllerConfig{},
		func(context.Context, *OperationResult) error { <-release; return nil })
	m := occupancyRoot("occ", time.Now(), 1)
	o.create(m)
	if err = o.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.holder() != "" || o.admission(m) != "" {
		t.Fatalf("occupancy granted while the load Node is measured: holder %q", o.holder())
	}
	o.controller.startNodeRun(Operation{ID: "load", Kind: occupancyKind, Node: testNode}, SchedulerControllerConfig{},
		func(context.Context, *OperationResult) error { <-release; return nil })
	gw.Spec.Taints = nil
	if _, err = o.kube.CoreV1().Nodes().Update(context.Background(), gw, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	onGateway := baselineRoot("gw-idle", time.Now())
	onGateway.Spec.State.TargetNode = "gw"
	o.create(onGateway)
	if err = o.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := o.admission(onGateway); got != "" {
		t.Fatalf("load Node root judged during a load: %q", got)
	}
}

// The hand-back removes the load Jobs of the Node.
func TestHandBackDeletesLoadJobs(t *testing.T) {
	o := occupancyHarness(t)
	job := &batch.Job{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "occupancy-x", Labels: map[string]string{loadNodeLabel: "node-uid"}}}
	other := &batch.Job{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "unrelated"}}
	for _, j := range []*batch.Job{job, other} {
		if _, err := o.kube.BatchV1().Jobs("chill-system").Create(context.Background(), j, meta.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	node := o.node.DeepCopy()
	node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "vanished", Effect: core.TaintEffectNoSchedule}}
	if _, err := o.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	o.admit()
	jobs, err := o.kube.BatchV1().Jobs("chill-system").List(context.Background(), meta.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Name != "unrelated" || o.holder() != "" {
		t.Fatalf("jobs %+v err %v holder %q", jobs.Items, err, o.holder())
	}
}

// The planner reads the latest accepted record per Node, mode, image and
// model; a negative d counts as 0 and keeps its source key.
func TestCostEvidenceReadsLatestAcceptedOccupancy(t *testing.T) {
	at := time.Unix(2000, 0).UTC()
	objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	put := func(name, mode, verdict string, d int64, measuredAt time.Time) string {
		record := DispatchOccupancyRecord{SchemaVersion: DispatchOccupancySchema, Node: testNode, PowerMode: mode, RuntimeImage: "image", Model: "m",
			PerRequestNanos: d, Verdict: verdict, MeasuredAt: measuredAt}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		key := DispatchOccupancyPrefix + "node-uid/" + name + ".json"
		objects.objects[key] = raw
		return key
	}
	put("old", "max", measurementAccepted, 9_000_000, at)
	newest := put("new", "max", measurementAccepted, 9_410_000, at.Add(time.Hour))
	put("later-insufficient", "max", measurementInsufficient, 1, at.Add(2*time.Hour))
	negative := put("low", "low", measurementAccepted, -5, at)
	evidence, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	view, err := evidence.Refresh(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]serving.DispatchOccupancy{}
	for _, o := range view.DispatchOccupancy {
		got[o.PowerMode] = o
	}
	if len(got) != 2 || got["max"].Source != newest || got["max"].PerRequest != 9_410_000 || got["max"].Node != testNode ||
		got["low"].PerRequest != 0 || got["low"].Source != negative {
		t.Fatalf("occupancy %+v", view.DispatchOccupancy)
	}
}

// Admit runs before Tick, so the snapshot can still hold the instance of the
// previous occupancy; the serving Pods listed in the same cycle decide.
func TestOccupancyJudgesWorkloadServingByListedPods(t *testing.T) {
	o := occupancyHarness(t)
	var w edge.Workload
	if err := o.controller.store.client.Get(context.Background(), client.ObjectKey{Name: "plc"}, &w); err != nil {
		t.Fatal(err)
	}
	o.controller.mu.Lock()
	o.controller.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: "previous/c", Node: testNode, WorkloadUID: string(w.UID)}}
	o.controller.mu.Unlock()
	// The Node reads back the profile mode, so the run needs no power change.
	profiles := o.controller.observer.Profiles()
	profiles[0].Condition.PowerMode = maxMode
	o.controller.observer.SetProfiles(profiles)
	o.setNode(func(n *core.Node) {
		n.Annotations[chillmeta.PowerActuator] = nvpModel0
	})
	m := occupancyRoot("occ", time.Now(), 1)
	o.create(m)
	if err := o.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := o.admission(m); !strings.HasPrefix(got, "Granted") {
		t.Fatalf("stale snapshot refused the occupancy: %q", got)
	}
	o.controller.wg.Wait()

	pod := servingPod("serving-plc")
	pod.Spec.NodeName = "gw"
	pod.Annotations = map[string]string{ServingRoutingAnnotation: `{"` + string(w.UID) + `":1}`}
	if _, err := o.kube.CoreV1().Pods("chill-system").Create(context.Background(), pod, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	next := occupancyRoot("occ-2", time.Now().Add(time.Second), 1)
	o.create(next)
	o.admit()
	if got := o.admission(next); got != "Refused: WorkloadServing chill-system/serving-plc" {
		t.Fatalf("serving Pod not refused: %q", got)
	}
}
