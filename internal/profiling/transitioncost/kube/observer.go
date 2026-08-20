// Package kube observes one NodeActuation and its selected serving Pod through
// the Kubernetes API, projecting only factual transition observations into the
// Kubernetes-independent transitioncost contract.
package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

// Request contains the operational selection and bounds for one off-target
// transition-fact observation. Expected state comparison remains Derive's job.
type Request struct {
	Namespace                  string
	NodeActuationName          string
	TargetNode                 string
	ServingService             string
	ServingContainer           string
	DeviceType                 string
	Model                      string
	PollInterval               time.Duration
	ObservationTimeout         time.Duration
	BoundaryMappingUncertainty time.Duration
}

// Observer polls the Kubernetes API without creating or mutating objects.
type Observer struct {
	reader  client.Reader
	request Request
	now     func() time.Time
}

// New constructs one observer and owns a value copy of its request.
func New(reader client.Reader, request Request) (*Observer, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes reader is required")
	}
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	return &Observer{reader: reader, request: request, now: time.Now}, nil
}

// Observe waits for one initial Ready serving process before reporting ready,
// then retains NodeActuation status facts and closes only on a fresh post-
// read-back Ready serving-process observation.
func (o *Observer) Observe(
	ctx context.Context,
	onReady func(),
) (transitioncost.RawObservation, error) {
	startedAt := o.now().UTC()
	raw := transitioncost.RawObservation{
		ObservationStartedAt:       startedAt,
		BoundaryMappingUncertainty: o.request.BoundaryMappingUncertainty,
	}
	finish := func(err error) (transitioncost.RawObservation, error) {
		raw.ObservationEndedAt = o.now().UTC()
		return raw, err
	}
	if onReady == nil {
		return finish(errors.New("transition fact ready callback is required"))
	}

	observeCtx, cancel := context.WithTimeout(ctx, o.request.ObservationTimeout)
	defer cancel()
	selector, err := o.serviceSelector(observeCtx)
	if err != nil {
		return finish(err)
	}

	var lastReady *bool
	for {
		snapshot, err := o.servingSnapshot(observeCtx, selector)
		if err != nil {
			return finish(err)
		}
		appendSnapshotReadiness(&raw, snapshot, &lastReady, false)
		if snapshot.process != nil && snapshot.readiness != nil && snapshot.readiness.Ready {
			raw.PreTransitionProcess = &transitioncost.ProcessObservation{
				ObservedAt: snapshot.readiness.ObservedAt,
				Process:    *snapshot.process,
			}
			break
		}
		if err := wait(observeCtx, o.request.PollInterval); err != nil {
			return finish(fmt.Errorf("observe Kubernetes transition facts: %w", err))
		}
	}

	onReady()
	for {
		terminal, err := o.observeActuation(observeCtx, &raw)
		if err != nil {
			return finish(err)
		}
		if terminal {
			return finish(nil)
		}

		// This LIST occurs after the NodeActuation GET in the same poll. Once
		// target read-back is retained, it is therefore a fresh readiness fact.
		snapshot, err := o.servingSnapshot(observeCtx, selector)
		if err != nil {
			return finish(err)
		}
		if raw.TargetReadBack != nil && snapshot.process != nil &&
			snapshot.readiness != nil && snapshot.readiness.Ready {
			appendSnapshotReadiness(&raw, snapshot, &lastReady, true)
			raw.PostTransitionProcess = &transitioncost.ProcessObservation{
				ObservedAt: snapshot.readiness.ObservedAt,
				Process:    *snapshot.process,
			}
			return finish(nil)
		}
		appendSnapshotReadiness(&raw, snapshot, &lastReady, false)

		if err := wait(observeCtx, o.request.PollInterval); err != nil {
			return finish(fmt.Errorf("observe Kubernetes transition facts: %w", err))
		}
	}
}

func (o *Observer) serviceSelector(ctx context.Context) (map[string]string, error) {
	service := &corev1.Service{}
	key := client.ObjectKey{Namespace: o.request.Namespace, Name: o.request.ServingService}
	if err := o.reader.Get(ctx, key, service); err != nil {
		return nil, fmt.Errorf("get serving Service %s: %w", key, err)
	}
	if len(service.Spec.Selector) == 0 {
		return nil, fmt.Errorf("serving Service %s has no selector", key)
	}
	selector := make(map[string]string, len(service.Spec.Selector))
	for key, value := range service.Spec.Selector {
		selector[key] = value
	}
	return selector, nil
}

func (o *Observer) servingSnapshot(
	ctx context.Context,
	selector map[string]string,
) (servingSnapshot, error) {
	pods := &corev1.PodList{}
	if err := o.reader.List(
		ctx,
		pods,
		client.InNamespace(o.request.Namespace),
		client.MatchingLabels(selector),
	); err != nil {
		return servingSnapshot{}, fmt.Errorf("list serving Pods: %w", err)
	}
	receivedAt := o.now().UTC()
	var selected *corev1.Pod
	for index := range pods.Items {
		if pods.Items[index].Spec.NodeName == o.request.TargetNode {
			if selected != nil {
				return servingSnapshot{}, nil
			}
			selected = &pods.Items[index]
		}
	}
	if selected == nil {
		return servingSnapshot{}, nil
	}
	return projectServingPod(*selected, o.request.ServingContainer, receivedAt), nil
}

func (o *Observer) observeActuation(
	ctx context.Context,
	raw *transitioncost.RawObservation,
) (bool, error) {
	actuation := &edgev1alpha1.NodeActuation{}
	key := client.ObjectKey{Namespace: o.request.Namespace, Name: o.request.NodeActuationName}
	if err := o.reader.Get(ctx, key, actuation); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get NodeActuation %s: %w", key, err)
	}
	receivedAt := o.now().UTC()
	if err := projectActuation(raw, actuation.Status, o.request.DeviceType, o.request.Model, receivedAt); err != nil {
		return false, err
	}
	return actuation.Status.TerminalFailure != nil &&
		actuation.Status.TerminalFailure.Stage != edgev1alpha1.NodeActuationFailureStageSettle, nil
}

func validateRequest(request Request) error {
	for name, value := range map[string]string{
		"namespace": request.Namespace, "NodeActuation name": request.NodeActuationName,
		"target Node": request.TargetNode, "serving Service": request.ServingService,
		"serving container": request.ServingContainer, "device type": request.DeviceType,
		"model": request.Model,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if request.PollInterval <= 0 {
		return errors.New("poll interval must be positive")
	}
	if request.ObservationTimeout <= 0 {
		return errors.New("observation timeout must be positive")
	}
	if request.BoundaryMappingUncertainty < 0 {
		return errors.New("boundary mapping uncertainty cannot be negative")
	}
	if request.BoundaryMappingUncertainty < request.PollInterval {
		return errors.New("boundary mapping uncertainty must cover the poll interval")
	}
	return nil
}

func wait(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
