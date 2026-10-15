package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	operationMembership      = "membership"
	nativeOperationComplete  = "complete"
	nativeOperationRunning   = "running"
	nativeOperationCancel    = "cancel"
	nativeStateUnloaded      = "unloaded"
	measurementAccepted      = "Accepted"
	measurementWallScope     = "node-exclusive-wall"
	servingInstanceComponent = "serving-instance"
)

// PrepareSpec freezes one measured execution and immutable profile delivery.
type PrepareSpec struct {
	Name                  string                         `json:"name"`
	Instance              serving.SchedulerInstance      `json:"instance"`
	Bundle                evidencestore.ObjectIdentity   `json:"bundle"`
	Store                 storekube.Binding              `json:"store"`
	Config                native.Config                  `json:"config"`
	MemoryBytes           uint64                         `json:"memoryBytes"`
	CompletionCalibration *serving.CompletionCalibration `json:"completionCalibration,omitempty"`
}
type Operation struct {
	ID             string                      `json:"id"`
	Kind           string                      `json:"kind"`
	Node           serving.NodeReference       `json:"node"`
	Instance       string                      `json:"instance,omitempty"`
	Pool           string                      `json:"pool,omitempty"`
	Prepare        *PrepareSpec                `json:"prepare,omitempty"`
	Actuation      *edge.NodeActuation         `json:"actuation,omitempty"`
	Target         *serving.SchedulerCandidate `json:"target,omitempty"`
	SourceConfigRV string                      `json:"sourceConfigRV,omitempty"`
	StaticRouting  map[string]map[string]int   `json:"staticRouting,omitempty"`
}
type OperationState struct {
	At        time.Time                   `json:"at"`
	Node      serving.NodeReference       `json:"node"`
	Pool      string                      `json:"pool"`
	PowerMode string                      `json:"powerMode"`
	Instances []serving.InstanceState     `json:"instances"`
	Native    map[string]native.State     `json:"native"`
	Pods      []string                    `json:"pods"`
	States    []string                    `json:"states"`
	Reasons   []string                    `json:"reasons,omitempty"`
	Routing   map[string]map[string]int32 `json:"routing,omitempty"`
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
}
type OperationConfig struct {
	Namespace          string
	ActuationNamespace string
	PollInterval       time.Duration
}
type Operations struct {
	store    *Store
	kube     kubernetes.Interface
	runtime  *serving.Runtime
	observer *Observer
	native   *sdk.Client
	config   OperationConfig
}

func NewOperations(store *Store, kube kubernetes.Interface, runtime *serving.Runtime, observer *Observer, native *sdk.Client, config OperationConfig) (*Operations, error) {
	if store == nil || kube == nil || runtime == nil || observer == nil || native == nil || config.Namespace == "" || config.PollInterval <= 0 {
		return nil, errors.New("atomic operations require store, clients, runtime, observer, namespace and positive poll interval")
	}
	if config.ActuationNamespace == "" {
		config.ActuationNamespace = config.Namespace
	}
	return &Operations{store: store, kube: kube, runtime: runtime, observer: observer, native: native, config: config}, nil
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
	s.Pool = current.Labels[ServingPoolLabel]
	var acts edge.NodeActuationList
	if err = o.store.client.List(ctx, &acts, client.InNamespace(o.config.ActuationNamespace)); err != nil {
		return s, err
	}
	var latest *edge.NodeActuation
	for i := range acts.Items {
		a := &acts.Items[i]
		if a.Spec.Node.UID == node.UID && (latest == nil || a.Spec.StageRequestedAt.After(latest.Spec.StageRequestedAt.Time)) {
			latest = a
		}
	}
	if latest != nil && latest.Status.Phase == edge.NodeActuationPhaseReady && latest.Status.TargetObservation != nil {
		s.PowerMode = latest.Status.TargetObservation.PowerMode
	}
	for _, pool := range o.runtime.Pools() {
		for _, i := range pool.Instances {
			if i.Node.UID == node.UID {
				s.Instances = append(s.Instances, i)
			}
		}
	}
	pods, err := o.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{})
	if err != nil {
		return s, err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName != node.Name || p.Status.Phase == core.PodSucceeded || p.Status.Phase == core.PodFailed {
			continue
		}
		s.Pods = append(s.Pods, p.Namespace+"/"+p.Name+"/"+string(p.UID))
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
		allocation := serving.SchedulerAllocation{CPUs: state.Metadata.Runtime.CPUAffinity, GPUUUID: state.Metadata.Runtime.GPUUUID, Threads: state.Metadata.Runtime.IntraOpThreads, MemoryBytes: uint64(p.Spec.Containers[0].Resources.Limits.Memory().Value())}
		s.States = append(s.States, serving.SchedulerInstanceState(serving.SchedulerInstance{Profile: &serving.Profile{ID: state.Metadata.ProfileID}, Allocation: allocation}))
	}
	if s.PowerMode == "" {
		if err := o.captureFixedPower(ctx, &s, current, acts.Items); err != nil {
			return s, err
		}
	}
	slices.Sort(s.Pods)
	slices.Sort(s.States)
	return s, nil
}
func (o *Operations) captureFixedPower(ctx context.Context, s *OperationState, current *core.Node, acts []edge.NodeActuation) error {
	if s.PowerMode == "" {
		var classes edge.DeviceClassList
		if err := o.store.client.List(ctx, &classes); err != nil {
			return err
		}
		for i := range classes.Items {
			class := &classes.Items[i]
			for _, mode := range class.Spec.PowerModes {
				if mode.Actuator.Kind == edge.PowerModeActuatorFixed && class.Spec.Architecture == current.Status.NodeInfo.Architecture && nodeClassMatches(current, class) && confirmedPower(current, class, mode.Name, acts) == nil {
					if s.PowerMode != "" && s.PowerMode != mode.Name {
						return errors.New("ambiguous fixed power mode")
					}
					s.PowerMode = mode.Name
				}
			}
		}
	}
	return nil
}
func (o *Operations) Execute(ctx context.Context, operation Operation) (OperationResult, error) {
	result := OperationResult{Operation: operation, StartedAt: time.Now().UTC(), Status: nativeOperationRunning}
	before := OperationState{At: result.StartedAt}
	err := o.validateOperationBinding(ctx, operation)
	if err == nil && operation.Kind != operationRoute {
		before, err = o.Capture(ctx, operation.Node)
	}
	result.Before = before
	if err == nil {
		switch operation.Kind {
		case operationPrepare:
			if operation.Prepare == nil {
				err = errors.New("prepare specification required")
			} else {
				err = o.PrepareInstance(ctx, *operation.Prepare)
				if err == nil {
					result.Progress = append(result.Progress, "prepare observed")
				}
			}
		case operationRemove:
			result.DrainNative = map[string]native.State{}
			err = o.removeInstance(ctx, operation.Node, operation.Instance, result.DrainNative)
			if err == nil {
				result.Progress = append(result.Progress, "stop/drain/termination/removal observed")
			}
		case operationMembership:
			err = o.observer.MoveNodePool(ctx, operation.Node, operation.Pool)
		case operationPower:
			err = o.ChangePower(ctx, operation.Node, operation.Actuation)
		case operationRoute:
			if operation.Target == nil {
				err = errors.New("routing target required")
			} else {
				err = o.ApplyTarget(ctx, *operation.Target)
			}
		case operationStop:
			err = o.StopInstance(ctx, operation.Instance)
		case operationDrain:
			err = o.wait(ctx, func() (bool, error) { return o.runtime.DetachInstance(operation.Instance) })
		case operationResize:
			if operation.Prepare == nil || operation.Target == nil {
				err = errors.New("resize requires prepared replacement and routing target")
			} else {
				err = o.PrepareInstance(ctx, *operation.Prepare)
				if err == nil {
					result.Progress = append(result.Progress, "replacement ready")
					err = o.ApplyTarget(ctx, *operation.Target)
				}
				if err == nil {
					result.Progress = append(result.Progress, "routing applied")
					result.DrainNative = map[string]native.State{}
					err = o.removeInstance(ctx, operation.Node, operation.Instance, result.DrainNative)
				}
			}
		default:
			err = fmt.Errorf("unsupported atomic operation %q", operation.Kind)
		}
	}
	actual := OperationState{At: time.Now().UTC()}
	var captureErr error
	if operation.Kind != operationRoute {
		actual, captureErr = o.Capture(ctx, operation.Node)
	}
	result.Actual = actual
	err = errors.Join(err, captureErr)
	result.EndedAt = time.Now().UTC()
	if err == nil {
		result.Status = nativeOperationComplete
	} else {
		result.Status = nativeOperationFailed
		if !reflect.DeepEqual(before.Pods, actual.Pods) || before.Pool != actual.Pool || before.PowerMode != actual.PowerMode || len(result.Progress) > 0 {
			result.Status = nativeOperationPartial
		}
		result.Error = err.Error()
	}
	return result, err
}
func samePreparedPod(actual, desired *core.Pod) bool {
	if actual.Annotations[ServingProfileAnnotation] != desired.Annotations[ServingProfileAnnotation] || actual.Annotations[ServingSlotAnnotation] != desired.Annotations[ServingSlotAnnotation] || actual.Spec.NodeName != desired.Spec.NodeName || len(actual.Spec.Containers) != 1 || !samePreparedContainer(actual.Spec.Containers[0], desired.Spec.Containers[0]) || len(actual.Spec.InitContainers) != len(desired.Spec.InitContainers) {
		return false
	}
	for i := range desired.Spec.InitContainers {
		if !samePreparedContainer(actual.Spec.InitContainers[i], desired.Spec.InitContainers[i]) {
			return false
		}
	}
	return true
}

func (o *Operations) PrepareInstance(ctx context.Context, spec PrepareSpec) error {
	if err := validatePreparation(spec); err != nil {
		return err
	}
	p := spec.Instance.Profile
	if err := bindPreparationAffinity(&spec); err != nil {
		return err
	}
	node, err := o.kube.CoreV1().Nodes().Get(ctx, spec.Instance.Node.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	if string(node.UID) != spec.Instance.Node.UID || node.Labels[ServingPoolLabel] != spec.Instance.Pool {
		return errors.New("actual Node/pool differs from preparation")
	}
	if err := o.ensurePreparationConfig(ctx, spec); err != nil {
		return err
	}
	cpuRequest, err := resource.ParseQuantity(p.Condition.CPU.Request)
	if err != nil {
		return err
	}
	cpuLimit, err := resource.ParseQuantity(p.Condition.CPU.Limit)
	if err != nil {
		return err
	}
	requests := core.ResourceList{core.ResourceCPU: cpuRequest, core.ResourceMemory: *resource.NewQuantity(int64(spec.MemoryBytes), resource.BinarySI)}
	limits := core.ResourceList{core.ResourceCPU: cpuLimit, core.ResourceMemory: *resource.NewQuantity(int64(spec.MemoryBytes), resource.BinarySI)}
	initResources := core.ResourceRequirements{Requests: requests, Limits: limits}
	if spec.Instance.Allocation.GPUUUID != "" {
		requests = requests.DeepCopy()
		limits = limits.DeepCopy()
		requests["nvidia.com/gpu"] = resource.MustParse("1")
		limits["nvidia.com/gpu"] = resource.MustParse("1")
	}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: o.config.Namespace, Name: spec.Name, Labels: map[string]string{"app.kubernetes.io/component": servingInstanceComponent}, Annotations: map[string]string{ServingProfileAnnotation: p.ID, ServingWeightAnnotation: strconv.Itoa(int(spec.Instance.Weight)), ServingPolicyAnnotation: spec.Config.Policy.DecisionID, ServingSlotAnnotation: spec.Instance.ID}, Finalizers: []string{sourceFinalizer}}, Spec: core.PodSpec{NodeName: node.Name, RestartPolicy: core.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), InitContainers: []core.Container{
		{Name: "artifact", Image: p.Condition.Artifact.Image, Command: []string{"sh", "-ec"}, Args: []string{`cp "$1" "$2"`, "artifact-copy", p.Condition.Artifact.Path, path.Join("/chill-artifact", "model"+path.Ext(p.Condition.Artifact.Path))}, Resources: initResources, VolumeMounts: []core.VolumeMount{{Name: "artifact", MountPath: "/chill-artifact"}}},
		{Name: "profile", Image: p.Condition.RuntimeImage, Command: []string{"/opt/chill/bin/serving-entrypoint"}, Args: []string{"--materialize-profile", "--store-name=" + spec.Store.Store, "--store-endpoint=" + spec.Store.Endpoint, "--store-bucket=" + spec.Store.Bucket, "--store-region=" + spec.Store.Region, "--profile-object-key=" + spec.Bundle.Key, "--profile-object-size=" + strconv.FormatInt(spec.Bundle.Size, 10), "--profile-output=/profile/bundle.json"}, Resources: initResources, Env: []core.EnvVar{secretEnvOperation("AWS_ACCESS_KEY_ID", spec.Store.CredentialsSecretName, component.EvidenceStoreAccessKeyKey), secretEnvOperation("AWS_SECRET_ACCESS_KEY", spec.Store.CredentialsSecretName, component.EvidenceStoreSecretKeyKey)}, VolumeMounts: []core.VolumeMount{{Name: "profile", MountPath: "/profile"}}},
	}, Containers: []core.Container{{Name: "firstfloor", Image: p.Condition.RuntimeImage, Command: []string{"/opt/chill/bin/serving-worker"}, Args: []string{"--config=/config/config.json", "--bind-address=:8080", "--node-name=" + node.Name}, Ports: []core.ContainerPort{{Name: "http", ContainerPort: 8080}}, Resources: core.ResourceRequirements{Requests: requests, Limits: limits}, VolumeMounts: []core.VolumeMount{{Name: "artifact", MountPath: "/model", ReadOnly: true}, {Name: "profile", MountPath: "/profile", ReadOnly: true}, {Name: "config", MountPath: "/config", ReadOnly: true}}, ReadinessProbe: &core.Probe{ProbeHandler: core.ProbeHandler{HTTPGet: &core.HTTPGetAction{Path: "/v2/health/ready", Port: port8080()}}, PeriodSeconds: 1}}}, Volumes: []core.Volume{{Name: "artifact", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "profile", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "config", VolumeSource: core.VolumeSource{ConfigMap: &core.ConfigMapVolumeSource{LocalObjectReference: core.LocalObjectReference{Name: spec.Name}}}}}}}
	if err = o.store.client.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	if apierrors.IsAlreadyExists(err) {
		existing := &core.Pod{}
		if err = o.store.client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, existing); err != nil {
			return err
		}
		if !samePreparedPod(existing, pod) {
			return errors.New("existing preparation Pod differs")
		}
		pod = existing
	}
	if err = o.ensureInstanceService(ctx, pod); err != nil {
		return err
	}
	return o.wait(ctx, func() (bool, error) {
		if err := o.observer.Refresh(ctx); err != nil {
			return false, err
		}
		current, e := o.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
		if e != nil {
			return false, e
		}
		if current.UID != pod.UID {
			return false, errors.New("prepared Pod identity changed")
		}
		if current.Status.Phase == core.PodFailed {
			return false, errors.New("native preparation Pod failed")
		}
		for _, pool := range o.runtime.Pools() {
			for _, i := range pool.Instances {
				if strings.HasPrefix(i.ID, string(pod.UID)+"/") && i.Ready {
					return true, nil
				}
			}
		}
		return false, nil
	})
}
func (o *Operations) ensurePreparationConfig(ctx context.Context, spec PrepareSpec) error {
	raw, err := json.Marshal(spec.Config)
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return errors.New("native mutable config exceeds small policy bound")
	}
	cm := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: o.config.Namespace, Name: spec.Name}, Data: map[string]string{"config.json": string(raw)}}
	if err = o.store.client.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	if apierrors.IsAlreadyExists(err) {
		existing := &core.ConfigMap{}
		if err = o.store.client.Get(ctx, types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, existing); err != nil {
			return err
		}
		var previous native.Config
		if err = json.Unmarshal([]byte(existing.Data["config.json"]), &previous); err != nil {
			return err
		}
		desired := spec.Config
		previous.Policy = native.Policy{DeadlinePolicy: native.EffectiveDeadlinePolicy(previous.Policy.DeadlinePolicy)}
		desired.Policy = native.Policy{DeadlinePolicy: native.EffectiveDeadlinePolicy(desired.Policy.DeadlinePolicy)}
		if !reflect.DeepEqual(previous, desired) {
			return errors.New("existing immutable preparation execution config differs")
		}
	}
	return nil
}
func validatePreparation(spec PrepareSpec) error {
	p := spec.Instance.Profile
	if p == nil || p.Condition.Firstfloor == nil || p.RuntimeMetadata == nil || spec.Name == "" || spec.Instance.Node.UID != p.Condition.NodeUID {
		return errors.New("measured firstfloor preparation contract required")
	}
	if err := spec.Store.Validate(); err != nil {
		return err
	}
	if spec.Bundle.Store != spec.Store.Store || spec.Bundle.Size <= 0 {
		return errors.New("immutable profile delivery identity required")
	}
	if spec.Config.Policy.ProfileID != p.ID || spec.Config.Mode != nativeServingMode || len(spec.Config.Policy.Batches) != 0 || spec.Config.ProfilePath != "/profile/bundle.json" {
		return errors.New("small policy must reference immutable startup profile")
	}
	if native.EffectiveDeadlinePolicy(spec.Config.Policy.DeadlinePolicy) !=
		native.EffectiveDeadlinePolicy(p.Condition.Firstfloor.DeadlinePolicy) {
		return errors.New("preparation deadline policy differs from measured profile")
	}
	if spec.MemoryBytes != spec.Instance.Allocation.MemoryBytes || spec.MemoryBytes > uint64(1<<63-1) {
		return errors.New("preparation memory must exactly equal selected bounded allocation")
	}
	if len(p.Condition.CPU.AffinityCPUs) > 0 && !slices.Equal(p.Condition.CPU.AffinityCPUs, p.RuntimeMetadata.CPUAffinity) {
		return errors.New("explicit measured CPU mask differs from actual runtime")
	}
	if !slices.Equal(spec.Instance.Allocation.CPUs, p.RuntimeMetadata.CPUAffinity) || spec.Instance.Allocation.GPUUUID != p.RuntimeMetadata.GPUUUID || spec.Instance.Allocation.Threads != p.RuntimeMetadata.IntraOpThreads {
		return errors.New("preparation CPU/GPU/thread selection differs from measured physical conditions")
	}
	return nil
}
func bindPreparationAffinity(spec *PrepareSpec) error {
	cpuIDs := make([]string, len(spec.Instance.Allocation.CPUs))
	for i, id := range spec.Instance.Allocation.CPUs {
		cpuIDs[i] = strconv.Itoa(id)
	}
	command := slices.Clone(spec.Config.RunnerCommand)
	for i := 0; i < len(command); i++ {
		if command[i] == "--cpu-affinity" {
			if i+1 >= len(command) {
				return errors.New("runner CPU affinity flag lacks value")
			}
			command = append(command[:i], command[i+2:]...)
			i--
		} else if strings.HasPrefix(command[i], "--cpu-affinity=") {
			command = append(command[:i], command[i+1:]...)
			i--
		}
	}
	spec.Config.RunnerCommand = append(command, "--cpu-affinity", strings.Join(cpuIDs, ","))
	return nil
}
func secretEnvOperation(name, secret, key string) core.EnvVar {
	return core.EnvVar{Name: name, ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: secret}, Key: key}}}
}
func (o *Operations) RemoveInstance(ctx context.Context, node serving.NodeReference, id string) error {
	return o.removeInstance(ctx, node, id, nil)
}
func (o *Operations) StopInstance(ctx context.Context, id string) error {
	if err := o.wait(ctx, func() (bool, error) { return o.runtime.StopSchedulerInstance(id) }); err != nil {
		return err
	}
	o.observer.mu.Lock()
	o.observer.removingInstances[id] = true
	o.observer.mu.Unlock()
	return nil
}
func (o *Operations) removeInstance(ctx context.Context, node serving.NodeReference, id string, proof map[string]native.State) error {
	uid, containerID, ok := strings.Cut(id, "/")
	if !ok || containerID == "" {
		return errors.New("exact physical instance required")
	}
	if err := o.validateOperationBinding(ctx, Operation{Kind: operationRemove, Node: node, Instance: id}); err != nil {
		return err
	}
	if err := o.StopInstance(ctx, id); err != nil {
		return err
	}
	if err := o.wait(ctx, func() (bool, error) { return o.runtime.DetachInstance(id) }); err != nil {
		return err
	}
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	var pod *core.Pod
	for i := range pods.Items {
		if string(pods.Items[i].UID) == uid {
			pod = &pods.Items[i]
		}
	}
	if pod == nil {
		return errors.New("physical source disappeared without retained termination proof")
	}
	status := firstfloorStatus(pod)
	if status == nil || status.ContainerID != containerID {
		return errors.New("physical source changed before removal")
	}
	o.observer.mu.Lock()
	if o.observer.sources[id] == nil {
		o.observer.sources[id] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: containerID, ownerUID: types.UID(workloadOwner(pod.OwnerReferences))}
	}
	o.observer.observeTermination(pod)
	o.observer.mu.Unlock()
	if status.State.Terminated == nil {
		if err = o.wait(ctx, func() (bool, error) {
			endpoint, e := nativeInstanceEndpoint(ctx, o.kube, pod)
			if e != nil {
				return false, e
			}
			state, e := o.native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: "chill_model"})
			if e != nil {
				return false, e
			}
			if len(state.Requests) != 0 {
				return false, nil
			}
			if proof != nil {
				proof[id] = state
			}
			return true, nil
		}); err != nil {
			return err
		}
	}
	if err = patchSourceFinalizer(ctx, o.store.client, pod, true); err != nil {
		return err
	}
	if pod.DeletionTimestamp == nil {
		u := pod.UID
		err = o.store.client.Delete(ctx, pod, &client.DeleteOptions{Preconditions: &meta.Preconditions{UID: &u}})
		if err != nil {
			return err
		}
	}
	return o.finishInstanceRemoval(ctx, pod, id)
}
func (o *Operations) finishInstanceRemoval(ctx context.Context, pod *core.Pod, id string) error {
	return o.wait(ctx, func() (bool, error) {
		current := &core.Pod{}
		e := o.store.client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, current)
		if apierrors.IsNotFound(e) {
			return false, errors.New("physical source lost before termination proof")
		}
		if e != nil {
			return false, e
		}
		if current.UID != pod.UID {
			return false, errors.New("Pod identity changed")
		}
		o.observer.mu.Lock()
		o.observer.observeTermination(current)
		source := o.observer.sources[id]
		stopped := source != nil && source.stopped
		o.observer.mu.Unlock()
		if !stopped || !podTerminated(current) {
			return false, nil
		}
		if e = deleteInstanceService(ctx, o.kube, current); e != nil {
			return false, e
		}
		if e = patchSourceFinalizer(ctx, o.store.client, current, false); e != nil {
			return false, e
		}
		if e = o.wait(ctx, func() (bool, error) {
			_, e := o.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
			return apierrors.IsNotFound(e), eIfNotFound(e)
		}); e != nil {
			return false, e
		}
		cm := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name}}
		if e = o.store.client.Delete(ctx, cm); e != nil && !apierrors.IsNotFound(e) {
			return false, e
		}
		return true, nil
	})
}

// patchSourceFinalizer changes only our primitive-list member. The API server
// rejects a different immutable UID, but unrelated status/RV and other
// finalizer updates do not invalidate this metadata-only delta.
func patchSourceFinalizer(ctx context.Context, c client.Client, pod *core.Pod, present bool) error {
	if pod.UID == "" {
		return errors.New("physical Pod UID required for source finalizer")
	}
	metadata := map[string]any{"uid": pod.UID}
	if present {
		metadata["finalizers"] = []string{sourceFinalizer}
	} else {
		metadata["$deleteFromPrimitiveList/finalizers"] = []string{sourceFinalizer}
	}
	patch, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return err
	}
	return c.Patch(ctx, pod, client.RawPatch(types.StrategicMergePatchType, patch))
}

func eIfNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
func (o *Operations) ChangePower(ctx context.Context, node serving.NodeReference, request *edge.NodeActuation) error {
	if request == nil || request.Spec.Node.Name != node.Name || request.Spec.Node.UID != node.UID {
		return errors.New("exact NodeActuation target required")
	}
	o.observer.mu.Lock()
	o.observer.movingNodes[node.UID] = true
	err := o.observer.detachNodeLocked(ctx, node.UID)
	o.observer.mu.Unlock()
	if err != nil {
		return err
	}
	// Every native instance on this Node is affected; never treat a mode as per UID.
	defer func() { o.observer.mu.Lock(); delete(o.observer.movingNodes, node.UID); o.observer.mu.Unlock() }()
	before, err := o.Capture(ctx, node)
	if err != nil {
		return err
	}
	if len(before.Reasons) > 0 {
		return errors.New("power transition lacks actual loaded source snapshot")
	}
	for _, state := range before.Native {
		if !state.Ready || len(state.Requests) != 0 {
			return errors.New("power transition native source not actually drained/ready")
		}
	}
	copy := request.DeepCopy()
	copy.Namespace = o.config.ActuationNamespace
	if err = o.store.client.Create(ctx, copy); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	err = o.wait(ctx, func() (bool, error) {
		actual := &edge.NodeActuation{}
		if e := o.store.client.Get(ctx, types.NamespacedName{Namespace: copy.Namespace, Name: copy.Name}, actual); e != nil {
			return false, e
		}
		if !reflect.DeepEqual(actual.Spec, copy.Spec) {
			return false, errors.New("existing NodeActuation target differs")
		}
		if actual.Status.Phase == edge.NodeActuationPhaseFailed {
			return false, fmt.Errorf("NodeActuation failed: %s", actual.Status.Message)
		}
		if actual.Status.Phase != edge.NodeActuationPhaseReady {
			return false, nil
		}
		if actual.Status.ObservedGeneration != actual.Generation || actual.Status.TargetObservation == nil || actual.Status.ReadyAt == nil || actual.Status.TargetObservation.PowerMode != copy.Spec.PowerMode || !reflect.DeepEqual(actual.Status.TargetObservation.Actuator, copy.Spec.Actuator) {
			return false, errors.New("independent power readback unconfirmed")
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if err := o.waitPowerState(ctx, node, before); err != nil {
		return err
	}
	o.observer.mu.Lock()
	delete(o.observer.movingNodes, node.UID)
	o.observer.mu.Unlock()
	return o.observer.Refresh(ctx)
}
func (o *Operations) waitPowerState(ctx context.Context, node serving.NodeReference, before OperationState) error {
	if err := o.wait(ctx, func() (bool, error) {
		after, e := o.Capture(ctx, node)
		if e != nil {
			return false, e
		}
		if len(after.Reasons) > 0 {
			return false, nil
		}
		if !slices.Equal(before.Pods, after.Pods) || !slices.Equal(before.States, after.States) || len(before.Native) != len(after.Native) {
			return false, errors.New("power transition changed actual loaded state")
		}
		for id, a := range before.Native {
			b, ok := after.Native[id]
			if !ok {
				return false, errors.New("power transition lost physical native actor")
			}
			if !b.Ready {
				return false, nil
			}
			if len(b.Requests) != 0 || a.AcceptedRequests != b.AcceptedRequests || a.CompletedRequests != b.CompletedRequests || a.Executions != b.Executions || !reflect.DeepEqual(a.Metadata, b.Metadata) {
				return false, errors.New("power transition native condition changed or Serving inference occurred")
			}
		}
		return true, nil
	}); err != nil {
		return err
	}
	return nil
}
func port8080() intstr.IntOrString { return intstr.FromInt32(8080) }

func (o *Operations) ApplyTarget(ctx context.Context, target serving.SchedulerCandidate) error {
	for _, instance := range target.Instances {
		if instance.Profile == nil || instance.Profile.Condition.Firstfloor == nil {
			return errors.New("route target missing measured profile")
		}
		policy := native.Policy{DecisionID: target.ID, ProfileID: instance.Profile.ID,
			DeadlinePolicy: native.EffectiveDeadlinePolicy(instance.Profile.Condition.Firstfloor.DeadlinePolicy),
			MaxBatchSize:   instance.MaxBatch, MaxBatchWaitMS: float64(instance.MaxWait) / float64(time.Millisecond),
			CompletionOverheadMS: float64(instance.CompletionOverhead) / float64(time.Millisecond),
			EnvelopeSource:       instance.CompletionPolicySource()}
		if err := o.ApplyPolicy(ctx, instance.ID, policy); err != nil {
			return err
		}
	}
	if err := o.observer.Refresh(ctx); err != nil {
		return err
	}
	if err := o.runtime.ApplySchedulerTarget(target); err != nil {
		return err
	}
	weights, _ := o.runtime.SchedulerRouting()
	o.observer.mu.Lock()
	for _, route := range weights {
		for id, weight := range route {
			if weight > 0 {
				delete(o.observer.removingInstances, id)
			}
		}
	}
	o.observer.mu.Unlock()
	return nil
}
func (o *Operations) ApplyPolicy(ctx context.Context, instanceID string, policy native.Policy) error {
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	var selected *core.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		status := firstfloorStatus(p)
		if p.Annotations[ServingSlotAnnotation] == instanceID || (status != nil && physicalID(p.UID, status.ContainerID) == instanceID) {
			selected = p
			break
		}
	}
	if selected == nil {
		return errors.New("actual policy target unavailable")
	}
	cm := &core.ConfigMap{}
	if err = o.store.client.Get(ctx, types.NamespacedName{Namespace: selected.Namespace, Name: selected.Name}, cm); err != nil {
		return err
	}
	var config native.Config
	if err = json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
		return err
	}
	if config.Policy.ProfileID != policy.ProfileID {
		return errors.New("policy cannot change immutable profile")
	}
	config.Policy = policy
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	before := cm.DeepCopy()
	cm.Data["config.json"] = string(raw)
	if err = o.store.client.Patch(ctx, cm, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	previous := selected.DeepCopy()
	selected.Annotations[ServingPolicyAnnotation] = policy.DecisionID
	if err = o.store.client.Patch(ctx, selected, client.MergeFromWithOptions(previous, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	return o.wait(ctx, func() (bool, error) {
		current, e := o.kube.CoreV1().Pods(selected.Namespace).Get(ctx, selected.Name, meta.GetOptions{})
		if e != nil {
			return false, e
		}
		if !sameRunningContainer(selected, current) {
			return false, errors.New("policy target physical executor changed")
		}
		endpoint, e := nativeInstanceEndpoint(ctx, o.kube, current)
		if e != nil {
			return false, e
		}
		state, e := o.native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: config.Model})
		if e != nil {
			return false, e
		}
		actual := state.Metadata.Policy
		actual.Batches = nil
		expected := policy
		expected.Batches = nil
		return state.Ready && state.Metadata.ProfileID == policy.ProfileID && state.Metadata.DecisionID == policy.DecisionID && reflect.DeepEqual(actual, expected), nil
	})
}
func nodeClassMatches(node *core.Node, class *edge.DeviceClass) bool {
	if len(class.Spec.NodeSelector) == 0 {
		return false
	}
	for key, value := range class.Spec.NodeSelector {
		if node.Labels[key] != value {
			return false
		}
	}
	return true
}
func samePreparedContainer(actual, desired core.Container) bool {
	return actual.Name == desired.Name && actual.Image == desired.Image && slices.Equal(actual.Command, desired.Command) && slices.Equal(actual.Args, desired.Args) && maps.EqualFunc(actual.Resources.Requests, desired.Resources.Requests, resource.Quantity.Equal) && maps.EqualFunc(actual.Resources.Limits, desired.Resources.Limits, resource.Quantity.Equal) && reflect.DeepEqual(actual.VolumeMounts, desired.VolumeMounts)
}
func (o *Operations) validateOperationBinding(ctx context.Context, operation Operation) error {
	if operation.Kind == operationRoute {
		return nil
	}
	actual, err := o.kube.CoreV1().Nodes().Get(ctx, operation.Node.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	if string(actual.UID) != operation.Node.UID || actual.DeletionTimestamp != nil {
		return errors.New("operation physical Node identity changed")
	}
	if operation.Kind == operationPrepare || operation.Kind == operationResize {
		if operation.Prepare == nil || operation.Prepare.Instance.Node != operation.Node || operation.Prepare.Instance.Pool != actual.Labels[ServingPoolLabel] {
			return errors.New("prepare physical Node/pool differs from operation scope")
		}
	}
	if operation.Kind == operationResize {
		if operation.Target == nil {
			return errors.New("resize routing target unavailable")
		}
		matched := false
		for _, i := range operation.Target.Instances {
			if i.ID == operation.Prepare.Instance.ID && i.Node == operation.Node && i.Pool == operation.Prepare.Instance.Pool {
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
	if !ok || containerID == "" {
		return errors.New("exact operation physical instance required")
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
		if pod.Spec.NodeName != actual.Name || status == nil || status.ContainerID != containerID || pod.Labels["app.kubernetes.io/component"] != servingInstanceComponent {
			return errors.New("operation instance belongs to different physical Node/source")
		}
		for _, pool := range o.runtime.Pools() {
			for _, i := range pool.Instances {
				if i.ID == operation.Instance && (i.Node != operation.Node || i.Pool != actual.Labels[ServingPoolLabel]) {
					return errors.New("operation physical instance pool observation changed")
				}
			}
		}
		return nil
	}
	return errors.New("operation physical instance unavailable")
}
