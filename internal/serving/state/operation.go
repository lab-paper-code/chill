package state

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

const (
	OperationPrepare         = "prepare"
	OperationRemove          = "remove"
	OperationPower           = "power"
	OperationIdle            = "idle"
	NativeOperationFailed    = "failed"
	NativeOperationPartial   = "partial"
	NativeServingMode        = "serving"
	OperationStop            = "stop"
	OperationDrain           = "drain"
	OperationResize          = "resize"
	OperationRoute           = "route"
	OperationUnroute         = "unroute"
	OperationRelease         = "release"
	OperationReset           = "reset"
	NativeOperationComplete  = "complete"
	NativeOperationRunning   = "running"
	NativeOperationCancel    = "cancel"
	NativeStateUnloaded      = "unloaded"
	MeasurementAccepted      = "Accepted"
	MeasurementWallScope     = "node-exclusive-wall"
	ServingInstanceComponent = "serving-instance"
)

func IsRoutingOperation(kind string) bool {
	return kind == OperationRoute || kind == OperationUnroute || kind == OperationRelease
}

func (operation Operation) ControlsWorkload(uid string) bool {
	if operation.WorkloadUID != "" {
		return operation.WorkloadUID == uid
	}
	return operation.WorkloadUIDs == nil || slices.Contains(operation.WorkloadUIDs, uid)
}

// PrepareSpec freezes one measured execution and immutable profile delivery.
// Owner, when set, makes the prepared instance serve only that Workload.
type PrepareSpec struct {
	Name        string                       `json:"name"`
	Instance    serving.SchedulerInstance    `json:"instance"`
	Bundle      evidencestore.ObjectIdentity `json:"bundle"`
	Store       storekube.Binding            `json:"store"`
	Config      native.Config                `json:"config"`
	MemoryBytes uint64                       `json:"memoryBytes"`
	Owner       *serving.WorkloadReference   `json:"owner,omitempty"`
}

type Operation struct {
	ID       string                  `json:"id"`
	Kind     string                  `json:"kind"`
	Node     serving.NodeReference   `json:"node"`
	Nodes    []serving.NodeReference `json:"nodes,omitempty"`
	Instance string                  `json:"instance,omitempty"`
	PodUID   string                  `json:"podUID,omitempty"`
	Prepare  *PrepareSpec            `json:"prepare,omitempty"`
	// PowerMode is the DeviceClass mode a power operation requests, within
	// MaxActuationDuration of its label write (the mode's actuation policy).
	PowerMode            string        `json:"powerMode,omitempty"`
	MaxActuationDuration time.Duration `json:"maxActuationDurationNanos,omitempty"`
	// Holder is the measurement root a measurement power operation acts for;
	// the Node's measurement taint must name it.
	Holder              string                        `json:"holder,omitempty"`
	Target              *serving.SchedulerCandidate   `json:"target,omitempty"`
	SourceConfigRV      string                        `json:"sourceConfigRV,omitempty"`
	StaticRouting       map[string]map[string]int     `json:"staticRouting,omitempty"`
	WorkloadUID         string                        `json:"workloadUID,omitempty"`
	WorkloadUIDs        []string                      `json:"workloadUIDs"`
	Reason              string                        `json:"reason,omitempty"`
	ActorUsername       string                        `json:"actorUsername,omitempty"`
	ActorUID            string                        `json:"actorUID,omitempty"`
	BeforeStaticRouting map[string]map[string]int     `json:"beforeStaticRouting,omitempty"`
	ExpectedSeconds     *float64                      `json:"expectedSeconds"`
	ExpectedSource      *serving.ActionExpectedSource `json:"expectedSource"`
	// SettingsValidatedAt names the settings change that declared StaticRouting.
	SettingsValidatedAt *time.Time `json:"settingsValidatedAt,omitempty"`
}

type OperationState struct {
	At        time.Time                   `json:"at"`
	Node      serving.NodeReference       `json:"node"`
	PowerMode string                      `json:"powerMode"`
	Instances []serving.InstanceState     `json:"instances"`
	Native    map[string]native.State     `json:"native"`
	Pods      []workloadobserver.Pod      `json:"pods"`
	States    []string                    `json:"states"`
	Reasons   []string                    `json:"reasons,omitempty"`
	Routing   map[string]map[string]int32 `json:"routing,omitempty"`
}

type OperationStep struct {
	Name      string         `json:"name"`
	StartedAt time.Time      `json:"startedAt"`
	EndedAt   time.Time      `json:"endedAt"`
	Status    string         `json:"status"`
	Before    OperationState `json:"before"`
	Actual    OperationState `json:"actual"`
	Error     string         `json:"error,omitempty"`
}

// PreparationEvidence survives a canceled final Capture. Spec remains the
// authority for slot, owner and policy; these are the API-confirmed identities.
type PreparationEvidence struct {
	PodUID        string                                 `json:"podUID"`
	NodeUID       string                                 `json:"nodeUID"`
	DecisionID    string                                 `json:"decisionID"`
	Created       bool                                   `json:"created"`
	RuntimeImage  string                                 `json:"runtimeImage,omitempty"`
	RunnerCommand []string                               `json:"runnerCommand,omitempty"`
	Failure       *serving.PreparationFailureObservation `json:"failure,omitempty"`
}

type OperationResult struct {
	Operation   Operation               `json:"operation"`
	Before      OperationState          `json:"before"`
	Actual      OperationState          `json:"actual"`
	StartedAt   time.Time               `json:"startedAt"`
	EndedAt     time.Time               `json:"endedAt"`
	Status      string                  `json:"status"`
	Error       string                  `json:"error,omitempty"`
	Progress    []string                `json:"progress"`
	DrainNative map[string]native.State `json:"drainNative,omitempty"`
	Steps       []OperationStep         `json:"steps,omitempty"`
	Preparation *PreparationEvidence    `json:"preparation,omitempty"`
	// Only a confirmed pre-effect failure permits immediate reservation return.
	EffectsNotStarted bool `json:"-"`
}

// PreparationFailure retains a failed start independently of action history.
// Cancellation is not evidence that the selected physical inputs cannot start.
func (r OperationResult) PreparationFailure(snapshot serving.SchedulerSnapshot) *serving.PreparationFailure {
	if r.Operation.Prepare == nil || r.Preparation == nil || r.Preparation.PodUID == "" ||
		(r.Status != NativeOperationFailed && r.Status != NativeOperationPartial && r.Status != NativeOperationComplete) {
		return nil
	}
	observation := r.Preparation.Failure
	terminalFailure := false
	for _, actual := range snapshot.Instances {
		if actual.PodUID == r.Preparation.PodUID && actual.Terminated && actual.StartupFailure != nil {
			terminalFailure = true
			if observation == nil || observation.Classification == serving.PreparationUnknownTransient || r.Status == NativeOperationComplete {
				observation = actual.StartupFailure
			}
		}
	}
	if r.Status == NativeOperationComplete && (!terminalFailure || observation.Classification == serving.PreparationTransient) {
		return nil
	}
	if observation == nil {
		if strings.Contains(r.Error, "context canceled") {
			return nil
		}
		failure, failed := ClassifyPreparationFailure(nil, nil, nil, errors.New(r.Error), true)
		if !failed {
			return nil
		}
		observation = &failure
	}
	observed := *observation
	wanted := r.Operation.Prepare.Instance
	wanted.Allocation.MemoryBytes = r.Operation.Prepare.MemoryBytes
	wanted.RuntimeImage = r.Preparation.RuntimeImage
	wanted.RunnerCommand = r.Preparation.RunnerCommand
	return serving.NewPreparationFailure(wanted, r.Preparation.PodUID, observed)
}
