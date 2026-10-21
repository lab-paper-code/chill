package kube

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	w := measureOccupancy(rows, 1, 20*gap+gap/2, 300*gap, 27.79e6)
	if math.Abs(w.throughput-26.88) > 0.01 || math.Abs(w.perRequest/1e6-9.41) > 0.01 {
		t.Fatalf("X %.3f d %.3f ms", w.throughput, w.perRequest/1e6)
	}
	if w.batches < 290 || w.idleFraction != 0 {
		t.Fatalf("window %+v", w)
	}
	// Queued requests that arrive after the batch ended make the instance idle.
	for i := range rows {
		late := int64(gap) + int64(30*time.Millisecond)
		rows[i].QueuedNanos = &late
	}
	if w = measureOccupancy(rows, 1, 20*gap, 300*gap, 27.79e6); w.idleFraction != 1 {
		t.Fatalf("idle fraction %v", w.idleFraction)
	}
	if w = measureOccupancy(rows[:3], 1, 0, time.Minute, 27.79e6); w.batches != 3 {
		t.Fatalf("short window %+v", w)
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
		{name: "loadgen", batch: 1, want: "LoadGeneratorUnconfigured", setup: func(o *opcostHarness) { o.controller.config.LoadGenerator.Image = "" }},
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
			if got := o.annotation(m, profilingrun.AdmissionAnnotation); got != "Refused: "+test.want {
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
	if o.holder() != "" || o.annotation(m, profilingrun.AdmissionAnnotation) != "" {
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
	if got := o.annotation(onGateway, profilingrun.AdmissionAnnotation); got != "" {
		t.Fatalf("load Node root judged during a load: %q", got)
	}
}

func TestLoadJobSpec(t *testing.T) {
	o := occupancyHarness(t)
	m := occupancyRoot("occ", time.Now(), 1)
	m.UID = types.UID("occ-uid")
	var plan occupancyPlan
	plan.workload = &edge.Workload{ObjectMeta: meta.ObjectMeta{Name: "plc"}}
	plan.dataset = &edge.Dataset{Spec: edge.DatasetSpec{Source: edge.DatasetSource{Image: "data:1", Path: "/records"}}}
	plan.fields = []string{"image"}
	job := loadJob(m, testNode, plan, 32.256, o.controller.config.LoadGenerator)
	pod := job.Spec.Template.Spec
	args := strings.Join(pod.Containers[0].Args, " ")
	if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 540 || pod.NodeName != "gw" || job.Labels[loadNodeLabel] != "node-uid" ||
		pod.ServiceAccountName != "chill-load-generator" || pod.RestartPolicy != core.RestartPolicyNever {
		t.Fatalf("job %+v", job.Spec)
	}
	if !strings.Contains(args, "--endpoint https://chill-serving-entrypoint.chill-system.svc:8080") || !strings.Contains(args, "--fields image --rate 32.2560 --duration 420") ||
		!strings.Contains(args, "--workload plc") || !strings.Contains(args, "--workers 128") {
		t.Fatalf("args %s", args)
	}
	if init := pod.InitContainers[0]; init.Image != "data:1" || init.Command[len(init.Command)-1] != "/records/evaluation" {
		t.Fatalf("init %+v", init)
	}
	ca := pod.Volumes[1].Projected.Sources[0].Secret
	if ca.Name != "serving-tls" || len(ca.Items) != 1 || ca.Items[0].Key != "tls.crt" {
		t.Fatalf("ca %+v", ca)
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
