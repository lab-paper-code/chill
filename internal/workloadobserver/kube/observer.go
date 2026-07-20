// Package kube observes active Pods on one Kubernetes Node and projects their
// LIST/WATCH history into the Kubernetes-free workloadobserver contract.
package kube

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

var policyDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// PodSource is satisfied by the cluster-wide core/v1 PodInterface returned by
// a Kubernetes client. Keeping this surface small makes LIST/WATCH behavior
// testable without a clientset.
type PodSource interface {
	List(context.Context, metav1.ListOptions) (*corev1.PodList, error)
	Watch(context.Context, metav1.ListOptions) (watch.Interface, error)
}

// Request contains identities and bounds already frozen for one profiling Run.
// SelectorLabelKeys limits captured labels to facts needed by the separately
// frozen allowed-background policy.
type Request struct {
	Target            workloadobserver.TargetIdentity
	Run               workloadobserver.RunIdentity
	PolicyDigest      string
	SelectorLabelKeys []string
	EventLimit        int
}

// Observer owns one bounded LIST/WATCH session.
type Observer struct {
	source PodSource
	now    func() time.Time
}

func New(source PodSource) (*Observer, error) {
	if source == nil {
		return nil, errors.New("workload observer Pod source is required")
	}
	return &Observer{source: source, now: time.Now}, nil
}

// Observe lists active Pods, establishes a watch from the list resourceVersion,
// then calls onStarted exactly once. Cancellation ends a continuous observation;
// post-start watch failures are retained as typed, incomplete evidence.
func (o *Observer) Observe(
	ctx context.Context,
	request Request,
	onStarted func(),
) (workloadobserver.Result, error) {
	if err := validateRequest(request, onStarted); err != nil {
		return workloadobserver.Result{}, err
	}

	selector := fields.OneTermEqualSelector("spec.nodeName", request.Target.NodeName).String()
	list, err := o.source.List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil {
		return workloadobserver.Result{}, fmt.Errorf("list Pods on Node %q: %w", request.Target.NodeName, err)
	}
	if strings.TrimSpace(list.ResourceVersion) == "" {
		return workloadobserver.Result{}, errors.New("list Pods: Kubernetes returned an empty resourceVersion")
	}

	active, err := initialActivePods(list.Items, request)
	if err != nil {
		return workloadobserver.Result{}, err
	}

	options := metav1.ListOptions{
		FieldSelector:       selector,
		ResourceVersion:     list.ResourceVersion,
		AllowWatchBookmarks: true,
	}
	first, err := o.source.Watch(ctx, options)
	if err != nil {
		return workloadobserver.Result{}, fmt.Errorf("watch Pods on Node %q: %w", request.Target.NodeName, err)
	}
	if first == nil {
		return workloadobserver.Result{}, errors.New("watch Pods: Kubernetes returned a nil watcher")
	}

	continuation := &continuingWatcher{
		ctx:           ctx,
		source:        o.source,
		fieldSelector: selector,
		first:         first,
	}
	retry, err := watchtools.NewRetryWatcher(list.ResourceVersion, continuation)
	if err != nil {
		first.Stop()
		return workloadobserver.Result{}, fmt.Errorf("create retrying Pod watch: %w", err)
	}
	defer retry.Stop()

	startedAt := o.now()
	result := workloadobserver.Result{
		SchemaVersion:          workloadobserver.ResultSchemaVersion,
		Target:                 request.Target,
		Run:                    request.Run,
		PolicyDigest:           request.PolicyDigest,
		CoverageStartedAt:      startedAt,
		InitialResourceVersion: list.ResourceVersion,
		LastResourceVersion:    list.ResourceVersion,
		EventLimit:             request.EventLimit,
		InitialActivePods:      orderedPods(active),
	}

	onStarted()
	return o.receive(ctx, retry, request, active, result)
}

func (o *Observer) receive(
	ctx context.Context,
	retry *watchtools.RetryWatcher,
	request Request,
	active map[string]workloadobserver.Pod,
	result workloadobserver.Result,
) (workloadobserver.Result, error) {
	lastObservedAt := result.CoverageStartedAt
	finish := func(failure *workloadobserver.Failure) (workloadobserver.Result, error) {
		endedAt := o.nextTime(lastObservedAt)
		result.CoverageEndedAt = endedAt
		result.FinalActivePods = orderedPods(active)
		result.Continuous = failure == nil
		result.Failure = failure
		if failure != nil {
			result.Failure.ObservedAt = endedAt
		}
		if err := result.Validate(); err != nil {
			return workloadobserver.Result{}, fmt.Errorf("validate projected workload observation: %w", err)
		}
		return result, nil
	}
	fail := func(reason workloadobserver.FailureReason, message string) (workloadobserver.Result, error) {
		return finish(&workloadobserver.Failure{Reason: reason, Message: message})
	}

	for {
		select {
		case <-ctx.Done():
			return finish(nil)
		case event, ok := <-retry.ResultChan():
			if !ok {
				if ctx.Err() != nil {
					return finish(nil)
				}
				return fail(workloadobserver.FailureReasonWatchFailed, "retrying Kubernetes Pod watch stopped")
			}

			if event.Type == watch.Error {
				reason, message := classifyWatchError(event.Object)
				return fail(reason, message)
			}

			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				return fail(workloadobserver.FailureReasonWatchFailed, fmt.Sprintf("Pod watch returned %T", event.Object))
			}
			if strings.TrimSpace(pod.ResourceVersion) == "" {
				return fail(workloadobserver.FailureReasonWatchFailed, "Pod watch event has an empty resourceVersion")
			}
			result.LastResourceVersion = pod.ResourceVersion

			projected, err := projectPod(*pod, request.Target.NodeName, request.SelectorLabelKeys)
			if err != nil {
				return fail(workloadobserver.FailureReasonWatchFailed, err.Error())
			}
			kind, material := transition(event.Type, projected, active)
			if !material {
				continue
			}
			if len(result.Events) == request.EventLimit {
				return fail(
					workloadobserver.FailureReasonEventLimitExceeded,
					fmt.Sprintf("workload observation exceeded its %d material-event limit", request.EventLimit),
				)
			}

			observedAt := o.nextTime(lastObservedAt)
			lastObservedAt = observedAt
			result.Events = append(result.Events, workloadobserver.Event{
				Kind:            kind,
				ObservedAt:      observedAt,
				ResourceVersion: pod.ResourceVersion,
				Pod:             projected,
			})
			applyTransition(kind, projected, active)
		}
	}
}

func initialActivePods(items []corev1.Pod, request Request) (map[string]workloadobserver.Pod, error) {
	active := make(map[string]workloadobserver.Pod, len(items))
	for index := range items {
		pod, err := projectPod(items[index], request.Target.NodeName, request.SelectorLabelKeys)
		if err != nil {
			return nil, fmt.Errorf("project listed Pod: %w", err)
		}
		if !isActive(pod.Phase) {
			continue
		}
		if _, duplicate := active[pod.UID]; duplicate {
			return nil, fmt.Errorf("list Pods: duplicate Pod UID %q", pod.UID)
		}
		active[pod.UID] = pod
	}
	run, present := active[request.Run.PodUID]
	if !present || run.Namespace != request.Run.Namespace || run.Name != request.Run.PodName {
		return nil, errors.New("list Pods: active set does not contain the frozen Run Pod identity")
	}
	return active, nil
}

func projectPod(pod corev1.Pod, nodeName string, labelKeys []string) (workloadobserver.Pod, error) {
	if pod.Spec.NodeName != nodeName {
		return workloadobserver.Pod{}, fmt.Errorf(
			"Pod %s/%s belongs to Node %q, expected %q",
			pod.Namespace,
			pod.Name,
			pod.Spec.NodeName,
			nodeName,
		)
	}
	if strings.TrimSpace(pod.Namespace) == "" || strings.TrimSpace(pod.Name) == "" ||
		strings.TrimSpace(string(pod.UID)) == "" || strings.TrimSpace(string(pod.Status.Phase)) == "" {
		return workloadobserver.Pod{}, fmt.Errorf("Pod %s/%s has incomplete identity or phase", pod.Namespace, pod.Name)
	}

	images := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers)+len(pod.Spec.EphemeralContainers))
	for index := range pod.Spec.InitContainers {
		images = append(images, pod.Spec.InitContainers[index].Image)
	}
	for index := range pod.Spec.Containers {
		images = append(images, pod.Spec.Containers[index].Image)
	}
	for index := range pod.Spec.EphemeralContainers {
		images = append(images, pod.Spec.EphemeralContainers[index].Image)
	}
	images = canonicalStrings(images)
	if len(images) == 0 || images[0] == "" {
		return workloadobserver.Pod{}, fmt.Errorf("Pod %s/%s has an empty container image", pod.Namespace, pod.Name)
	}

	projected := workloadobserver.Pod{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		UID:       string(pod.UID),
		Phase:     string(pod.Status.Phase),
		Images:    images,
	}
	if owner := metav1.GetControllerOf(&pod); owner != nil {
		projected.Owner = &workloadobserver.OwnerIdentity{
			Kind: owner.Kind,
			Name: owner.Name,
			UID:  string(owner.UID),
		}
	}
	for _, key := range labelKeys {
		if value, present := pod.Labels[key]; present {
			if projected.Labels == nil {
				projected.Labels = make(map[string]string)
			}
			projected.Labels[key] = value
		}
	}
	return projected, nil
}

func transition(
	eventType watch.EventType,
	pod workloadobserver.Pod,
	active map[string]workloadobserver.Pod,
) (workloadobserver.EventKind, bool) {
	current, exists := active[pod.UID]
	switch eventType {
	case watch.Added, watch.Modified:
		if !isActive(pod.Phase) {
			if exists {
				return workloadobserver.EventExited, true
			}
			return "", false
		}
		if !exists {
			return workloadobserver.EventEntered, true
		}
		if reflect.DeepEqual(current, pod) {
			return "", false
		}
		return workloadobserver.EventUpdated, true
	case watch.Deleted:
		if exists {
			return workloadobserver.EventExited, true
		}
		return "", false
	default:
		return "", false
	}
}

func applyTransition(kind workloadobserver.EventKind, pod workloadobserver.Pod, active map[string]workloadobserver.Pod) {
	if kind == workloadobserver.EventExited {
		delete(active, pod.UID)
		return
	}
	active[pod.UID] = pod
}

func classifyWatchError(object runtime.Object) (workloadobserver.FailureReason, string) {
	err := apierrors.FromObject(object)
	if apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
		return workloadobserver.FailureReasonHistoryExpired, err.Error()
	}
	return workloadobserver.FailureReasonWatchFailed, err.Error()
}

type continuingWatcher struct {
	ctx           context.Context
	source        PodSource
	fieldSelector string

	mu    sync.Mutex
	first watch.Interface
}

var _ cache.Watcher = (*continuingWatcher)(nil)

func (w *continuingWatcher) Watch(options metav1.ListOptions) (watch.Interface, error) {
	w.mu.Lock()
	if w.first != nil {
		first := w.first
		w.first = nil
		w.mu.Unlock()
		return first, nil
	}
	w.mu.Unlock()

	options.FieldSelector = w.fieldSelector
	options.AllowWatchBookmarks = true
	return w.source.Watch(w.ctx, options)
}

func validateRequest(request Request, onStarted func()) error {
	if onStarted == nil {
		return errors.New("workload observer start callback is required")
	}
	for name, value := range map[string]string{
		"target Node name": request.Target.NodeName,
		"target Node UID":  request.Target.NodeUID,
		"Run namespace":    request.Run.Namespace,
		"Run Job UID":      request.Run.JobUID,
		"Run Pod name":     request.Run.PodName,
		"Run Pod UID":      request.Run.PodUID,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("workload observer %s is required without surrounding whitespace", name)
		}
	}
	if !policyDigestPattern.MatchString(request.PolicyDigest) {
		return errors.New("workload observer policy digest is invalid")
	}
	if request.EventLimit < 1 {
		return errors.New("workload observer event limit must be positive")
	}
	previous := ""
	for _, key := range request.SelectorLabelKeys {
		if strings.TrimSpace(key) == "" || key <= previous {
			return errors.New("workload observer selector label keys must be non-empty, unique, and sorted")
		}
		previous = key
	}
	return nil
}

func canonicalStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func orderedPods(active map[string]workloadobserver.Pod) []workloadobserver.Pod {
	pods := make([]workloadobserver.Pod, 0, len(active))
	for _, pod := range active {
		pods = append(pods, pod)
	}
	sort.Slice(pods, func(i, j int) bool {
		left := pods[i].Namespace + "\x00" + pods[i].Name + "\x00" + pods[i].UID
		right := pods[j].Namespace + "\x00" + pods[j].Name + "\x00" + pods[j].UID
		return left < right
	})
	return pods
}

func isActive(phase string) bool {
	return phase == string(corev1.PodPending) || phase == string(corev1.PodRunning) || phase == string(corev1.PodUnknown)
}

func (o *Observer) nextTime(previous time.Time) time.Time {
	now := o.now()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}
