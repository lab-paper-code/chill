package projection

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

type workloadWindow struct {
	Known                    bool
	ConcurrentCHILLWorkloads []string
	ExternalWorkloadChanges  []string
	EnvironmentIdentity      string
}

func projectWorkloadWindow(
	result *workloadobserver.Result,
	runtimeStarted time.Time,
	runtimeEnded time.Time,
) (workloadWindow, error) {
	if result == nil {
		identity, err := UnknownEnvironmentIdentity()
		if err != nil {
			return workloadWindow{}, fmt.Errorf("derive unknown workload environment identity: %w", err)
		}
		return workloadWindow{EnvironmentIdentity: identity}, nil
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
		Known:                    result.Continuous && !endDrainAmbiguous,
		ConcurrentCHILLWorkloads: sortedValues(concurrent),
		ExternalWorkloadChanges:  sortedValues(externalChanges),
		EnvironmentIdentity:      environmentIdentity,
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
	type podIdentity struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	}
	type member struct {
		Namespace string                          `json:"namespace"`
		Owner     *workloadobserver.OwnerIdentity `json:"owner,omitempty"`
		Pod       *podIdentity                    `json:"pod,omitempty"`
		Phase     string                          `json:"phase"`
		Images    []string                        `json:"images"`
		Labels    map[string]string               `json:"labels,omitempty"`
	}
	encoded := make([]string, 0, len(pods))
	for _, pod := range pods {
		entry := member{
			Namespace: pod.Namespace, Owner: pod.Owner, Phase: pod.Phase,
			Images: pod.Images, Labels: pod.Labels,
		}
		if pod.Owner == nil {
			entry.Pod = &podIdentity{Name: pod.Name, UID: pod.UID}
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			return "", fmt.Errorf("encode external workload member: %w", err)
		}
		encoded = append(encoded, string(payload))
	}
	sort.Strings(encoded)
	members := make([]json.RawMessage, len(encoded))
	for index, payload := range encoded {
		members[index] = json.RawMessage(payload)
	}
	return digestIdentity(
		environmentIdentitySchemaVersion,
		struct {
			Known   bool              `json:"known"`
			Members []json.RawMessage `json:"members"`
		}{Known: true, Members: members},
	)
}
