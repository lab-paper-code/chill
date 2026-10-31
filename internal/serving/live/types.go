// Package live owns the bounded, in-memory chill-live/1 feed. It has no
// authorization or dashboard publish endpoint; trusted server producers publish
// DTOs and internal consumers may subscribe to the same ordered channel.
package live

import "encoding/json"

const (
	Protocol                = "chill-live/1"
	MaxSeq           uint64 = 1<<53 - 1
	MaxFrameBytes           = 1 << 20
	RetainedMessages        = 600
	RetainedBytes           = 8 << 20
	QueueMessages           = 602
	QueueBytes              = 2 << 20
)

type SLO struct {
	Percentile         float64 `json:"percentile"`
	TargetLatencyNanos int64   `json:"targetLatencyNanos"`
}

type Readiness struct {
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason"`
	ObservedAt string `json:"observedAt"`
}

type Workload struct {
	UID          string    `json:"uid"`
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	DefinitionID *string   `json:"definitionID"`
	SLO          SLO       `json:"slo"`
	Autonomous   bool      `json:"autonomous"`
	Admission    bool      `json:"admission"`
	Pending      int       `json:"pending"`
	Readiness    Readiness `json:"readiness"`
}

type NodeRef struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}

type Node struct {
	UID         string   `json:"uid"`
	Name        string   `json:"name"`
	PowerMode   string   `json:"powerMode"`
	Ready       bool     `json:"ready"`
	Measurement bool     `json:"measurement"`
	Reasons     []string `json:"reasons,omitempty"`
	ObservedAt  *string  `json:"observedAt"`
}

type Instance struct {
	ID               string          `json:"id"`
	Node             NodeRef         `json:"node"`
	OwnerWorkloadUID string          `json:"ownerWorkloadUID"`
	ProfileID        string          `json:"profileID"`
	Ready            bool            `json:"ready"`
	Stopped          bool            `json:"stopped"`
	Routable         map[string]bool `json:"routable"`
	Running          map[string]int  `json:"running"`
	Waiting          map[string]int  `json:"waiting"`
	ObservedAt       string          `json:"observedAt"`
}

type Reservation struct {
	Owner        string    `json:"owner"`
	Nodes        []NodeRef `json:"nodes"`
	WorkloadUIDs []string  `json:"workloadUIDs"`
	PodUIDs      []string  `json:"podUIDs"`
	Instances    []string  `json:"instances"`
	Reason       string    `json:"reason"`
	Explanation  string    `json:"explanation"`
}

type Decision struct {
	At          string `json:"at"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
	Explanation string `json:"explanation"`
}

type ExpectedSource struct {
	CostID     string          `json:"costID"`
	Reference  json.RawMessage `json:"reference"`
	Statistic  string          `json:"statistic"`
	TrialCount int             `json:"trialCount"`
}

type Action struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Node            NodeRef         `json:"node"`
	WorkloadUID     string          `json:"workloadUID"`
	Status          string          `json:"status"`
	StartedAt       string          `json:"startedAt"`
	CompletedAt     *string         `json:"completedAt,omitempty"`
	ExpectedSeconds *float64        `json:"expectedSeconds"`
	ExpectedSource  *ExpectedSource `json:"expectedSource"`
}

func (a Action) Key() string {
	key, _ := json.Marshal([3]string{a.ID, a.Node.UID, a.StartedAt})
	return string(key)
}

type ActionResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type ActionEvent struct {
	Action
	Phase  string        `json:"phase"`
	Result *ActionResult `json:"result,omitempty"`
}

type Routing map[string]map[string]float64

type Snapshot struct {
	Workloads    []Workload    `json:"workloads"`
	Nodes        []Node        `json:"nodes"`
	Instances    []Instance    `json:"instances"`
	Routing      Routing       `json:"routing"`
	Decision     *Decision     `json:"decision"`
	Actions      []Action      `json:"actions"`
	Reservations []Reservation `json:"reservations"`
}

type Removed struct {
	Instances []string `json:"instances,omitempty"`
	Actions   []string `json:"actions,omitempty"`
}

// Pointers distinguish omitted collections from an explicitly empty collection.
// Decision points to a nullable value: &nil clears the current decision.
type Delta struct {
	Workloads    *[]Workload    `json:"workloads,omitempty"`
	Nodes        *[]Node        `json:"nodes,omitempty"`
	Instances    *[]Instance    `json:"instances,omitempty"`
	Routing      *Routing       `json:"routing,omitempty"`
	Decision     **Decision     `json:"decision,omitempty"`
	Actions      *[]Action      `json:"actions,omitempty"`
	Reservations *[]Reservation `json:"reservations,omitempty"`
	Removed      *Removed       `json:"removed,omitempty"`
}

type Event struct {
	Kind     string        `json:"kind"`
	Decision *Decision     `json:"decision,omitempty"`
	Action   *ActionEvent  `json:"action,omitempty"`
	Cluster  *ClusterEvent `json:"cluster,omitempty"`
}

// ClusterEvent is the live event-lane projection of one committed cluster row.
type ClusterEvent struct {
	Kind           string      `json:"kind"`
	At             string      `json:"at"`
	Event          string      `json:"event"`
	Pod            *ClusterPod `json:"pod,omitempty"`
	Source         string      `json:"source,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	PreviousReason string      `json:"previousReason,omitempty"`
}

type ClusterPod struct {
	Namespace string  `json:"namespace,omitempty"`
	Name      string  `json:"name,omitempty"`
	UID       string  `json:"uid"`
	Node      string  `json:"node,omitempty"`
	Phase     string  `json:"phase,omitempty"`
	Ready     *string `json:"ready,omitempty"`
}

type Metric struct {
	Received       uint64   `json:"received"`
	Completed      uint64   `json:"completed"`
	Failed         uint64   `json:"failed"`
	WithinSLO      uint64   `json:"withinSLO"`
	LatencySamples uint64   `json:"latencySamples"`
	P50MS          *float64 `json:"p50ms"`
	P90MS          *float64 `json:"p90ms"`
	P95MS          *float64 `json:"p95ms"`
	P99MS          *float64 `json:"p99ms"`
}

type NodeMetric struct {
	Watts      *float64 `json:"watts"`
	ObservedAt *string  `json:"observedAt"`
}

type PoolMetric struct {
	WholeWatts  *float64 `json:"wholeWatts"`
	ActiveWatts *float64 `json:"activeWatts"`
}

type Gap struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

type MetricsFrame struct {
	BucketAt  string                `json:"bucketAt"`
	Workloads map[string]Metric     `json:"workloads"`
	Nodes     map[string]NodeMetric `json:"nodes"`
	Pool      PoolMetric            `json:"pool"`
	Gaps      []Gap                 `json:"gaps,omitempty"`
}
