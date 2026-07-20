package workloadobserver

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Validate checks internal observation consistency without applying admission
// policy or interpreting Pod activity.
func (r Result) Validate() error {
	if r.SchemaVersion != ResultSchemaVersion {
		return fmt.Errorf("unsupported workload observation schemaVersion %q", r.SchemaVersion)
	}
	if err := validateIdentities(r.Target, r.Run); err != nil {
		return err
	}
	if !digestPattern.MatchString(r.PolicyDigest) {
		return errors.New("workload observation policy digest is invalid")
	}
	if r.CoverageStartedAt.IsZero() || r.CoverageEndedAt.IsZero() ||
		!r.CoverageEndedAt.After(r.CoverageStartedAt) {
		return errors.New("workload observation coverage window is invalid")
	}
	if blank(r.InitialResourceVersion, r.LastResourceVersion) {
		return errors.New("workload observation resourceVersion boundary is incomplete")
	}
	if r.EventLimit < 1 || len(r.Events) > r.EventLimit {
		return errors.New("workload observation event limit is invalid or exceeded")
	}
	if r.Continuous == (r.Failure != nil) {
		return errors.New("workload observation continuity and failure disagree")
	}
	if err := validateFailure(r.Failure, r.CoverageStartedAt, r.CoverageEndedAt); err != nil {
		return err
	}

	active, err := validateActiveSet(r.InitialActivePods, r.Run.PodUID, "initial")
	if err != nil {
		return err
	}
	if err := applyEvents(active, r.Events, r.CoverageStartedAt, r.CoverageEndedAt); err != nil {
		return err
	}
	if _, err := validateActiveSet(r.FinalActivePods, r.Run.PodUID, "final"); err != nil {
		return err
	}
	derived := activePods(active)
	if !reflect.DeepEqual(derived, r.FinalActivePods) {
		return errors.New("workload observation final active set does not match retained events")
	}
	return nil
}

func validateIdentities(target TargetIdentity, run RunIdentity) error {
	for name, value := range map[string]string{
		"target Node name": target.NodeName,
		"target Node UID":  target.NodeUID,
		"Run namespace":    run.Namespace,
		"Run Job UID":      run.JobUID,
		"Run Pod name":     run.PodName,
		"Run Pod UID":      run.PodUID,
	} {
		if blank(value) {
			return fmt.Errorf("workload observation %s is required without surrounding whitespace", name)
		}
	}
	return nil
}

func validateFailure(failure *Failure, started, ended time.Time) error {
	if failure == nil {
		return nil
	}
	if !within(failure.ObservedAt, started, ended) || blank(failure.Message) {
		return errors.New("workload observation failure is invalid")
	}
	switch failure.Reason {
	case FailureReasonWatchFailed, FailureReasonHistoryExpired, FailureReasonEventLimitExceeded,
		FailureReasonEndFenceFailed:
		return nil
	default:
		return fmt.Errorf("unsupported workload observation failure reason %q", failure.Reason)
	}
}

func validateActiveSet(pods []Pod, runPodUID, boundary string) (map[string]Pod, error) {
	active := make(map[string]Pod, len(pods))
	previousKey := ""
	runPresent := false
	for index := range pods {
		pod := pods[index]
		if err := validatePod(pod, true); err != nil {
			return nil, fmt.Errorf("validate %s active Pod: %w", boundary, err)
		}
		key := podKey(pod)
		if previousKey != "" && key <= previousKey {
			return nil, fmt.Errorf("%s active Pods are duplicated or not canonically ordered", boundary)
		}
		if _, duplicate := active[pod.UID]; duplicate {
			return nil, fmt.Errorf("%s active Pods contain duplicate UID %q", boundary, pod.UID)
		}
		active[pod.UID] = pod
		previousKey = key
		runPresent = runPresent || pod.UID == runPodUID
	}
	if !runPresent {
		return nil, fmt.Errorf("%s active Pods do not contain the Run Pod", boundary)
	}
	return active, nil
}

func applyEvents(active map[string]Pod, events []Event, started, ended time.Time) error {
	var previous time.Time
	for index := range events {
		event := events[index]
		if !within(event.ObservedAt, started, ended) ||
			(!previous.IsZero() && event.ObservedAt.Before(previous)) || blank(event.ResourceVersion) {
			return errors.New("workload observation events are outside coverage or out of order")
		}
		current, exists := active[event.Pod.UID]
		switch event.Kind {
		case EventEntered:
			if exists {
				return fmt.Errorf("workload Pod %q entered while already active", event.Pod.UID)
			}
			if err := validatePod(event.Pod, true); err != nil {
				return fmt.Errorf("validate entered workload Pod: %w", err)
			}
			active[event.Pod.UID] = event.Pod
		case EventUpdated:
			if !exists || !samePodIdentity(current, event.Pod) {
				return fmt.Errorf("workload Pod %q updated without matching active identity", event.Pod.UID)
			}
			if err := validatePod(event.Pod, true); err != nil {
				return fmt.Errorf("validate updated workload Pod: %w", err)
			}
			active[event.Pod.UID] = event.Pod
		case EventExited:
			if !exists || !samePodIdentity(current, event.Pod) {
				return fmt.Errorf("workload Pod %q exited without matching active identity", event.Pod.UID)
			}
			if err := validatePod(event.Pod, false); err != nil {
				return fmt.Errorf("validate exited workload Pod: %w", err)
			}
			delete(active, event.Pod.UID)
		default:
			return fmt.Errorf("unsupported workload observation event kind %q", event.Kind)
		}
		previous = event.ObservedAt
	}
	return nil
}

func validatePod(pod Pod, requireActive bool) error {
	if blank(pod.Namespace, pod.Name, pod.UID, pod.Phase) {
		return errors.New("workload Pod identity or phase is incomplete")
	}
	if requireActive && pod.Phase != "Pending" && pod.Phase != "Running" && pod.Phase != "Unknown" {
		return fmt.Errorf("workload Pod phase %q is not active", pod.Phase)
	}
	if pod.Owner != nil && blank(pod.Owner.Kind, pod.Owner.Name, pod.Owner.UID) {
		return errors.New("workload Pod owner identity is incomplete")
	}
	if len(pod.Images) == 0 {
		return errors.New("workload Pod images are empty")
	}
	previous := ""
	for _, image := range pod.Images {
		if blank(image) || (previous != "" && image <= previous) {
			return errors.New("workload Pod images are invalid, duplicated, or not canonically ordered")
		}
		previous = image
	}
	for key, value := range pod.Labels {
		if blank(key) || strings.TrimSpace(value) != value {
			return errors.New("workload Pod selector labels are invalid")
		}
	}
	if pod.Labels != nil && len(pod.Labels) == 0 {
		return errors.New("workload Pod selector labels must use nil for an empty set")
	}
	return nil
}

func activePods(active map[string]Pod) []Pod {
	pods := make([]Pod, 0, len(active))
	for _, pod := range active {
		pods = append(pods, pod)
	}
	sort.Slice(pods, func(i, j int) bool { return podKey(pods[i]) < podKey(pods[j]) })
	return pods
}

func samePodIdentity(left, right Pod) bool {
	return left.Namespace == right.Namespace && left.Name == right.Name && left.UID == right.UID
}

func podKey(pod Pod) string {
	return pod.Namespace + "\x00" + pod.Name + "\x00" + pod.UID
}

func within(value, started, ended time.Time) bool {
	return !value.IsZero() && !value.Before(started) && !value.After(ended)
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return true
		}
	}
	return false
}
