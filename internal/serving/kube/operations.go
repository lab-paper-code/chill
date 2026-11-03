package kube

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	workloadkube "github.com/lab-paper-code/chill/internal/workloadobserver/kube"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	operationPrepare         = "prepare"
	operationRemove          = "remove"
	operationPower           = "power"
	operationIdle            = "idle"
	nativeOperationFailed    = "failed"
	nativeOperationPartial   = "partial"
	nativeServingMode        = "serving"
	operationStop            = "stop"
	operationDrain           = "drain"
	operationResize          = "resize"
	operationRoute           = "route"
	operationUnroute         = "unroute"
	operationRelease         = "release"
	operationReset           = "reset"
	nativeOperationComplete  = "complete"
	nativeOperationRunning   = "running"
	nativeOperationCancel    = "cancel"
	nativeStateUnloaded      = "unloaded"
	measurementAccepted      = "Accepted"
	measurementWallScope     = "node-exclusive-wall"
	servingInstanceComponent = "serving-instance"
)

func isRoutingOperation(kind string) bool {
	return kind == operationRoute || kind == operationUnroute || kind == operationRelease
}

func (operation Operation) controlsWorkload(uid string) bool {
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
	PodUID     string `json:"podUID"`
	NodeUID    string `json:"nodeUID"`
	DecisionID string `json:"decisionID"`
	Created    bool   `json:"created"`
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
	effectsNotStarted bool
}

// OperationConfig.Measurement selects measurement operations: they prepare only
// on a measurement POOL Node and tolerate its taint. Other operations refuse it.
type OperationConfig struct {
	Namespace    string
	PollInterval time.Duration
	Measurement  bool
	// MetricLimits is process delivery state, not a stored sampling setting.
	MetricLimits metricobserver.Limits
}

// ErrNodeInMeasurement rejects a Workload action on a measurement POOL Node.
var ErrNodeInMeasurement = errors.New("NodeInMeasurement: Node carries the " + profilingrun.MeasurementTaintKey + " taint")

type Operations struct {
	cluster  *ClusterState
	store    *Store
	kube     kubernetes.Interface
	runtime  *serving.Runtime
	observer *Observer
	native   *sdk.Client
	config   OperationConfig
}

func NewOperations(cluster *ClusterState, store *Store, kube kubernetes.Interface, runtime *serving.Runtime, observer *Observer, native *sdk.Client, config OperationConfig) (*Operations, error) {
	if cluster == nil || store == nil || kube == nil || runtime == nil || observer == nil || native == nil || config.Namespace == "" || config.PollInterval <= 0 {
		return nil, errors.New("atomic operations require store, clients, runtime, observer, namespace and positive poll interval")
	}
	return &Operations{cluster: cluster, store: store, kube: kube, runtime: runtime, observer: observer, native: native, config: config}, nil
}

func (o *Operations) wait(ctx context.Context, step func() (bool, error)) error {
	for {
		done, err := step()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		timer := time.NewTimer(o.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (o *Operations) Capture(ctx context.Context, node serving.NodeReference) (OperationState, error) {
	s := OperationState{At: time.Now().UTC(), Node: node, Native: map[string]native.State{}}
	current, err := o.kube.CoreV1().Nodes().Get(ctx, node.Name, meta.GetOptions{})
	if err != nil {
		return s, err
	}
	if string(current.UID) != node.UID {
		return s, errors.New("physical Node identity changed")
	}
	var classes edge.DeviceClassList
	if err = o.store.client.List(ctx, &classes); err != nil {
		return s, err
	}
	s.PowerMode = readBackMode(current, nodeClass(current, classes.Items))
	for _, i := range o.runtime.Instances() {
		if i.Node.UID == node.UID {
			s.Instances = append(s.Instances, i)
		}
	}
	pods, err := o.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		return s, err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName != node.Name || p.Status.Phase == core.PodSucceeded || p.Status.Phase == core.PodFailed {
			continue
		}
		pod, e := workloadkube.ProjectPod(*p, node.Name, []string{profilingrun.MeasurementWorkloadLabelKey})
		if e != nil {
			return s, e
		}
		s.Pods = append(s.Pods, pod)
		if p.Labels["app.kubernetes.io/component"] != servingInstanceComponent {
			continue
		}
		status := firstfloorStatus(p)
		if status == nil || status.State.Running == nil || status.ContainerID == "" {
			s.Reasons = append(s.Reasons, p.Name+": native identity unavailable")
			continue
		}
		id := physicalID(p.UID, status.ContainerID)
		endpoint, e := nativeInstanceEndpoint(ctx, o.kube, p)
		if e != nil {
			s.Reasons = append(s.Reasons, p.Name+": "+e.Error())
			continue
		}
		state, e := o.native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: "chill_model"})
		if e != nil {
			s.Reasons = append(s.Reasons, p.Name+": "+e.Error())
			continue
		}
		s.Native[id] = state
		allocation := serving.SchedulerAllocation{GPUUUID: state.Metadata.Runtime.GPUUUID, MemoryBytes: uint64(p.Spec.Containers[0].Resources.Limits.Memory().Value())}
		s.States = append(s.States, serving.SchedulerInstanceState(serving.SchedulerInstance{Profile: &serving.Profile{ID: state.Metadata.ProfileID}, Allocation: allocation}))
	}
	slices.SortFunc(s.Pods, func(a, b workloadobserver.Pod) int { return strings.Compare(a.UID, b.UID) })
	slices.Sort(s.States)
	return s, nil
}

func (o *Operations) Execute(ctx context.Context, operation Operation) (OperationResult, error) {
	result := OperationResult{Operation: operation, StartedAt: time.Now().UTC(), Status: nativeOperationRunning}
	before := OperationState{At: result.StartedAt}
	err := o.validateOperationBinding(ctx, operation)
	if err == nil && !isRoutingOperation(operation.Kind) {
		before, err = o.Capture(ctx, operation.Node)
	}
	result.Before = before
	if err == nil {
		err = o.executeOperation(ctx, operation, &result)
	}
	actual := result.Actual
	actual.At = time.Now().UTC()
	var captureErr error
	if !isRoutingOperation(operation.Kind) {
		actual, captureErr = o.Capture(ctx, operation.Node)
		for id, state := range result.Actual.Native {
			if _, observed := actual.Native[id]; !observed {
				if actual.Native == nil {
					actual.Native = map[string]native.State{}
				}
				actual.Native[id] = state
			}
		}
		if result.Before.Routing != nil {
			actual.Routing, _ = o.runtime.SchedulerRouting()
		}
	}
	result.Actual = actual
	err = errors.Join(err, captureErr)
	result.EndedAt = time.Now().UTC()
	if err == nil {
		result.Status = nativeOperationComplete
	} else {
		result.Status = nativeOperationFailed
		if !reflect.DeepEqual(before.Pods, actual.Pods) || before.PowerMode != actual.PowerMode || len(result.Progress) > 0 {
			result.Status = nativeOperationPartial
		}
		result.Error = err.Error()
	}
	return result, err
}

func (o *Operations) executeOperation(ctx context.Context, operation Operation, result *OperationResult) error {
	var err error
	switch operation.Kind {
	case operationPrepare:
		if operation.Prepare == nil {
			err = errors.New("prepare specification required")
		} else {
			result.Preparation, err = o.PrepareInstance(ctx, *operation.Prepare)
			if err == nil {
				result.Progress = append(result.Progress, "prepare observed")
			}
		}
	case operationRemove:
		result.DrainNative = map[string]native.State{}
		err = o.removeInstance(ctx, operation.Node, operation.PodUID, operation.Instance, result.DrainNative)
		if err == nil {
			result.Progress = append(result.Progress, "stop/drain/termination/removal observed")
		}
	case operationPower:
		err = o.ChangePower(ctx, operation.Node, operation.Holder, operation.PowerMode, operation.MaxActuationDuration)
	case operationRoute, operationUnroute:
		if operation.Target == nil {
			err = errors.New("routing target required")
		} else {
			err = o.ApplyTarget(ctx, *operation.Target, result)
		}
	case operationRelease:
		result.Before.Routing, _ = o.runtime.SchedulerRouting()
		result.Before.Instances = o.runtime.Instances()
		err = o.observer.Refresh(ctx)
		if err == nil {
			err = o.runtime.ReleaseSchedulerRouting(operation.WorkloadUID)
		}
		result.Actual.At = time.Now().UTC()
		result.Actual.Routing, _ = o.runtime.SchedulerRouting()
		result.Actual.Instances = o.runtime.Instances()
	case operationStop:
		err = o.StopInstance(ctx, operation.Instance)
	case operationDrain:
		err = o.detachInstance(ctx, operation.Instance)
	case operationResize:
		if operation.Prepare == nil || operation.Target == nil {
			err = errors.New("resize requires prepared replacement and routing target")
		} else {
			result.Preparation, err = o.PrepareInstance(ctx, *operation.Prepare)
			if err == nil {
				result.Progress = append(result.Progress, "replacement ready")
				err = o.ApplyTarget(ctx, *operation.Target, result)
			}
			if err == nil {
				result.Progress = append(result.Progress, "routing applied")
				result.DrainNative = map[string]native.State{}
				err = o.removeInstance(ctx, operation.Node, operation.PodUID, operation.Instance, result.DrainNative)
			}
		}
	default:
		err = fmt.Errorf("unsupported atomic operation %q", operation.Kind)
	}
	return err
}

func (o *Operations) validateOperationBinding(ctx context.Context, operation Operation) error {
	if isRoutingOperation(operation.Kind) {
		return nil
	}
	if operation.Kind == operationRemove && operation.WorkloadUID != "" {
		current := o.runtime.SchedulerInputs(time.Now)
		for _, route := range current.Routing {
			if route[operation.Instance] > 0 {
				return refused("OperationConflict", "current routing still owns the removal target")
			}
		}
		for _, request := range current.Outstanding {
			if request.Instance == operation.Instance {
				return refused("OperationConflict", "current outstanding request still owns the removal target")
			}
		}
	}
	actual, err := o.kube.CoreV1().Nodes().Get(ctx, operation.Node.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	if string(actual.UID) != operation.Node.UID || actual.DeletionTimestamp != nil {
		return errors.New("operation physical Node identity changed")
	}
	if operation.Kind == operationPrepare || operation.Kind == operationResize {
		if operation.Prepare == nil || operation.Prepare.Instance.Node != operation.Node {
			return errors.New("prepare physical Node differs from operation scope")
		}
	}
	if operation.Kind == operationResize {
		if operation.Target == nil {
			return errors.New("resize routing target unavailable")
		}
		matched := false
		for _, i := range operation.Target.Instances {
			if i.ID == operation.Prepare.Instance.ID && i.Node == operation.Node {
				matched = true
			}
		}
		if !matched {
			return errors.New("resize replacement target differs from operation physical scope")
		}
	}
	if operation.Kind != operationRemove && operation.Kind != operationResize && operation.Kind != operationStop && operation.Kind != operationDrain {
		return nil
	}
	return o.validatePhysicalInstanceBinding(ctx, operation, actual)
}

func (o *Operations) validatePhysicalInstanceBinding(ctx context.Context, operation Operation, actual *core.Node) error {
	uid, containerID, ok := strings.Cut(operation.Instance, "/")
	if operation.Instance == "" && operation.Kind == operationRemove && operation.PodUID != "" {
		uid = operation.PodUID
	} else if !ok || containerID == "" || operation.PodUID != "" && operation.PodUID != uid {
		return errors.New("exact operation Pod/executor identity required")
	}
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if string(pod.UID) != uid {
			continue
		}
		status := firstfloorStatus(&pod)
		if pod.Spec.NodeName != actual.Name || pod.Labels["app.kubernetes.io/component"] != servingInstanceComponent ||
			operation.Instance != "" && (status == nil || status.ContainerID != containerID) ||
			operation.Instance == "" && !podTerminated(&pod) && !podAwaitingExecutor(&pod) {
			return errors.New("operation instance belongs to different physical Node/source")
		}
		for _, i := range o.runtime.Instances() {
			if i.ID == operation.Instance && i.Node != operation.Node {
				return errors.New("operation physical instance Node observation changed")
			}
		}
		return nil
	}
	return errors.New("operation physical instance unavailable")
}
