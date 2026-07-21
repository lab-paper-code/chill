package evidence

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func (f TerminalFacts) validate(intent profilingrun.Intent, rawPodUID string) error {
	if blank(f.Job.Namespace, f.Job.Name, f.Job.UID) || f.Job.Succeeded != 1 || f.Job.Failed != 0 ||
		f.Job.CompletionTime.IsZero() {
		return errors.New("Job terminal facts are incomplete")
	}
	if blank(f.Pod.Namespace, f.Pod.Name, f.Pod.UID, f.Pod.ControllerOwnerUID, f.Pod.NodeName) ||
		f.Pod.Namespace != f.Job.Namespace || f.Pod.ControllerOwnerUID != f.Job.UID ||
		f.Pod.UID != rawPodUID || f.Pod.Phase != "Succeeded" {
		return errors.New("Pod terminal facts or Job ownership are invalid")
	}
	if blank(f.Node.Name, f.Node.UID) || f.Node.Name != f.Pod.NodeName ||
		f.Node.Name != intent.TargetNode.Name || f.Node.UID != intent.TargetNode.UID {
		return errors.New("terminal Node identity mismatch")
	}
	if err := validateContainers(intent, f.Containers); err != nil {
		return err
	}
	if f.NodeWorkload.ObservedAt.IsZero() || f.NodeWorkload.ObservedAt.Before(f.Job.CompletionTime) {
		return errors.New("Node workload observation time is invalid")
	}
	seen := map[string]struct{}{}
	containsRunPod := false
	for _, pod := range f.NodeWorkload.Pods {
		if blank(pod.Namespace, pod.Name, pod.UID, pod.Phase) {
			return errors.New("Node workload Pod identity is incomplete")
		}
		if _, duplicate := seen[pod.UID]; duplicate {
			return errors.New("Node workload contains duplicate Pod UID")
		}
		seen[pod.UID] = struct{}{}
		containsRunPod = containsRunPod || pod.UID == f.Pod.UID
	}
	if !containsRunPod {
		return errors.New("Node workload does not contain the Run Pod")
	}
	return nil
}

func validateContainers(intent profilingrun.Intent, containers []ContainerTerminal) error {
	expected := map[string]string{
		"artifact":        intent.Images.Artifact,
		"runtime":         intent.Images.Runtime,
		"metric-observer": intent.Images.MetricObserver,
	}
	if intent.Images.WorkloadObserver != "" {
		expected["workload-observer"] = intent.Images.WorkloadObserver
	}
	if len(containers) != len(expected) {
		return fmt.Errorf("expected %d terminal containers, found %d", len(expected), len(containers))
	}
	seen := map[string]struct{}{}
	for _, container := range containers {
		want, ok := expected[container.Name]
		if !ok {
			return fmt.Errorf("unexpected terminal container %q", container.Name)
		}
		if _, duplicate := seen[container.Name]; duplicate {
			return fmt.Errorf("duplicate terminal container %q", container.Name)
		}
		seen[container.Name] = struct{}{}
		if container.RequestedImage != want || strings.TrimSpace(container.ImageID) == "" ||
			container.RestartCount != 0 || container.ExitCode != 0 || container.Reason != "Completed" ||
			container.StartedAt.IsZero() || container.FinishedAt.Before(container.StartedAt) {
			return fmt.Errorf("terminal container %q is invalid", container.Name)
		}
		if container.Name != "artifact" && !strings.Contains(container.ImageID, imageDigest(want)) {
			return fmt.Errorf("terminal container %q imageID mismatch", container.Name)
		}
	}
	return nil
}

func imageDigest(image string) string {
	position := strings.LastIndex(image, "@sha256:")
	if position < 0 {
		return ""
	}
	return image[position+1:]
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return true
		}
	}
	return false
}
