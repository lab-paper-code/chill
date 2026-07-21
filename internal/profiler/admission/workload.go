package admission

import (
	"errors"
	"fmt"
	"sort"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func projectWorkloadWindow(
	intent profilingrun.Intent,
	result *workloadobserver.Result,
) (bool, []string, error) {
	if result == nil {
		return false, nil, nil
	}
	if err := result.Validate(); err != nil {
		return false, nil, fmt.Errorf("validate workload observation: %w", err)
	}
	policy := intent.Workload.AllowedBackground
	if err := policy.Validate(); err != nil {
		return false, nil, fmt.Errorf("validate allowed-background policy: %w", err)
	}
	digest, err := policy.Digest()
	if err != nil {
		return false, nil, fmt.Errorf("digest allowed-background policy: %w", err)
	}
	if result.PolicyDigest != digest || result.Target.NodeName != intent.TargetNode.Name ||
		result.Target.NodeUID != intent.TargetNode.UID {
		return false, nil, errors.New("workload observation policy or target identity mismatch")
	}

	unexpected := map[string]string{}
	classify := func(pod workloadobserver.Pod) {
		if pod.UID == result.Run.PodUID || allowedBackgroundPod(policy, pod) {
			return
		}
		unexpected[pod.UID] = pod.Namespace + "/" + pod.Name + "@" + pod.UID
	}
	for _, pod := range result.InitialActivePods {
		classify(pod)
	}
	for _, event := range result.Events {
		classify(event.Pod)
	}
	for _, pod := range result.FinalActivePods {
		classify(pod)
	}

	identities := make([]string, 0, len(unexpected))
	for _, identity := range unexpected {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	return result.Continuous, identities, nil
}

func allowedBackgroundPod(policy profilingrun.AllowedBackgroundPolicy, pod workloadobserver.Pod) bool {
	if pod.Owner == nil {
		return false
	}
	return policy.Matches(pod.Namespace, pod.Owner.Kind, pod.Owner.Name, pod.Labels)
}
