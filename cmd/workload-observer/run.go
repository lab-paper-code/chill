package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	workloadkube "github.com/lab-paper-code/chill/internal/workloadobserver/kube"
)

const (
	signalPollInterval    = 10 * time.Millisecond
	endFenceContainerName = "metric-observer"
)

type observation struct {
	result workloadobserver.Result
	err    error
}

func execute(
	ctx context.Context,
	opts options,
	coreClient typedcorev1.CoreV1Interface,
	pollInterval time.Duration,
) error {
	if coreClient == nil {
		return errors.New("Kubernetes core client is required")
	}
	intent, err := worker.LoadVerifiedIntent(opts.intentPath, maximumIntentBytes)
	if err != nil {
		return err
	}
	intentRecord, err := intent.Record()
	if err != nil {
		return err
	}
	targetNode := intentRecord.TargetNode
	pod, err := coreClient.Pods(opts.podNamespace).Get(ctx, opts.podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get workload-observer Pod: %w", err)
	}
	if string(pod.UID) != opts.podUID || pod.Spec.NodeName != targetNode.Name {
		return errors.New("workload-observer Pod identity does not match the frozen Run intent")
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "Job" || string(owner.UID) == "" {
		return errors.New("workload-observer Pod must have a Job controller owner")
	}
	keys, err := opts.selectorLabelKeys()
	if err != nil {
		return err
	}
	observer, err := workloadkube.New(coreClient.Pods(metav1.NamespaceAll))
	if err != nil {
		return err
	}
	request := workloadkube.Request{
		Target: workloadobserver.TargetIdentity{
			NodeName: targetNode.Name,
			NodeUID:  targetNode.UID,
		},
		Run: workloadobserver.RunIdentity{
			Namespace: opts.podNamespace,
			JobUID:    string(owner.UID),
			PodName:   opts.podName,
			PodUID:    opts.podUID,
		},
		SelectorLabelKeys:     keys,
		EventLimit:            opts.maxEvents,
		EndFenceContainerName: endFenceContainerName,
		EndFenceTimeout:       opts.endFenceTimeout,
	}
	if err := observer.Probe(ctx, request); err != nil {
		return fmt.Errorf("probe workload observation: %w", err)
	}
	if err := worker.PublishSignal(opts.readyPath); err != nil {
		return fmt.Errorf("publish workload-observer-ready: %w", err)
	}
	if err := worker.WaitForSignal(ctx, opts.windowStartPath, opts.endWaitTimeout, pollInterval); err != nil {
		return fmt.Errorf("wait for workload-window-start: %w", err)
	}

	observeContext, stopObservation := context.WithCancel(ctx)
	defer stopObservation()
	started := make(chan struct{})
	observed := make(chan observation, 1)
	go func() {
		result, observeErr := observer.Observe(observeContext, request, func() { close(started) })
		observed <- observation{result: result, err: observeErr}
	}()

	select {
	case outcome := <-observed:
		return observationStartError(outcome)
	case <-started:
	case <-ctx.Done():
		stopObservation()
		<-observed
		return fmt.Errorf("workload observation interrupted before ready: %w", ctx.Err())
	}
	if err := worker.PublishSignal(opts.windowReadyPath); err != nil {
		stopObservation()
		<-observed
		return fmt.Errorf("publish workload-window-ready: %w", err)
	}

	waitContext, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	endSignal := make(chan error, 1)
	go func() {
		endSignal <- worker.WaitForSignal(waitContext, opts.endPath, opts.endWaitTimeout, pollInterval)
	}()

	var outcome observation
	select {
	case err := <-endSignal:
		if err != nil {
			stopObservation()
			<-observed
			return fmt.Errorf("wait for measurement-end: %w", err)
		}
		stopObservation()
		outcome = <-observed
	case outcome = <-observed:
		stopWaiting()
	}
	stopObservation()
	if outcome.err != nil {
		return outcome.err
	}
	if err := worker.PublishWorkloadResult(opts.resultPath, opts.maxResultBytes, outcome.result); err != nil {
		return err
	}
	logWorkloadSummary(outcome.result)
	return nil
}

func observationStartError(outcome observation) error {
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.result.Failure != nil {
		return fmt.Errorf("workload observation failed before ready: %s", outcome.result.Failure.Message)
	}
	return errors.New("workload observation stopped before ready")
}

func logWorkloadSummary(result workloadobserver.Result) {
	entered, updated, exited := 0, 0, 0
	for _, event := range result.Events {
		switch event.Kind {
		case workloadobserver.EventEntered:
			entered++
		case workloadobserver.EventUpdated:
			updated++
		case workloadobserver.EventExited:
			exited++
		}
	}
	failure := "none"
	if result.Failure != nil {
		failure = string(result.Failure.Reason)
	}
	log.Printf(
		"workload observation completed\n"+
			"  node: %s\n"+
			"  coverage: %s\n"+
			"  initial/final active Pods: %d/%d\n"+
			"  material events: %d entered, %d updated, %d exited\n"+
			"  resourceVersion boundary: %s -> %s\n"+
			"  failure: %s",
		result.Target.NodeName,
		coverageLabel(result),
		len(result.InitialActivePods),
		len(result.FinalActivePods),
		entered,
		updated,
		exited,
		result.InitialResourceVersion,
		result.LastResourceVersion,
		failure,
	)
}

func coverageLabel(result workloadobserver.Result) string {
	if result.Continuous {
		return "continuous"
	}
	return "incomplete"
}
