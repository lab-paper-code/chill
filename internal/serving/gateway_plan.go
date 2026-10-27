package serving

import (
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

// GatewayPlan is the immutable control-to-router distribution snapshot. Identity
// is independent of Workload definitions and native DecisionID policy ACKs.
type GatewayPlan struct {
	PlanVersion       string                      `json:"planVersion"`
	CreatedAt         time.Time                   `json:"createdAt"`
	Participants      int                         `json:"participants"`
	Definitions       []*Definition               `json:"definitions"`
	ObservationWindow time.Duration               `json:"observationWindowNanos"`
	Admission         map[string]bool             `json:"admission"`
	Targets           []GatewayTarget             `json:"targets"`
	Routing           map[string]map[string]int32 `json:"routing"`
}

type GatewayTarget struct {
	InstanceID       string                  `json:"instanceID"`
	PodUID           string                  `json:"podUID"`
	ContainerID      string                  `json:"containerID"`
	Node             NodeReference           `json:"node"`
	Endpoint         string                  `json:"endpoint"`
	ModelName        string                  `json:"modelName"`
	Profile          *Profile                `json:"profile"`
	ExpectedRuntime  *native.RuntimeMetadata `json:"expectedRuntime"`
	DecisionID       string                  `json:"decisionID"`
	OwnerWorkloadUID string                  `json:"ownerWorkloadUID"`
	Capacity         int                     `json:"capacity"`
	RouterLimit      int                     `json:"routerLimit"`
}

// GatewayReport includes all reservations, including assigned-but-not-sent
// calls and old plan versions. A missing report is never a zero report.
type GatewayReport struct {
	RouterID    string               `json:"routerID"`
	PlanVersion string               `json:"planVersion"`
	AppliedAt   time.Time            `json:"appliedAt"`
	ObservedAt  time.Time            `json:"observedAt"`
	Outstanding []GatewayOutstanding `json:"outstanding"`
	Requests    []SchedulerRequest   `json:"requests"`
	Open        []RequestRow         `json:"open"`
	Arrivals    []GatewayArrivals    `json:"arrivals"`
}

type GatewayOutstanding struct {
	InstanceID  string   `json:"instanceID"`
	PodUID      string   `json:"podUID"`
	ContainerID string   `json:"containerID"`
	Count       int      `json:"count"`
	RequestIDs  []string `json:"requestIDs"`
}

type GatewayArrivals struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	WorkloadUID string    `json:"workloadUID"`
	Received    uint64    `json:"received"`
	Validated   uint64    `json:"validated"`
}
