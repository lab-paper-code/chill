package kube

import (
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// ValidateLoadPod checks the live load execution before measurement evidence is
// published. Evidence v1alpha17 retains only the measurement Pod's terminal facts;
// this check does not append load containers to that Pod or invent stored facts.
func ValidateLoadPod(config profilingrun.RunConfig, job *batchv1.Job, pod *corev1.Pod) error {
	if job == nil || pod == nil {
		return errors.New("load Job and Pod are required")
	}
	if err := validateTerminalJobPod(job, pod); err != nil {
		return err
	}
	if config.LoadOriginNode == "" || config.LoadOriginNode == config.TargetNode.Name ||
		job.Spec.Template.Spec.NodeName != config.LoadOriginNode || pod.Spec.NodeName != config.LoadOriginNode {
		return errors.New("load Pod binding differs from frozen off-node origin")
	}
	template := &job.Spec.Template.Spec
	if len(template.InitContainers) != 1 || template.InitContainers[0].Name != "dataset" ||
		len(template.Containers) != 1 || template.Containers[0].Name != "load" ||
		config.Measurement.Dataset == nil || template.InitContainers[0].Image != config.Measurement.Dataset.Image {
		return errors.New("load Job must declare frozen dataset init and load containers")
	}
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "dataset" ||
		len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != "load" {
		return errors.New("load Pod must declare dataset init and load containers")
	}
	containers, err := projectContainers(pod)
	if err != nil {
		return err
	}
	if err := evidence.ValidateLoadContainers(config, template.Containers[0].Image, containers); err != nil {
		return fmt.Errorf("validate load terminal containers: %w", err)
	}
	return nil
}
