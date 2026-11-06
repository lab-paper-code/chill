package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRuntimeFailureProjectionPreservesContainerAndProvenPhase(t *testing.T) {
	for _, test := range []struct {
		name, container, reason, stage, failure, message string
		class                                            string
		observerOOM                                      bool
	}{
		{name: "active runtime OOM", container: "runtime", reason: "OOMKilled", stage: evidence.StageExecution},
		{name: "startup runtime OOM", container: "runtime", reason: "OOMKilled", stage: evidence.StageStartup},
		{name: "observer OOM", container: "metric-observer", reason: "OOMKilled", stage: evidence.StageExecution},
		{name: "observer OOM outranks runtime fallout", container: "runtime", reason: "OOMKilled", stage: evidence.StageExecution, observerOOM: true},
		{name: "active frozen budget", container: "runtime", reason: "Error", stage: evidence.StageExecution, failure: evidence.FailureMeasurementBudget},
		{name: "active runtime allocation", container: "runtime", reason: "Error", stage: evidence.StageExecution, failure: evidence.FailureMemoryAllocation, class: evidence.FailureClassExpectedStop},
		{name: "startup runtime allocation", container: "runtime", reason: "Error", stage: evidence.StageStartup, failure: evidence.FailureMemoryAllocation, class: evidence.FailureClassFatalFailure},
		{name: "observer stage allocation", container: "runtime", reason: "Error", stage: evidence.StageObserver, failure: evidence.FailureMemoryAllocation, class: evidence.FailureClassFatalFailure},
		{name: "observer container allocation", container: "metric-observer", reason: "Error", stage: evidence.StageExecution, failure: evidence.FailureMemoryAllocation, class: evidence.FailureClassFatalFailure},
		{name: "observer OOM outranks runtime allocation", container: "runtime", reason: "Error", stage: evidence.StageExecution, failure: evidence.FailureMemoryAllocation, observerOOM: true, class: evidence.FailureClassFatalFailure},
		{name: "deadline before execution", container: "runtime", reason: "Error", stage: evidence.StageObserver},
		{name: "malformed result", container: "runtime", reason: "Error", stage: evidence.StageResultValidation, failure: evidence.FailureIntegrity},
		{name: "unstructured diagnostics", container: "runtime", reason: "Error", message: "Execution MeasurementBudgetExceeded OOMKilled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "job", UID: "job-uid"},
				Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
					Reason: "DeadlineExceeded", Message: "Job reached its frozen active deadline"}}}}
			message := test.message
			if test.stage != "" {
				payload, err := json.Marshal(evidence.TerminationMessage{SchemaVersion: evidence.TerminationSchemaVersion,
					Stage: test.stage, FailureReason: test.failure})
				if err != nil {
					t.Fatal(err)
				}
				message = string(payload)
			}
			statuses := []corev1.ContainerStatus{{Name: test.container, State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 3, Reason: test.reason, Message: message}}}}
			if test.observerOOM {
				statuses = append(statuses, corev1.ContainerStatus{Name: "metric-observer", State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}}})
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "pod", OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}},
				Status: corev1.PodStatus{ContainerStatuses: statuses}}
			finalizer, err := NewKubernetesExecutionFinalizer(fake.NewClientBuilder().WithScheme(executionJobTestScheme(t)).WithObjects(pod).Build(), unusedStoreFactory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := finalizer.RuntimeTermination(context.Background(), run, job)
			if err != nil || got == nil {
				t.Fatalf("termination=%#v err=%v", got, err)
			}
			wantContainer, wantReason := test.container, test.reason
			wantStage, wantFailure := test.stage, test.failure
			if test.observerOOM {
				wantContainer, wantReason = "metric-observer", "OOMKilled"
			}
			if wantContainer != "runtime" {
				wantStage, wantFailure = "", ""
			}
			if got.Container != wantContainer || got.Reason != wantReason || got.Stage != wantStage || got.FailureReason != wantFailure ||
				got.JobReason != "DeadlineExceeded" || got.JobMessage != job.Status.Conditions[0].Message {
				t.Fatalf("termination=%#v, want container=%s reason=%s stage=%s failure=%s", got, wantContainer, wantReason, wantStage, wantFailure)
			}
			if test.class != "" && got.Class != test.class {
				t.Fatalf("termination=%#v, want class=%s", got, test.class)
			}
		})
	}
}

func TestRuntimeFailureProjectionRetainsNativeJobWhenPodIsGone(t *testing.T) {
	run := testRun()
	for _, test := range []struct {
		name       string
		conditions []batchv1.JobCondition
		wantReason string
	}{
		{name: "early failure target", conditions: []batchv1.JobCondition{{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "native job deadline"}}, wantReason: "DeadlineExceeded"},
		{name: "failed terminal condition", conditions: []batchv1.JobCondition{{
			Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "native job deadline"}}, wantReason: "DeadlineExceeded"},
		{name: "failed follows target", conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "terminal job failure"}}, wantReason: "BackoffLimitExceeded"},
		{name: "failed precedes target", conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "terminal job failure"},
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}, wantReason: "BackoffLimitExceeded"},
		{name: "false target is not failure", conditions: []batchv1.JobCondition{{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionFalse, Reason: "DeadlineExceeded"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := &batchv1.Job{Status: batchv1.JobStatus{Conditions: test.conditions}}
			finalizer, err := NewKubernetesExecutionFinalizer(fake.NewClientBuilder().WithScheme(executionJobTestScheme(t)).Build(), unusedStoreFactory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := finalizer.RuntimeTermination(context.Background(), run, job)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantReason == "" {
				if got != nil {
					t.Fatalf("non-failing Job termination=%#v", got)
				}
				return
			}
			wantMessage := "native job deadline"
			if test.wantReason == "BackoffLimitExceeded" {
				wantMessage = "terminal job failure"
			}
			if got == nil || got.JobReason != test.wantReason || got.JobMessage != wantMessage ||
				got.Class != evidence.FailureClassUnknownFailure || got.Container != "" || got.Stage != "" {
				t.Fatalf("absent Pod termination=%#v", got)
			}
		})
	}
}

func TestNativeDisruptionOverridesWorkerIntegrityFailure(t *testing.T) {
	for _, reason := range []string{"Evicted", ""} {
		t.Run(map[string]string{"Evicted": "evicted Pod", "": "not ready Node"}[reason], func(t *testing.T) {
			run := testRun()
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "job", UID: "job-uid"}}
			payload, err := json.Marshal(evidence.TerminationMessage{SchemaVersion: evidence.TerminationSchemaVersion,
				Stage: evidence.StageResultValidation, FailureReason: evidence.FailureIntegrity})
			if err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "pod", OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}},
				Spec: corev1.PodSpec{NodeName: "node"},
				Status: corev1.PodStatus{Reason: reason, ContainerStatuses: []corev1.ContainerStatus{{Name: runtimeContainerName,
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 2, Reason: "Error", Message: string(payload)}}}}}}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"},
				Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
					Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"}}}}
			finalizer, err := NewKubernetesExecutionFinalizer(fake.NewClientBuilder().WithScheme(executionJobTestScheme(t)).WithObjects(pod, node).Build(), unusedStoreFactory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := finalizer.RuntimeTermination(context.Background(), run, job)
			wantReason := reason
			if wantReason == "" {
				wantReason = "KubeletNotReady"
			}
			if err != nil || got == nil || got.Class != evidence.FailureClassRetryableFailure ||
				got.FailureReason != wantReason || got.Stage != evidence.StageResultValidation {
				t.Fatalf("native disruption with invalid worker result = %#v, err=%v", got, err)
			}
		})
	}
}

func TestRuntimeMemoryAllocationPreservesNativeFailureGuards(t *testing.T) {
	apiFailure := errors.New("failure attribution API unavailable")
	for _, test := range []struct {
		name, podReason, disruption, class, failure string
		deleting, missingNode, notReady             bool
		apiError                                    error
	}{
		{name: "evicted", podReason: "Evicted", class: evidence.FailureClassRetryableFailure, failure: "Evicted"},
		{name: "disrupted", disruption: "DeletionByTaintManager", class: evidence.FailureClassRetryableFailure, failure: "DeletionByTaintManager"},
		{name: "deleting", deleting: true, class: evidence.FailureClassUnknownFailure, failure: "PodDeleting"},
		{name: "missing node", missingNode: true, class: evidence.FailureClassRetryableFailure, failure: "NodeLost"},
		{name: "not ready node", notReady: true, class: evidence.FailureClassRetryableFailure, failure: "KubeletNotReady"},
		{name: "node API error", apiError: apiFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "job", UID: "job-uid"},
				Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
					Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}}}
			payload, err := json.Marshal(evidence.TerminationMessage{SchemaVersion: evidence.TerminationSchemaVersion,
				Stage: evidence.StageExecution, FailureReason: evidence.FailureMemoryAllocation})
			if err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "pod", OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}},
				Spec: corev1.PodSpec{NodeName: "node"},
				Status: corev1.PodStatus{Reason: test.podReason, ContainerStatuses: []corev1.ContainerStatus{{Name: runtimeContainerName,
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 3, Reason: "Error", Message: string(payload)}}}}}}
			if test.deleting {
				now := metav1.Now()
				pod.DeletionTimestamp, pod.Finalizers = &now, []string{"test.chill.dacs.io/retain"}
			}
			if test.disruption != "" {
				pod.Status.Conditions = []corev1.PodCondition{{
					Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: test.disruption}}
			}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}
			if test.notReady {
				node.Status.Conditions = []corev1.NodeCondition{{
					Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"}}
			}
			builder := fake.NewClientBuilder().WithScheme(executionJobTestScheme(t)).WithObjects(pod)
			if !test.missingNode {
				builder = builder.WithObjects(node)
			}
			var reader client.Reader = builder.Build()
			if test.apiError != nil {
				reader = failingJobReader{Reader: reader, err: test.apiError}
			}
			finalizer, err := NewKubernetesExecutionFinalizer(reader, unusedStoreFactory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := finalizer.RuntimeTermination(context.Background(), run, job)
			if test.apiError != nil {
				if !errors.Is(err, test.apiError) || got != nil {
					t.Fatalf("termination=%#v err=%v, want API error", got, err)
				}
				return
			}
			if err != nil || got == nil || got.Class != test.class || got.FailureReason != test.failure ||
				got.Stage != evidence.StageExecution || got.JobReason != job.Status.Conditions[0].Reason {
				t.Fatalf("termination=%#v err=%v, want class=%s failure=%s", got, err, test.class, test.failure)
			}
		})
	}
}
