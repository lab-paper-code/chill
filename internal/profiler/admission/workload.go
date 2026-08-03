package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

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
	runtimeStarted time.Time,
	runtimeEnded time.Time,
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
	if runtimeStarted.IsZero() || !runtimeEnded.After(runtimeStarted) ||
		result.CoverageStartedAt.After(runtimeStarted) || result.CoverageEndedAt.Before(runtimeEnded) {
		return workloadWindow{}, errors.New("runtime measurement window is outside workload observation coverage")
	}

	concurrent := map[string]string{}
	externalChanges := map[string]string{}
	active := make(map[string]workloadobserver.Pod, len(result.InitialActivePods))
	for _, pod := range result.InitialActivePods {
		active[pod.UID] = pod
	}
	for _, event := range result.Events {
		if !event.ObservedAt.Before(runtimeStarted) {
			break
		}
		applyWorkloadEvent(active, event)
	}

	externalAtStart := make([]workloadobserver.Pod, 0, len(active))
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
	for _, pod := range active {
		if classify(pod) {
			externalAtStart = append(externalAtStart, pod)
		}
	}
	endDrainAmbiguous := false
	for _, event := range result.Events {
		if event.ObservedAt.Before(runtimeStarted) {
			continue
		}
		if event.ObservedAt.After(runtimeEnded) {
			// The observer timestamps receipt, not the source-side Pod transition.
			// Events drained while establishing the causal end fence therefore
			// cannot be attributed safely to either side of the runtime boundary.
			endDrainAmbiguous = true
			break
		}
		if classify(event.Pod) {
			externalChanges[event.Pod.UID] = string(event.Kind) + ":" + workloadIdentity(event.Pod)
		}
	}

	environmentIdentity, err := workloadEnvironmentIdentity(externalAtStart)
	if err != nil {
		return workloadWindow{}, err
	}
	return workloadWindow{
		Known:                       result.Continuous && !endDrainAmbiguous,
		ConcurrentCHILLWorkloads:    sortedValues(concurrent),
		ExternalWorkloadChanges:     sortedValues(externalChanges),
		ExternalEnvironmentIdentity: environmentIdentity,
	}, nil
}

func applyWorkloadEvent(active map[string]workloadobserver.Pod, event workloadobserver.Event) {
	if event.Kind == workloadobserver.EventExited {
		delete(active, event.Pod.UID)
		return
	}
	active[event.Pod.UID] = event.Pod
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
