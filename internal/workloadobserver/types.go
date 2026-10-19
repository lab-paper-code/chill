// Package workloadobserver defines Kubernetes-free evidence for one bounded
// profiling-window workload-membership observation.
package workloadobserver

import "time"

const ResultSchemaVersion = "chill.dacs.io/workload-observation.v1alpha1"

type EventKind string

const (
	EventEntered EventKind = "Entered"
	EventUpdated EventKind = "Updated"
	EventExited  EventKind = "Exited"
)

type FailureReason string

const (
	FailureReasonWatchFailed        FailureReason = "WatchFailed"
	FailureReasonHistoryExpired     FailureReason = "HistoryExpired"
	FailureReasonEventLimitExceeded FailureReason = "EventLimitExceeded"
	FailureReasonEndFenceFailed     FailureReason = "EndFenceFailed"
)

// TargetIdentity binds observation to the Node selected in the RunConfig.
type TargetIdentity struct {
	NodeName string `json:"nodeName"`
	NodeUID  string `json:"nodeUID"`
}

// RunIdentity identifies the profiling Pod that admission excludes from the
// observed workload set.
type RunIdentity struct {
	Namespace string `json:"namespace"`
	JobUID    string `json:"jobUID"`
	PodName   string `json:"podName"`
	PodUID    string `json:"podUID"`
}

type OwnerIdentity struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// Pod records only the material facts needed for workload membership and
// CHILL measurement-workload identification.
type Pod struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	UID       string            `json:"uid"`
	Phase     string            `json:"phase"`
	Owner     *OwnerIdentity    `json:"owner,omitempty"`
	Images    []string          `json:"images"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// SamePod reports whether two records name the same Pod instance.
func SamePod(a, b Pod) bool {
	return a.Namespace == b.Namespace && a.Name == b.Name && a.UID == b.UID
}

// Event is one material change to the active Pod set. ResourceVersion remains
// an opaque Kubernetes identity and is never parsed as a number.
type Event struct {
	Kind            EventKind `json:"kind"`
	ObservedAt      time.Time `json:"observedAt"`
	ResourceVersion string    `json:"resourceVersion"`
	Pod             Pod       `json:"pod"`
}

type Failure struct {
	ObservedAt time.Time     `json:"observedAt"`
	Reason     FailureReason `json:"reason"`
	Message    string        `json:"message"`
}

// Result is one bounded, factual workload-membership observation.
type Result struct {
	SchemaVersion          string         `json:"schemaVersion"`
	Target                 TargetIdentity `json:"target"`
	Run                    RunIdentity    `json:"run"`
	CoverageStartedAt      time.Time      `json:"coverageStartedAt"`
	CoverageEndedAt        time.Time      `json:"coverageEndedAt"`
	InitialResourceVersion string         `json:"initialResourceVersion"`
	LastResourceVersion    string         `json:"lastResourceVersion"`
	EventLimit             int            `json:"eventLimit"`
	InitialActivePods      []Pod          `json:"initialActivePods"`
	Events                 []Event        `json:"events"`
	FinalActivePods        []Pod          `json:"finalActivePods"`
	Continuous             bool           `json:"continuous"`
	Failure                *Failure       `json:"failure,omitempty"`
}
