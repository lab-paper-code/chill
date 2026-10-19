// Package kube observes active Pods on one Kubernetes Node and projects their
// LIST/WATCH history into the Kubernetes-free workloadobserver contract.
package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

const minimumEndFenceTimeout = 3 * time.Second

// PodSource is satisfied by the cluster-wide core/v1 PodInterface returned by
// a Kubernetes client. Keeping this surface small makes LIST/WATCH behavior
// testable without a clientset.
type PodSource interface {
	List(context.Context, metav1.ListOptions) (*corev1.PodList, error)
	Watch(context.Context, metav1.ListOptions) (watch.Interface, error)
}

// Request contains identities and bounds already frozen for one profiling Run.
// SelectorLabelKeys limits captured labels to facts needed to identify another
// CHILL measurement workload. EndFenceContainerName identifies the companion
// whose successful termination is causally after measurement-end, and
// EndFenceTimeout bounds the wait for that status on the same Pod watch.
type Request struct {
	Target                workloadobserver.TargetIdentity
	Run                   workloadobserver.RunIdentity
	SelectorLabelKeys     []string
	EventLimit            int
	EndFenceContainerName string
	EndFenceTimeout       time.Duration
}

// Observer owns one bounded LIST/WATCH session.
type Observer struct {
	source PodSource
	now    func() time.Time
}

type preparedObservation struct {
	active          map[string]workloadobserver.Pod
	resourceVersion string
	retry           *watchtools.RetryWatcher
}

func New(source PodSource) (*Observer, error) {
	if source == nil {
		return nil, errors.New("workload observer Pod source is required")
	}
	return &Observer{source: source, now: time.Now}, nil
}

// Probe verifies that the frozen Run Pod can be listed and that a Pod watch can
// be established. It intentionally retains no evidence: the measurement-bound
// Observe session must begin only after runtime warm-up has completed.
func (o *Observer) Probe(ctx context.Context, request Request) error {
	if err := validateRequest(request, func() {}); err != nil {
		return err
	}
	prepared, err := o.prepare(ctx, ctx, request)
	if err != nil {
		return err
	}
	prepared.retry.Stop()
	return nil
}

// Observe lists active Pods, establishes a watch from the list resourceVersion,
// then calls onStarted exactly once. After cancellation it drains that same
// watch until the frozen Run Pod reports successful termination of the end-fence
// companion. Post-start failures are retained as incomplete evidence.
func (o *Observer) Observe(
	ctx context.Context,
	request Request,
	onStarted func(),
) (workloadobserver.Result, error) {
	if err := validateRequest(request, onStarted); err != nil {
		return workloadobserver.Result{}, err
	}
	watchContext, stopWatch := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWatch()
	prepared, err := o.prepare(ctx, watchContext, request)
	if err != nil {
		return workloadobserver.Result{}, err
	}
	defer prepared.retry.Stop()

	startedAt := o.now()
	result := workloadobserver.Result{
		SchemaVersion:          workloadobserver.ResultSchemaVersion,
		Target:                 request.Target,
		Run:                    request.Run,
		CoverageStartedAt:      startedAt,
		InitialResourceVersion: prepared.resourceVersion,
		LastResourceVersion:    prepared.resourceVersion,
		EventLimit:             request.EventLimit,
		InitialActivePods:      orderedPods(prepared.active),
	}

	onStarted()
	return o.receive(ctx, prepared.retry, request, prepared.active, result)
}

func (o *Observer) prepare(
	ctx context.Context,
	watchContext context.Context,
	request Request,
) (preparedObservation, error) {
	selector := fields.OneTermEqualSelector("spec.nodeName", request.Target.NodeName).String()
	list, err := o.source.List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil {
		return preparedObservation{}, fmt.Errorf("list Pods on Node %q: %w", request.Target.NodeName, err)
	}
	if list == nil {
		return preparedObservation{}, errors.New("list Pods: Kubernetes returned a nil list")
	}
	if strings.TrimSpace(list.ResourceVersion) == "" {
		return preparedObservation{}, errors.New("list Pods: Kubernetes returned an empty resourceVersion")
	}

	active, err := initialActivePods(list.Items, request)
	if err != nil {
		return preparedObservation{}, err
	}
	first, err := o.source.Watch(watchContext, metav1.ListOptions{
		FieldSelector:       selector,
		ResourceVersion:     list.ResourceVersion,
		AllowWatchBookmarks: true,
	})
	if err != nil {
		return preparedObservation{}, fmt.Errorf("watch Pods on Node %q: %w", request.Target.NodeName, err)
	}
	if first == nil {
		return preparedObservation{}, errors.New("watch Pods: Kubernetes returned a nil watcher")
	}

	continuation := &continuingWatcher{ctx: watchContext, source: o.source, fieldSelector: selector, first: first}
	retry, err := watchtools.NewRetryWatcher(list.ResourceVersion, continuation)
	if err != nil {
		first.Stop()
		return preparedObservation{}, fmt.Errorf("create retrying Pod watch: %w", err)
	}
	return preparedObservation{active: active, resourceVersion: list.ResourceVersion, retry: retry}, nil
}

func (o *Observer) receive(
	ctx context.Context,
	retry *watchtools.RetryWatcher,
	request Request,
	active map[string]workloadobserver.Pod,
	result workloadobserver.Result,
) (workloadobserver.Result, error) {
	lastObservedAt := result.CoverageStartedAt
	endFenceResourceVersion := ""
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

	process := func(event watch.Event) *workloadobserver.Failure {
		if event.Type == watch.Error {
			reason, message := classifyWatchError(event.Object)
			return &workloadobserver.Failure{Reason: reason, Message: message}
		}

		pod, ok := event.Object.(*corev1.Pod)
		if !ok {
			return &workloadobserver.Failure{
				Reason:  workloadobserver.FailureReasonWatchFailed,
				Message: fmt.Sprintf("Pod watch returned %T", event.Object),
			}
		}
		if strings.TrimSpace(pod.ResourceVersion) == "" {
			return &workloadobserver.Failure{
				Reason:  workloadobserver.FailureReasonWatchFailed,
				Message: "Pod watch event has an empty resourceVersion",
			}
		}
		result.LastResourceVersion = pod.ResourceVersion
		if reachesEndFence(*pod, request) {
			endFenceResourceVersion = pod.ResourceVersion
		}

		projected, err := ProjectPod(*pod, request.Target.NodeName, request.SelectorLabelKeys)
		if err != nil {
			return &workloadobserver.Failure{Reason: workloadobserver.FailureReasonWatchFailed, Message: err.Error()}
		}
		kind, material := transition(event.Type, projected, active)
		if !material {
			return nil
		}
		if len(result.Events) == request.EventLimit {
			return &workloadobserver.Failure{
				Reason: workloadobserver.FailureReasonEventLimitExceeded,
				Message: fmt.Sprintf(
					"workload observation exceeded its %d material-event limit",
					request.EventLimit,
				),
			}
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
		return nil
	}
	endFence := func() (workloadobserver.Result, error) {
		if endFenceResourceVersion != "" {
			return finish(nil)
		}
		fenceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), request.EndFenceTimeout)
		defer cancel()

		for {
			select {
			case <-fenceContext.Done():
				return fail(
					workloadobserver.FailureReasonEndFenceFailed,
					fmt.Sprintf(
						"Pod watch did not observe successful termination of end-fence container %q within %s",
						request.EndFenceContainerName,
						request.EndFenceTimeout,
					),
				)
			case event, ok := <-retry.ResultChan():
				if !ok {
					return fail(workloadobserver.FailureReasonEndFenceFailed, "Pod watch stopped before the causal end fence")
				}
				if failure := process(event); failure != nil {
					return fail(
						workloadobserver.FailureReasonEndFenceFailed,
						fmt.Sprintf("Pod watch failed before the causal end fence: %s", failure.Message),
					)
				}
				if endFenceResourceVersion != "" {
					return finish(nil)
				}
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return endFence()
		case event, ok := <-retry.ResultChan():
			if !ok {
				if ctx.Err() != nil {
					return endFence()
				}
				return fail(workloadobserver.FailureReasonWatchFailed, "retrying Kubernetes Pod watch stopped")
			}

			if failure := process(event); failure != nil {
				return finish(failure)
			}
		}
	}
}

func validateRequest(request Request, onStarted func()) error {
	if onStarted == nil {
		return errors.New("workload observer start callback is required")
	}
	for name, value := range map[string]string{
		"target Node name":         request.Target.NodeName,
		"target Node UID":          request.Target.NodeUID,
		"Run namespace":            request.Run.Namespace,
		"Run Job UID":              request.Run.JobUID,
		"Run Pod name":             request.Run.PodName,
		"Run Pod UID":              request.Run.PodUID,
		"end-fence container name": request.EndFenceContainerName,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("workload observer %s is required without surrounding whitespace", name)
		}
	}
	if request.EventLimit < 1 {
		return errors.New("workload observer event limit must be positive")
	}
	if request.EndFenceTimeout < minimumEndFenceTimeout {
		return fmt.Errorf("workload observer end-fence timeout must be at least %s", minimumEndFenceTimeout)
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

func (o *Observer) nextTime(previous time.Time) time.Time {
	now := o.now()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}
