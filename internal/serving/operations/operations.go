package operations

import (
	context "context"
	errors "errors"
	fmt "fmt"
	reflect "reflect"
	slices "slices"
	strings "strings"
	time "time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	metricobserver "github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	serving "github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	workloadobserver "github.com/lab-paper-code/chill/internal/workloadobserver"
	workloadkube "github.com/lab-paper-code/chill/internal/workloadobserver/kube"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetes "k8s.io/client-go/kubernetes"
)

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
	Cluster  removalState
	Store    objectStore
	Kube     kubernetes.Interface
	Runtime  *serving.Runtime
	Observer sourceObserver
	Native   *sdk.Client
	Config   OperationConfig
}

func NewOperations(cluster removalState, store objectStore, kube kubernetes.Interface, runtime *serving.Runtime, observer sourceObserver, native *sdk.Client, config OperationConfig) (*Operations, error) {
	if nilDependency(cluster) || nilDependency(store) || kube == nil || runtime == nil || nilDependency(observer) || native == nil || config.Namespace == "" || config.PollInterval <= 0 {
		return nil, errors.New("atomic operations require store, clients, runtime, observer, namespace and positive poll interval")
	}
	return &Operations{Cluster: cluster, Store: store, Kube: kube, Runtime: runtime, Observer: observer, Native: native, Config: config}, nil
}

func (o *Operations) Wait(ctx context.Context, step func() (bool, error)) error {
	for {
		done, err := step()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		timer := time.NewTimer(o.Config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (o *Operations) Capture(ctx context.Context, node serving.NodeReference) (servingstate.OperationState, error) {
	s := servingstate.OperationState{At: time.Now().UTC(), Node: node, Native: map[string]native.State{}}
	current, err := o.Kube.CoreV1().Nodes().Get(ctx, node.Name, meta.GetOptions{})
	if err != nil {
		return s, err
	}
	if string(current.UID) != node.UID {
		return s, errors.New("physical Node identity changed")
	}
	var classes edge.DeviceClassList
	if err = o.Store.Client().List(ctx, &classes); err != nil {
		return s, err
	}
	s.PowerMode = servingstate.ReadBackMode(current, servingstate.NodeClass(current, classes.Items))
	for _, i := range o.Runtime.Instances() {
		if i.Node.UID == node.UID {
			s.Instances = append(s.Instances, i)
		}
	}
	pods, err := o.Kube.CoreV1().Pods("").List(ctx, meta.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
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
		if p.Labels["app.kubernetes.io/component"] != servingstate.ServingInstanceComponent {
			continue
		}
		status := servingstate.FirstfloorStatus(p)
		if status == nil || status.State.Running == nil || status.ContainerID == "" {
			s.Reasons = append(s.Reasons, p.Name+": native identity unavailable")
			continue
		}
		id := servingstate.PhysicalID(p.UID, status.ContainerID)
		endpoint, e := servingstate.NativeInstanceEndpoint(ctx, o.Kube, p)
		if e != nil {
			s.Reasons = append(s.Reasons, p.Name+": "+e.Error())
			continue
		}
		state, e := o.Native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: "chill_model"})
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

func (o *Operations) Execute(ctx context.Context, operation servingstate.Operation) (servingstate.OperationResult, error) {
	result := servingstate.OperationResult{Operation: operation, StartedAt: time.Now().UTC(), Status: servingstate.NativeOperationRunning}
	before := servingstate.OperationState{At: result.StartedAt}
	err := o.ValidateOperationBinding(ctx, operation)
	if err == nil && !servingstate.IsRoutingOperation(operation.Kind) {
		before, err = o.Capture(ctx, operation.Node)
	}
	result.Before = before
	if err == nil {
		err = o.executeOperation(ctx, operation, &result)
	}
	actual := result.Actual
	actual.At = time.Now().UTC()
	var captureErr error
	if !servingstate.IsRoutingOperation(operation.Kind) {
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
			actual.Routing, _ = o.Runtime.SchedulerRouting()
		}
	}
	result.Actual = actual
	err = errors.Join(err, captureErr)
	result.EndedAt = time.Now().UTC()
	if err == nil {
		result.Status = servingstate.NativeOperationComplete
	} else {
		result.Status = servingstate.NativeOperationFailed
		if !reflect.DeepEqual(before.Pods, actual.Pods) || before.PowerMode != actual.PowerMode || len(result.Progress) > 0 {
			result.Status = servingstate.NativeOperationPartial
		}
		result.Error = err.Error()
	}
	return result, err
}

func (o *Operations) executeOperation(ctx context.Context, operation servingstate.Operation, result *servingstate.OperationResult) error {
	var err error
	switch operation.Kind {
	case servingstate.OperationPrepare:
		if operation.Prepare == nil {
			err = errors.New("prepare specification required")
		} else {
			result.Preparation, err = o.PrepareInstance(ctx, *operation.Prepare)
			if err == nil {
				result.Progress = append(result.Progress, "prepare observed")
			}
		}
	case servingstate.OperationRemove:
		result.DrainNative = map[string]native.State{}
		err = o.removeInstance(ctx, operation.Node, operation.PodUID, operation.Instance, result.DrainNative)
		if err == nil {
			result.Progress = append(result.Progress, "stop/drain/termination/removal observed")
		}
	case servingstate.OperationPower:
		err = o.ChangePower(ctx, operation.Node, operation.Holder, operation.PowerMode, operation.MaxActuationDuration)
	case servingstate.OperationRoute, servingstate.OperationUnroute:
		if operation.Target == nil {
			err = errors.New("routing target required")
		} else {
			err = o.ApplyTarget(ctx, *operation.Target, result)
		}
	case servingstate.OperationRelease:
		result.Before.Routing, _ = o.Runtime.SchedulerRouting()
		result.Before.Instances = o.Runtime.Instances()
		err = o.Observer.Refresh(ctx)
		if err == nil {
			err = o.Runtime.ReleaseSchedulerRouting(operation.WorkloadUID)
		}
		result.Actual.At = time.Now().UTC()
		result.Actual.Routing, _ = o.Runtime.SchedulerRouting()
		result.Actual.Instances = o.Runtime.Instances()
	case servingstate.OperationStop:
		err = o.StopInstance(ctx, operation.Instance)
	case servingstate.OperationDrain:
		err = o.detachInstance(ctx, operation.Instance)
	case servingstate.OperationResize:
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

func (o *Operations) ValidateOperationBinding(ctx context.Context, operation servingstate.Operation) error {
	if servingstate.IsRoutingOperation(operation.Kind) {
		return nil
	}
	if operation.Kind == servingstate.OperationRemove && operation.WorkloadUID != "" {
		current := o.Runtime.SchedulerInputs(time.Now)
		for _, route := range current.Routing {
			if route[operation.Instance] > 0 {
				return servingstate.Refused("OperationConflict", "current routing still owns the removal target")
			}
		}
		for _, request := range current.Outstanding {
			if request.Instance == operation.Instance {
				return servingstate.Refused("OperationConflict", "current outstanding request still owns the removal target")
			}
		}
	}
	actual, err := o.Kube.CoreV1().Nodes().Get(ctx, operation.Node.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	if string(actual.UID) != operation.Node.UID || actual.DeletionTimestamp != nil {
		return errors.New("operation physical Node identity changed")
	}
	if operation.Kind == servingstate.OperationPrepare || operation.Kind == servingstate.OperationResize {
		if operation.Prepare == nil || operation.Prepare.Instance.Node != operation.Node {
			return errors.New("prepare physical Node differs from operation scope")
		}
	}
	if operation.Kind == servingstate.OperationResize {
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
	if operation.Kind != servingstate.OperationRemove && operation.Kind != servingstate.OperationResize && operation.Kind != servingstate.OperationStop && operation.Kind != servingstate.OperationDrain {
		return nil
	}
	return o.validatePhysicalInstanceBinding(ctx, operation, actual)
}

func (o *Operations) validatePhysicalInstanceBinding(ctx context.Context, operation servingstate.Operation, actual *core.Node) error {
	uid, containerID, ok := strings.Cut(operation.Instance, "/")
	if operation.Instance == "" && operation.Kind == servingstate.OperationRemove && operation.PodUID != "" {
		uid = operation.PodUID
	} else if !ok || containerID == "" || operation.PodUID != "" && operation.PodUID != uid {
		return errors.New("exact operation Pod/executor identity required")
	}
	pods, err := o.Kube.CoreV1().Pods(o.Config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if string(pod.UID) != uid {
			continue
		}
		status := servingstate.FirstfloorStatus(&pod)
		if pod.Spec.NodeName != actual.Name || pod.Labels["app.kubernetes.io/component"] != servingstate.ServingInstanceComponent ||
			operation.Instance != "" && (status == nil || status.ContainerID != containerID) ||
			operation.Instance == "" && !servingstate.PodTerminated(&pod) && !servingstate.PodAwaitingExecutor(&pod) {
			return errors.New("operation instance belongs to different physical Node/source")
		}
		for _, i := range o.Runtime.Instances() {
			if i.ID == operation.Instance && i.Node != operation.Node {
				return errors.New("operation physical instance Node observation changed")
			}
		}
		return nil
	}
	return errors.New("operation physical instance unavailable")
}
