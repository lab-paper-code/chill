// Package kube projects terminal Kubernetes objects into the Kubernetes-free
// profiling evidence domain. It does not read, reconcile, or retain objects.
package kube

import (
	"errors"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Observation is one already-read terminal execution view.
type Observation struct {
	Job  *batchv1.Job
	Pod  *corev1.Pod
	Node *corev1.Node
}

// ProjectTerminalFacts validates the structural Kubernetes relationships and
// copies only facts used by the evidence domain. RunConfig-specific validation
// remains outside this adapter.
func ProjectTerminalFacts(observation Observation) (evidence.TerminalFacts, error) {
	if observation.Job == nil || observation.Pod == nil || observation.Node == nil {
		return evidence.TerminalFacts{}, errors.New("Job, Pod, and Node observations are required")
	}
	job := observation.Job
	pod := observation.Pod
	node := observation.Node
	if err := validateTerminalObjects(job, pod, node); err != nil {
		return evidence.TerminalFacts{}, err
	}

	owner := metav1.GetControllerOf(pod)
	return evidence.TerminalFacts{
		Job: evidence.JobTerminalFacts{
			Namespace:      job.Namespace,
			Name:           job.Name,
			UID:            string(job.UID),
			Succeeded:      job.Status.Succeeded,
			Failed:         job.Status.Failed,
			CompletionTime: job.Status.CompletionTime.Time,
		},
		Pod: evidence.PodTerminalFacts{
			Namespace:          pod.Namespace,
			Name:               pod.Name,
			UID:                string(pod.UID),
			ControllerOwnerUID: string(owner.UID),
			NodeName:           pod.Spec.NodeName,
			Phase:              string(pod.Status.Phase),
		},
		Node: evidence.NodeTerminalFacts{
			Name: node.Name,
			UID:  string(node.UID),
		},
	}, nil
}

func validateTerminalObjects(job *batchv1.Job, pod *corev1.Pod, node *corev1.Node) error {
	if err := validateTerminalJobPod(job, pod); err != nil {
		return err
	}
	if node.Name == "" || node.UID == "" || pod.Spec.NodeName == "" || pod.Spec.NodeName != node.Name {
		return errors.New("Pod binding and observed Node do not match")
	}
	return nil
}

func validateTerminalJobPod(job *batchv1.Job, pod *corev1.Pod) error {
	if job.Namespace == "" || job.Name == "" || job.UID == "" || job.Status.CompletionTime == nil {
		return errors.New("Job identity or completion time is incomplete")
	}
	complete := false
	failed := false
	for _, condition := range job.Status.Conditions {
		complete = complete || condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue
		failed = failed || condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue
	}
	if !complete || failed || job.Status.Succeeded != 1 || job.Status.Failed != 0 {
		return errors.New("Job is not an unambiguous terminal success")
	}
	if pod.Namespace == "" || pod.Name == "" || pod.UID == "" || pod.Namespace != job.Namespace ||
		pod.Status.Phase != corev1.PodSucceeded {
		return errors.New("Pod identity or terminal phase is invalid")
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "Job" || owner.UID != job.UID {
		return errors.New("Pod is not controller-owned by the Job")
	}
	return nil
}
