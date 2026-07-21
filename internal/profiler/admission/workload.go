package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

type workloadWindow struct {
	Known                       bool
	ConcurrentCHILLWorkloads    []string
	ExternalWorkloadChanges     []string
	ExternalEnvironmentIdentity string
}

func projectWorkloadWindow(
	intent profilingrun.Intent,
	result *workloadobserver.Result,
) (workloadWindow, error) {
	if result == nil {
		return workloadWindow{}, nil
	}
	if err := result.Validate(); err != nil {
		return workloadWindow{}, fmt.Errorf("validate workload observation: %w", err)
	}
	digest, err := intent.Workload.Digest()
	if err != nil {
		return workloadWindow{}, fmt.Errorf("digest workload observation contract: %w", err)
	}
	if result.PolicyDigest != digest || result.Target.NodeName != intent.TargetNode.Name ||
		result.Target.NodeUID != intent.TargetNode.UID {
		return workloadWindow{}, errors.New("workload observation contract or target identity mismatch")
	}

	concurrent := map[string]string{}
	externalChanges := map[string]string{}
	externalAtStart := make([]workloadobserver.Pod, 0, len(result.InitialActivePods))
	classify := func(pod workloadobserver.Pod) bool {
		if pod.UID == result.Run.PodUID {
			return false
		}
		ownerKind := ""
		if pod.Owner != nil {
			ownerKind = pod.Owner.Kind
		}
		if profilingrun.IsMeasurementWorkload(ownerKind, pod.Labels) {
			concurrent[pod.UID] = workloadIdentity(pod)
			return false
		}
		return true
	}
	for _, pod := range result.InitialActivePods {
		if classify(pod) {
			externalAtStart = append(externalAtStart, pod)
		}
	}
	for _, event := range result.Events {
		if classify(event.Pod) {
			externalChanges[event.Pod.UID] = string(event.Kind) + ":" + workloadIdentity(event.Pod)
		}
	}
	for _, pod := range result.FinalActivePods {
		classify(pod)
	}

	environmentIdentity, err := workloadEnvironmentIdentity(externalAtStart)
	if err != nil {
		return workloadWindow{}, err
	}
	return workloadWindow{
		Known:                       result.Continuous,
		ConcurrentCHILLWorkloads:    sortedValues(concurrent),
		ExternalWorkloadChanges:     sortedValues(externalChanges),
		ExternalEnvironmentIdentity: environmentIdentity,
	}, nil
}

func workloadIdentity(pod workloadobserver.Pod) string {
	return pod.Namespace + "/" + pod.Name + "@" + pod.UID
}

func sortedValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func workloadEnvironmentIdentity(pods []workloadobserver.Pod) (string, error) {
	canonical := append([]workloadobserver.Pod(nil), pods...)
	sort.Slice(canonical, func(i, j int) bool {
		return workloadIdentity(canonical[i]) < workloadIdentity(canonical[j])
	})
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode external workload environment: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
