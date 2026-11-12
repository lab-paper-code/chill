package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type admissionMetadataClient struct {
	client.Client
	metadataLists, profilingLists, profilingItems, profilingGets int
	// The benchmark models the API server's metadata response, not fake.Client's
	// full-object JSON marshal followed by metadata projection on the client.
	profilingMetadata []byte
}

func (c *admissionMetadataClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if metadata, ok := list.(*meta.PartialObjectMetadataList); ok {
		c.metadataLists++
		if c.profilingMetadata != nil && metadata.Kind == "ProfilingRunList" {
			return json.Unmarshal(c.profilingMetadata, metadata)
		}
	}
	err := c.Client.List(ctx, list, opts...)
	if runs, ok := list.(*edge.ProfilingRunList); ok {
		c.profilingLists++
		c.profilingItems += len(runs.Items)
	}
	return err
}

func (c *admissionMetadataClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if _, ok := object.(*edge.ProfilingRun); ok {
		c.profilingGets++
	}
	return c.Client.Get(ctx, key, object, opts...)
}

func terminalAdmissionRuns(n int) []client.Object {
	objects := make([]client.Object, n)
	for i := range objects {
		objects[i] = &edge.ProfilingRun{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: fmt.Sprintf("terminal-%05d", i), UID: types.UID(fmt.Sprint(i)), ResourceVersion: "1"}, Status: edge.ProfilingRunStatus{Conditions: []meta.Condition{{Type: edge.ProfilingRunConditionExecutionComplete, Status: meta.ConditionTrue, Message: strings.Repeat("completed ", 450)}}}}
	}
	return objects
}

func terminalAdmissionController(tb testing.TB, n int) (*SchedulerController, *admissionMetadataClient) {
	tb.Helper()
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		tb.Fatal(err)
	}
	api := &admissionMetadataClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(terminalAdmissionRuns(n)...).Build()}
	store := &Store{client: api}
	kube := kubefake.NewSimpleClientset()
	cluster := NewClusterState(store, kube, "chill-system")
	ctx, cancel := context.WithCancel(context.Background())
	c := &SchedulerController{reservationState: &cluster.reservation, store: store, kube: kube, runtime: &serving.Runtime{}, observer: &Observer{observerState: &cluster.observation, config: ObserverConfig{Namespace: "chill-system"}}, config: SchedulerControllerConfig{MeasurementNamespaces: []string{"chill-system"}}, ctx: ctx, cancel: cancel}
	tb.Cleanup(c.Close)
	return c, api
}

func TestAdmitDoesNotRedecodeTerminalProfilingRuns(t *testing.T) {
	c, api := terminalAdmissionController(t, 6500)
	for range 3 {
		if err := c.Admit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if api.profilingLists != 1 || api.profilingItems != 6500 || api.profilingGets != 0 {
		t.Fatalf("terminal payload reads: lists=%d items=%d gets=%d, want 1/6500/0", api.profilingLists, api.profilingItems, api.profilingGets)
	}
	if api.metadataLists != 18 {
		t.Fatalf("metadata lists=%d, want six per round", api.metadataLists)
	}
}

func BenchmarkAdmitTerminalProfilingRuns(b *testing.B) {
	c, api := terminalAdmissionController(b, 6500)
	list := &meta.PartialObjectMetadataList{}
	list.SetGroupVersionKind(edge.GroupVersion.WithKind("ProfilingRunList"))
	if err := api.Client.List(context.Background(), list, client.InNamespace("chill-system")); err != nil {
		b.Fatal(err)
	}
	wire, err := json.Marshal(list)
	if err != nil {
		b.Fatal(err)
	}
	api.profilingMetadata = wire
	if err := c.Admit(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := c.Admit(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(wire)), "metadata-B/op")
}

func TestAdmitObservesNewWaitingUpdatedDeletedAndRecreatedRoots(t *testing.T) {
	c, api := terminalAdmissionController(t, 20)
	ctx := context.Background()
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	waiting := baselineRoot("new-waiting", time.Now())
	if err := api.Create(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(waiting), waiting); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(waiting.Annotations[profilingrun.AdmissionAnnotation], "Refused: NodeMissing") {
		t.Fatalf("new waiting root not observed: %v", waiting.Annotations)
	}
	if err := api.Delete(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	replacement := baselineRoot(waiting.Name, time.Now())
	replacement.UID = "recreated-root-uid"
	if err := api.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(replacement), replacement); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(replacement.Annotations[profilingrun.AdmissionAnnotation], "Refused: NodeMissing") {
		t.Fatalf("replacement root not observed: %v", replacement.Annotations)
	}
	run := &edge.ProfilingRun{}
	if err := api.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: "terminal-00001"}, run); err != nil {
		t.Fatal(err)
	}
	run.Status.Conditions = nil
	run.Spec.TargetNode = "gone"
	if err := api.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.Annotations[profilingrun.AdmissionAnnotation], "Refused: NodeMissing") {
		t.Fatalf("terminal-to-waiting update not observed: %v", run.Annotations)
	}
	if err := api.Delete(ctx, run); err != nil {
		t.Fatal(err)
	}
	roots, err := c.measurementRoots(ctx, c.config.MeasurementNamespaces, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if root.object.GetUID() == run.UID {
			t.Fatal("deleted root retained")
		}
	}
	if api.profilingLists != 1 {
		t.Fatalf("decoded full lists=%d, want one cold baseline", api.profilingLists)
	}
}

func TestMeasurementMetadataDetectsRecreationWithSameResourceVersion(t *testing.T) {
	c, api := terminalAdmissionController(t, 1)
	ctx := context.Background()
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	old := terminalAdmissionRuns(1)[0]
	if err := api.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	replacement := old.(*edge.ProfilingRun)
	replacement.UID = "new-uid"
	replacement.ResourceVersion = ""
	replacement.Status.Conditions = nil
	if err := api.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	if api.profilingGets != 1 {
		t.Fatalf("gets=%d, want new payload despite same RV", api.profilingGets)
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(replacement), replacement); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(replacement.Annotations[profilingrun.AdmissionAnnotation], "Refused: NodeMissing") {
		t.Fatalf("recreated identity missed: %v", replacement.Annotations)
	}
}

func TestMeasurementHolderUsesCurrentMetadata(t *testing.T) {
	c, api := terminalAdmissionController(t, 1)
	ctx := context.Background()
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	run := &edge.ProfilingRun{}
	if err := api.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: "terminal-00000"}, run); err != nil {
		t.Fatal(err)
	}
	run.Status.Conditions = nil
	if err := api.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	node := core.Node{Spec: core.NodeSpec{Taints: []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: string(run.UID), Effect: core.TaintEffectNoSchedule}}}}
	roots, err := c.measurementRoots(ctx, c.config.MeasurementNamespaces, []core.Node{node})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].terminal {
		t.Fatalf("stale terminal state used for holder: %+v", roots)
	}
}

func TestMeasurementRootsRetainReferencedTerminalBEO(t *testing.T) {
	c, api := terminalAdmissionController(t, 0)
	ctx := context.Background()
	beo := &edge.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "source", UID: "source-uid"}}
	beo.Status.Phase = catalogCompletePhase
	beo.Spec.State.TargetNode = "measured-node"
	measurement := &edge.DispatchOccupancyMeasurement{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "waiting", UID: "waiting-uid"}}
	measurement.Spec.BatchEnergyOptimization = beo.Name
	if err := api.Create(ctx, beo); err != nil {
		t.Fatal(err)
	}
	if err := api.Create(ctx, measurement); err != nil {
		t.Fatal(err)
	}
	roots, err := c.measurementRoots(ctx, c.config.MeasurementNamespaces, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots=%d, want waiting measurement and completed BEO", len(roots))
	}
	for _, root := range roots {
		if source, ok := root.object.(*edge.BatchEnergyOptimization); ok && source.Spec.State.TargetNode != beo.Spec.State.TargetNode {
			t.Fatalf("terminal BEO payload lost: %+v", source)
		}
	}
}

type delayedAdmissionMetadata struct {
	client.Client
	kind    string
	entered chan struct{}
	release chan struct{}
}

func (c delayedAdmissionMetadata) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if metadata, ok := list.(*meta.PartialObjectMetadataList); ok && metadata.Kind == c.kind {
		close(c.entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.release:
		}
	}
	return c.Client.List(ctx, list, opts...)
}

func TestMeasurementMetadataBoundaryHonorsRoundCancellation(t *testing.T) {
	c, api := terminalAdmissionController(t, 0)
	blocked := delayedAdmissionMetadata{Client: api, kind: "BatchEnergyOptimizationList", entered: make(chan struct{}), release: make(chan struct{})}
	c.store.client = blocked
	round, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Admit(round) }()
	<-blocked.entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Admit error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		close(blocked.release)
		<-done
		t.Fatal("metadata boundary ignored round cancellation and held tickMu")
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked after round cancellation")
	}
}

func TestAdmitWaitsForAllKindMetadataBeforeOrderingRoots(t *testing.T) {
	h := newAdmissionHarness(t)
	h.admit()
	early := baselineRoot("early", time.Now().Add(-time.Minute))
	late := &edge.ProfilingRun{ObjectMeta: meta.ObjectMeta{Namespace: early.Namespace, Name: "late", UID: "late-uid", CreationTimestamp: meta.Now()}}
	late.Spec.TargetNode = early.Spec.State.TargetNode
	h.create(early, late)
	delayed := delayedAdmissionMetadata{Client: h.controller.store.client, kind: "ModelLoadedIdleBaselineList", entered: make(chan struct{}), release: make(chan struct{})}
	h.controller.store.client = delayed
	done := make(chan error, 1)
	go func() { done <- h.controller.Admit(context.Background()) }()
	<-delayed.entered
	if holder := h.holder(); holder != "" {
		close(delayed.release)
		<-done
		t.Fatalf("admitted %s before earlier kind's metadata", holder)
	}
	select {
	case err := <-done:
		close(delayed.release)
		t.Fatalf("Admit escaped incomplete boundary: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(delayed.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if holder := h.holder(); holder != string(early.UID) {
		t.Fatalf("holder=%s, want earlier root %s", holder, early.UID)
	}
	if got := h.admission(late); got != "" {
		t.Fatalf("later root bypassed earlier kind: %s", got)
	}
}
