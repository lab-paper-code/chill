package evidence

import (
	"errors"
	"strings"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func (f TerminalFacts) validate(config profilingrun.RunConfig, rawPodUID string) error {
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
		f.Node.Name != config.TargetNode.Name || f.Node.UID != config.TargetNode.UID {
		return errors.New("terminal Node identity mismatch")
	}
	return nil
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return true
		}
	}
	return false
}
