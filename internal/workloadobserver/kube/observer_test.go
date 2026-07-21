package kube

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

const testPolicyDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestProbeEstablishesAndClosesUnretainedWatch(t *testing.T) {
	preflight := watch.NewRaceFreeFake()
	source := sourceWithRunPod(preflight)
	observer := mustObserver(t, source)

	if err := observer.Probe(context.Background(), testRequest(4)); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	assertOptions(t, source.listOptions, "")
	assertOptions(t, <-source.watchCalls, "10")
}

func TestObserveProjectsContinuousPodHistory(t *testing.T) {
	run := testPod("chill-system", "run", "run-uid", "10", corev1.PodRunning)
	background := testPod("monitoring", "agent", "agent-uid", "10", corev1.PodRunning)
	background.Labels = map[string]string{"app": "agent", "ignored": "value"}
	controller := true
	background.OwnerReferences = []metav1.OwnerReference{{
		Kind:       "DaemonSet",
		Name:       "agent",
		UID:        types.UID("owner-uid"),
		Controller: &controller,
	}}

	watcher := watch.NewRaceFreeFake()
	fence := watch.NewRaceFreeFake()
	source := &fakePodSource{
		list: &corev1.PodList{
			ListMeta: metav1.ListMeta{ResourceVersion: "10"},
			Items:    []corev1.Pod{background, run},
		},
		watchers:   []watch.Interface{watcher, fence},
		watchCalls: make(chan metav1.ListOptions, 2),
	}
	observer := mustObserver(t, source)
	ticks := installTestClock(observer)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	resultCh := observeAsync(observer, ctx, testRequest(8), started)

	waitClosed(t, started)
	waitTick(t, ticks) // coverage start
	assertOptions(t, source.listOptions, "")
	assertOptions(t, <-source.watchCalls, "10")

	transient := testPod("default", "transient", "transient-uid", "11", corev1.PodPending)
	transient.Labels = map[string]string{"app": "load", "ignored": "value"}
	watcher.Add(transient.DeepCopy())
	waitTick(t, ticks)

	noOp := transient.DeepCopy()
	noOp.ResourceVersion = "12"
	watcher.Modify(noOp)

	updated := transient.DeepCopy()
	updated.ResourceVersion = "13"
	updated.Status.Phase = corev1.PodRunning
	watcher.Modify(updated)
	waitTick(t, ticks)

	exited := updated.DeepCopy()
	exited.ResourceVersion = "14"
	watcher.Delete(exited)
	waitTick(t, ticks)
	cancel()
	fenceOptions := waitWatchCall(t, source.watchCalls)
	assertOptions(t, fenceOptions, "14")
	if fenceOptions.TimeoutSeconds == nil || *fenceOptions.TimeoutSeconds != 3 {
		t.Fatalf("end-fence timeoutSeconds = %v, want 3", fenceOptions.TimeoutSeconds)
	}
	sendBookmark(fence, "15")

	result := waitResult(t, resultCh)
	if err := result.err; err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if err := result.value.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !result.value.Continuous || result.value.Failure != nil {
		t.Fatalf("continuity = %v, failure = %#v", result.value.Continuous, result.value.Failure)
	}
	if result.value.LastResourceVersion != "15" {
		t.Fatalf("last resourceVersion = %q, want end-fence bookmark 15", result.value.LastResourceVersion)
	}
	wantKinds := []workloadobserver.EventKind{
		workloadobserver.EventEntered,
		workloadobserver.EventUpdated,
		workloadobserver.EventExited,
	}
	gotKinds := make([]workloadobserver.EventKind, 0, len(result.value.Events))
	for _, event := range result.value.Events {
		gotKinds = append(gotKinds, event.Kind)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", gotKinds, wantKinds)
	}
	got := findPod(t, result.value.InitialActivePods, "agent-uid")
	if !reflect.DeepEqual(got.Labels, map[string]string{"app": "agent"}) {
		t.Fatalf("captured labels = %#v, want only selector label", got.Labels)
	}
	if owner := got.Owner; owner == nil || owner.Kind != "DaemonSet" || owner.UID != "owner-uid" {
		t.Fatalf("captured owner = %#v", owner)
	}
}

func TestObserveClassifiesExpiredHistory(t *testing.T) {
	watcher := watch.NewRaceFreeFake()
	source := sourceWithRunPod(watcher)
	observer := mustObserver(t, source)
	installTestClock(observer)
	started := make(chan struct{})
	resultCh := observeAsync(observer, context.Background(), testRequest(4), started)

	waitClosed(t, started)
	expired := apierrors.NewResourceExpired("resource version 10 is too old")
	watcher.Error(&expired.ErrStatus)

	result := waitResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("Observe() error = %v", result.err)
	}
	if result.value.Continuous || result.value.Failure == nil ||
		result.value.Failure.Reason != workloadobserver.FailureReasonHistoryExpired {
		t.Fatalf("result continuity/failure = %v/%#v", result.value.Continuous, result.value.Failure)
	}
	if err := result.value.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestObserveFailsClosedAtMaterialEventLimit(t *testing.T) {
	watcher := watch.NewRaceFreeFake()
	source := sourceWithRunPod(watcher)
	observer := mustObserver(t, source)
	installTestClock(observer)
	started := make(chan struct{})
	resultCh := observeAsync(observer, context.Background(), testRequest(1), started)

	waitClosed(t, started)
	first := testPod("default", "first", "first-uid", "11", corev1.PodRunning)
	second := testPod("default", "second", "second-uid", "12", corev1.PodRunning)
	watcher.Add(first.DeepCopy())
	watcher.Add(second.DeepCopy())

	result := waitResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("Observe() error = %v", result.err)
	}
	if len(result.value.Events) != 1 {
		t.Fatalf("retained events = %d, want 1", len(result.value.Events))
	}
	if result.value.Failure == nil || result.value.Failure.Reason != workloadobserver.FailureReasonEventLimitExceeded {
		t.Fatalf("failure = %#v", result.value.Failure)
	}
	if result.value.LastResourceVersion != "12" {
		t.Fatalf("last resourceVersion = %q, want triggering event 12", result.value.LastResourceVersion)
	}
	if err := result.value.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestObserveReestablishesClosedWatchFromLastResourceVersion(t *testing.T) {
	first := watch.NewRaceFreeFake()
	second := watch.NewRaceFreeFake()
	fence := watch.NewRaceFreeFake()
	source := sourceWithRunPod(first, second, fence)
	observer := mustObserver(t, source)
	ticks := installTestClock(observer)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	resultCh := observeAsync(observer, ctx, testRequest(4), started)

	waitClosed(t, started)
	waitTick(t, ticks) // coverage start
	initialOptions := <-source.watchCalls
	assertOptions(t, initialOptions, "10")
	first.Stop()
	retryOptions := waitWatchCall(t, source.watchCalls)
	assertOptions(t, retryOptions, "10")

	reconnected := testPod("default", "after-reconnect", "reconnect-uid", "11", corev1.PodRunning)
	second.Add(reconnected.DeepCopy())
	waitTick(t, ticks)
	cancel()
	assertOptions(t, waitWatchCall(t, source.watchCalls), "11")
	sendBookmark(fence, "12")

	result := waitResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("Observe() error = %v", result.err)
	}
	if !result.value.Continuous || len(result.value.Events) != 1 {
		t.Fatalf("continuous/events = %v/%d", result.value.Continuous, len(result.value.Events))
	}
}

func TestObserveReplaysPendingEventsBeforeEndFence(t *testing.T) {
	initial := watch.NewRaceFreeFake()
	fence := watch.NewRaceFreeFake()
	source := sourceWithRunPod(initial, fence)
	observer := mustObserver(t, source)
	installTestClock(observer)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	resultCh := observeAsync(observer, ctx, testRequest(4), started)

	waitClosed(t, started)
	assertOptions(t, <-source.watchCalls, "10")
	cancel()
	assertOptions(t, waitWatchCall(t, source.watchCalls), "10")

	transient := testPod("default", "pending-at-end", "pending-uid", "11", corev1.PodRunning)
	fence.Add(transient.DeepCopy())
	sendBookmark(fence, "12")

	result := waitResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("Observe() error = %v", result.err)
	}
	if !result.value.Continuous || result.value.LastResourceVersion != "12" {
		t.Fatalf("continuous/last resourceVersion = %v/%q", result.value.Continuous, result.value.LastResourceVersion)
	}
	if len(result.value.Events) != 1 || result.value.Events[0].Pod.UID != "pending-uid" {
		t.Fatalf("events = %#v, want replayed pending Pod", result.value.Events)
	}
}

func TestObserveFailsClosedWhenEndFenceStopsWithoutBookmark(t *testing.T) {
	initial := watch.NewRaceFreeFake()
	fence := watch.NewRaceFreeFake()
	source := sourceWithRunPod(initial, fence)
	observer := mustObserver(t, source)
	installTestClock(observer)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	resultCh := observeAsync(observer, ctx, testRequest(4), started)

	waitClosed(t, started)
	<-source.watchCalls
	cancel()
	assertOptions(t, waitWatchCall(t, source.watchCalls), "10")
	fence.Stop()

	result := waitResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("Observe() error = %v", result.err)
	}
	if result.value.Continuous || result.value.Failure == nil ||
		result.value.Failure.Reason != workloadobserver.FailureReasonEndFenceFailed {
		t.Fatalf("continuity/failure = %v/%#v", result.value.Continuous, result.value.Failure)
	}
}

func TestObserveRejectsPreparationFailuresBeforeStarted(t *testing.T) {
	t.Run("list error", func(t *testing.T) {
		source := &fakePodSource{listErr: errors.New("denied")}
		observer := mustObserver(t, source)
		called := false
		_, err := observer.Observe(context.Background(), testRequest(4), func() { called = true })
		if err == nil || called {
			t.Fatalf("Observe() error/callback = %v/%v", err, called)
		}
	})

	t.Run("watch error", func(t *testing.T) {
		source := sourceWithRunPod()
		source.watchErr = errors.New("denied")
		observer := mustObserver(t, source)
		called := false
		_, err := observer.Observe(context.Background(), testRequest(4), func() { called = true })
		if err == nil || called {
			t.Fatalf("Observe() error/callback = %v/%v", err, called)
		}
	})

	t.Run("Run Pod absent", func(t *testing.T) {
		source := &fakePodSource{list: &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "10"}}}
		observer := mustObserver(t, source)
		called := false
		_, err := observer.Observe(context.Background(), testRequest(4), func() { called = true })
		if err == nil || called {
			t.Fatalf("Observe() error/callback = %v/%v", err, called)
		}
	})
}

func TestObserveFailsClosedOnInvalidWatchObject(t *testing.T) {
	watcher := watch.NewRaceFreeFake()
	source := sourceWithRunPod(watcher)
	observer := mustObserver(t, source)
	installTestClock(observer)
	started := make(chan struct{})
	resultCh := observeAsync(observer, context.Background(), testRequest(4), started)

	waitClosed(t, started)
	status := &metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusSuccess,
		Code:     200,
	}
	status.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Status"})
	watcher.Add(status)

	result := waitResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("Observe() error = %v", result.err)
	}
	if result.value.Failure == nil || result.value.Failure.Reason != workloadobserver.FailureReasonWatchFailed {
		t.Fatalf("failure = %#v", result.value.Failure)
	}
}

type fakePodSource struct {
	mu sync.Mutex

	list        *corev1.PodList
	listErr     error
	listOptions metav1.ListOptions

	watchers   []watch.Interface
	watchErr   error
	watchCalls chan metav1.ListOptions
	watchIndex int
}

func (s *fakePodSource) List(_ context.Context, options metav1.ListOptions) (*corev1.PodList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listOptions = options
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list.DeepCopy(), nil
}

func (s *fakePodSource) Watch(_ context.Context, options metav1.ListOptions) (watch.Interface, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchCalls != nil {
		s.watchCalls <- options
	}
	if s.watchErr != nil {
		return nil, s.watchErr
	}
	if s.watchIndex >= len(s.watchers) {
		return nil, errors.New("unexpected watch call")
	}
	watcher := s.watchers[s.watchIndex]
	s.watchIndex++
	return watcher, nil
}

type observationResult struct {
	value workloadobserver.Result
	err   error
}

func observeAsync(
	observer *Observer,
	ctx context.Context,
	request Request,
	started chan struct{},
) <-chan observationResult {
	result := make(chan observationResult, 1)
	go func() {
		value, err := observer.Observe(ctx, request, func() { close(started) })
		result <- observationResult{value: value, err: err}
	}()
	return result
}

func sourceWithRunPod(watchers ...watch.Interface) *fakePodSource {
	return &fakePodSource{
		list: &corev1.PodList{
			ListMeta: metav1.ListMeta{ResourceVersion: "10"},
			Items:    []corev1.Pod{testPod("chill-system", "run", "run-uid", "10", corev1.PodRunning)},
		},
		watchers:   watchers,
		watchCalls: make(chan metav1.ListOptions, 4),
	}
}

func testRequest(eventLimit int) Request {
	return Request{
		Target: workloadobserver.TargetIdentity{NodeName: "lattepanda", NodeUID: "node-uid"},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system",
			JobUID:    "job-uid",
			PodName:   "run",
			PodUID:    "run-uid",
		},
		PolicyDigest:      testPolicyDigest,
		SelectorLabelKeys: []string{"app"},
		EventLimit:        eventLimit,
		EndFenceTimeout:   3 * time.Second,
	}
}

func sendBookmark(watcher *watch.RaceFreeFakeWatcher, resourceVersion string) {
	watcher.Action(watch.Bookmark, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{ResourceVersion: resourceVersion}})
}

func testPod(namespace, name, uid, resourceVersion string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			Name:            name,
			UID:             types.UID(uid),
			ResourceVersion: resourceVersion,
		},
		Spec: corev1.PodSpec{
			NodeName:   "lattepanda",
			Containers: []corev1.Container{{Name: "runtime", Image: "example/runtime:v1"}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func mustObserver(t *testing.T, source PodSource) *Observer {
	t.Helper()
	observer, err := New(source)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return observer
}

func installTestClock(observer *Observer) <-chan time.Time {
	ticks := make(chan time.Time, 32)
	current := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	observer.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(time.Second)
		ticks <- current
		return current
	}
	return ticks
}

func waitClosed(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for observation start")
	}
}

func waitTick(t *testing.T, ticks <-chan time.Time) {
	t.Helper()
	select {
	case <-ticks:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for projected event")
	}
}

func waitResult(t *testing.T, channel <-chan observationResult) observationResult {
	t.Helper()
	select {
	case result := <-channel:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for observation result")
		return observationResult{}
	}
}

func waitWatchCall(t *testing.T, calls <-chan metav1.ListOptions) metav1.ListOptions {
	t.Helper()
	select {
	case options := <-calls:
		return options
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for watch restart")
		return metav1.ListOptions{}
	}
}

func assertOptions(t *testing.T, got metav1.ListOptions, resourceVersion string) {
	t.Helper()
	const fieldSelector = "spec.nodeName=lattepanda"
	if got.ResourceVersion != resourceVersion || got.FieldSelector != fieldSelector {
		t.Fatalf("list/watch options = RV %q, selector %q; want RV %q, selector %q", got.ResourceVersion, got.FieldSelector, resourceVersion, fieldSelector)
	}
}

func findPod(t *testing.T, pods []workloadobserver.Pod, uid string) workloadobserver.Pod {
	t.Helper()
	for _, pod := range pods {
		if pod.UID == uid {
			return pod
		}
	}
	t.Fatalf("Pod UID %q not found", uid)
	return workloadobserver.Pod{}
}
