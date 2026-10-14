package kube

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ServingPoolLabel         = "edge.dacs.io/serving-pool"
	ServingProfileAnnotation = "edge.dacs.io/serving-profile"
	ServingWeightAnnotation  = "edge.dacs.io/serving-weight"
	ServingPolicyAnnotation  = "edge.dacs.io/serving-policy"
	ServingSlotAnnotation    = "edge.dacs.io/serving-slot"
	sourceFinalizer          = "edge.dacs.io/serving-source-termination"
	firstfloorContainerName  = "firstfloor"
)

type ObserverConfig struct {
	Namespace          string
	ActuationNamespace string
}
type ObserverIssue struct {
	Source string    `json:"source"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}
type nativeSource struct {
	pod         types.NamespacedName
	uid         types.UID
	containerID string
	ownerUID    types.UID
	stopped     bool
	cancel      context.CancelFunc
	stream      io.ReadCloser
}

// Observer reads uncached Kubernetes facts and the native server. It never
// prepares capacity, changes power, or turns desired state into readiness.
// ponytail: one source lock serializes refresh/cleanup for this single owner;
// split locks only if observed cluster size makes refresh contention material.
type Observer struct {
	mu                sync.Mutex
	store             *Store
	kube              kubernetes.Interface
	runtime           *serving.Runtime
	native            *firstfloor.Client
	config            ObserverConfig
	profiles          []serving.Profile
	sources           map[string]*nativeSource
	issues            map[string]ObserverIssue
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	podWatch          watch.Interface
	closed            bool
	movingNodes       map[string]bool
	removingInstances map[string]bool
}

func NewObserver(store *Store, kube kubernetes.Interface, runtime *serving.Runtime, native *firstfloor.Client, config ObserverConfig) (*Observer, error) {
	if store == nil || store.client == nil || kube == nil || runtime == nil || native == nil || config.Namespace == "" {
		return nil, errors.New("observer requires direct store, Kubernetes/native clients, runtime and selected namespace")
	}
	if config.ActuationNamespace == "" {
		config.ActuationNamespace = config.Namespace
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Observer{store: store, kube: kube, runtime: runtime, native: native, config: config, sources: map[string]*nativeSource{}, issues: map[string]ObserverIssue{}, movingNodes: map[string]bool{}, removingInstances: map[string]bool{}, ctx: ctx, cancel: cancel}, nil
}

func (o *Observer) SetProfiles(profiles []serving.Profile) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.profiles = slices.Clone(profiles)
	o.runtime.SetProfiles(o.profiles)
}
func (o *Observer) Profiles() []serving.Profile {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.profiles)
}
func (o *Observer) Issues() []ObserverIssue {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]ObserverIssue, 0, len(o.issues))
	for _, issue := range o.issues {
		result = append(result, issue)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Source < result[j].Source })
	return result
}
func (o *Observer) issue(source string, err error) {
	if err == nil {
		delete(o.issues, source)
	} else {
		o.issues[source] = ObserverIssue{Source: source, Reason: err.Error(), At: time.Now().UTC()}
	}
}
func physicalID(podUID types.UID, containerID string) string {
	return string(podUID) + "/" + containerID
}
func firstfloorStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == firstfloorContainerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// Refresh publishes only the current verified set. List/probe failures close
// assignment, but do not claim that any accepted backend request has stopped.
func (o *Observer) Refresh(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refreshLocked(ctx)
}
func (o *Observer) refreshLocked(ctx context.Context) error {
	if o.closed {
		return errors.New("observer is closed")
	}
	nodes, err := o.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return o.refreshFailure(err)
	}
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return o.refreshFailure(err)
	}
	resources, err := o.sourceResources(ctx, pods.Items)
	if err != nil {
		return o.refreshFailure(err)
	}
	var classes edgev1alpha1.DeviceClassList
	if err = o.store.client.List(ctx, &classes); err != nil {
		return o.refreshFailure(err)
	}
	var actuations edgev1alpha1.NodeActuationList
	if err = o.store.client.List(ctx, &actuations, client.InNamespace(o.config.ActuationNamespace)); err != nil {
		return o.refreshFailure(err)
	}
	currentNodes := map[string][]serving.NodeReference{}
	byName := map[string]*corev1.Node{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		byName[node.Name] = node
		if pool := node.Labels[ServingPoolLabel]; pool != "" && node.UID != "" {
			currentNodes[pool] = append(currentNodes[pool], serving.NodeReference{Name: node.Name, UID: string(node.UID)})
		}
	}
	for pool := range currentNodes {
		sort.Slice(currentNodes[pool], func(i, j int) bool { return currentNodes[pool][i].Name < currentNodes[pool][j].Name })
	}
	o.runtime.SetNodes(currentNodes)
	// Start a native status watch before using this snapshot for execution or cleanup.
	if o.podWatch == nil {
		w, e := o.kube.CoreV1().Pods(o.config.Namespace).Watch(o.ctx, metav1.ListOptions{ResourceVersion: pods.ResourceVersion})
		if e != nil {
			return o.refreshFailure(e)
		}
		o.podWatch = w
		o.wg.Add(1)
		go o.watchPods(w)
	}
	profiles := map[string]*serving.Profile{}
	for i := range o.profiles {
		profiles[o.profiles[i].ID] = &o.profiles[i]
	}
	ready := make([]serving.ReadyInstance, 0, len(pods.Items))
	observed := map[types.UID]bool{}
	o.restoreSources(pods.Items)
	for i := range pods.Items {
		pod := &pods.Items[i]
		observed[pod.UID] = true
		instance, publish := o.refreshSource(ctx, pod, byName[pod.Spec.NodeName], profiles[pod.Annotations[ServingProfileAnnotation]], classes.Items, actuations.Items, resources)
		if publish {
			ready = append(ready, instance)
		}
	}
	// Missing/relabeled Pods are still queried for exact old executor termination.
	for id, source := range o.sources {
		if observed[source.uid] {
			continue
		}
		pod, e := o.kube.CoreV1().Pods(source.pod.Namespace).Get(ctx, source.pod.Name, metav1.GetOptions{})
		if e == nil && pod.UID == source.uid {
			o.observeTermination(pod)
		} else if !source.stopped {
			o.issue(id, errors.New("source disappeared without confirmed container termination"))
		}
	}
	if err = o.runtime.InstallInstances(ready); err != nil {
		return err
	}
	o.issue("refresh", nil)
	return nil
}

func (o *Observer) restoreSources(pods []corev1.Pod) {
	// Restored accepted assignments identify exact physical sources, not routes.
	// Join all namespace Pods by UID so relabeling cannot hide drain evidence.
	for _, pool := range o.runtime.Pools() {
		for _, instance := range pool.Instances {
			outstanding := 0
			for _, counts := range []map[string]int{instance.AssignedUnknown, instance.Waiting, instance.Running} {
				for _, count := range counts {
					outstanding += count
				}
			}
			if outstanding == 0 || o.sources[instance.ID] != nil {
				continue
			}
			podUID, containerID, ok := strings.Cut(instance.ID, "/")
			if !ok {
				continue
			}
			for i := range pods {
				pod := &pods[i]
				if string(pod.UID) == podUID {
					o.sources[instance.ID] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: containerID, ownerUID: types.UID(workloadOwner(pod.OwnerReferences))}
					break
				}
			}
		}
	}
}

func (o *Observer) refreshSource(ctx context.Context, pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass, actuations []edgev1alpha1.NodeActuation, resources map[types.UID]client.Object) (serving.ReadyInstance, bool) {
	o.observeTermination(pod)
	owner, ownershipErr := resourceWorkloadOwner(pod, resources)
	for _, source := range o.sources {
		if source.uid == pod.UID && ownershipErr == nil {
			source.ownerUID = types.UID(owner)
		}
	}
	if status := firstfloorStatus(pod); status != nil && status.State.Running != nil && status.ContainerID != "" {
		id := physicalID(pod.UID, status.ContainerID)
		if retained := o.sources[id]; retained != nil && !retained.stopped {
			o.issue("logs/"+id, o.startLogs(ctx, pod, owner))
		}
	}
	if pod.Labels["app.kubernetes.io/component"] != servingInstanceComponent {
		return serving.ReadyInstance{}, false
	}
	endpoint, err := nativeInstanceEndpoint(ctx, o.kube, pod)
	var spec firstfloor.Spec
	if err == nil {
		spec, err = sourceSpec(pod, node, profile, classes, actuations, endpoint)
	}
	if err == nil {
		err = ownershipErr
	}
	if node != nil && o.movingNodes[string(node.UID)] {
		err = errors.New("NodePoolTransition")
	}
	var instance serving.ReadyInstance
	if err == nil {
		instance, err = o.native.Probe(ctx, spec)
		if err == nil {
			// A restart between snapshot and HTTP readiness must not alias executors.
			latest, getErr := o.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if getErr != nil {
				err = getErr
			} else if !sameRunningContainer(pod, latest) {
				err = errors.New("native container changed during probe")
			} else {
				instance.OwnerWorkloadUID = owner
				instance.State.TargetID = pod.Annotations[ServingSlotAnnotation]
				instance.State.Stopped = o.removingInstances[spec.InstanceID]
				err = o.startLogs(ctx, latest, owner)
			}
		}
	}
	o.issue(pod.Namespace+"/"+pod.Name, err)
	if err == nil {
		return instance, true
	}
	if status := firstfloorStatus(pod); status != nil && status.ContainerID != "" && node != nil && node.Labels[ServingPoolLabel] != "" {
		return serving.ReadyInstance{State: serving.InstanceState{ID: physicalID(pod.UID, status.ContainerID), Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: node.Labels[ServingPoolLabel], Profile: pod.Annotations[ServingProfileAnnotation], Reason: err.Error(), ObservedAt: time.Now().UTC()}}, true
	}
	return serving.ReadyInstance{}, false
}
func (o *Observer) refreshFailure(err error) error {
	o.issue("refresh", err)
	o.runtime.SetNodes(nil)
	_ = o.runtime.InstallInstances(nil)
	return err
}
func sameNativeContainer(before, after *corev1.Pod) bool {
	a, b := firstfloorStatus(before), firstfloorStatus(after)
	return before.UID == after.UID && a != nil && b != nil && a.ContainerID != "" && a.ContainerID == b.ContainerID && b.State.Running != nil
}
func sameRunningContainer(before, after *corev1.Pod) bool {
	b := firstfloorStatus(after)
	return sameNativeContainer(before, after) && b.Ready && after.DeletionTimestamp == nil && podReady(after)
}
func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
func workloadOwner(owners []metav1.OwnerReference) string {
	if len(owners) == 1 && owners[0].APIVersion == edgev1alpha1.GroupVersion.String() && owners[0].Kind == "Workload" {
		return string(owners[0].UID)
	}
	return ""
}

func sourceSpec(pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass, actuations []edgev1alpha1.NodeActuation, endpoint string) (firstfloor.Spec, error) {
	var empty firstfloor.Spec
	if err := validateSourcePlacement(pod, node, profile, classes, actuations); err != nil {
		return empty, err
	}
	condition := &profile.Condition
	weight, e := strconv.ParseInt(pod.Annotations[ServingWeightAnnotation], 10, 32)
	if e != nil || weight < 1 {
		return empty, errors.New("explicit positive serving weight required")
	}
	if err := validateSourceContainers(pod, condition); err != nil {
		return empty, err
	}
	runtime := &pod.Spec.Containers[0]
	if err := validateSourceResources(runtime, node, profile); err != nil {
		return empty, err
	}
	status := firstfloorStatus(pod)
	expected := *profile.RuntimeMetadata
	// Delivery changes only the verified artifact's mount location, not the
	// measured runtime, model bytes, provider, device or execution allocation.
	expected.ModelPath = path.Join("/model", "model"+path.Ext(condition.Artifact.Path))
	return firstfloor.Spec{Endpoint: endpoint, ModelName: "chill_model", InstanceID: physicalID(pod.UID, status.ContainerID), Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: node.Labels[ServingPoolLabel], Profile: profile, Weight: int32(weight), Capacity: int(condition.Firstfloor.MaxQueueSize), ExpectedRuntime: &expected, DecisionID: pod.Annotations[ServingPolicyAnnotation]}, nil
}

func validateSourcePlacement(pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass, actuations []edgev1alpha1.NodeActuation) error {
	if pod.UID == "" || node == nil || node.UID == "" || node.DeletionTimestamp != nil || node.Labels[ServingPoolLabel] == "" {
		return errors.New("current Node UID and serving-pool membership unavailable")
	}
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !podReady(pod) {
		return errors.New("Pod is not currently ready")
	}
	nodeReady := false
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			nodeReady = c.Status == corev1.ConditionTrue
		}
	}
	if !nodeReady {
		return errors.New("Node readiness unconfirmed")
	}
	if profile == nil {
		return errors.New("selected catalog profile unavailable")
	}
	condition := &profile.Condition
	if profile.NodeName != node.Name || condition.NodeUID != string(node.UID) || condition.RuntimeFamily != profilingrun.FirstfloorRuntimeFamily || condition.Firstfloor == nil {
		return errors.New("profile does not match current physical Node/native execution")
	}
	var class *edgev1alpha1.DeviceClass
	for i := range classes {
		if classes[i].Name == condition.DeviceClass {
			class = &classes[i]
			break
		}
	}
	if class == nil || len(class.Spec.NodeSelector) == 0 || !labels.SelectorFromSet(class.Spec.NodeSelector).Matches(labels.Set(node.Labels)) || class.Spec.Architecture != node.Status.NodeInfo.Architecture || node.Labels[corev1.LabelArchStable] != class.Spec.Architecture {
		return errors.New("current device class/architecture mismatch")
	}
	return confirmedPower(node, class, condition.PowerMode, actuations)
}

func validateSourceContainers(pod *corev1.Pod, condition *profilederivation.CurvePlanSpec) error {
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 2 || len(pod.Spec.EphemeralContainers) != 0 || pod.Spec.HostNetwork {
		return errors.New("native source requires isolated firstfloor runner, artifact and immutable profile init containers")
	}
	runtime, artifact := pod.Spec.Containers[0], pod.Spec.InitContainers[0]
	status := firstfloorStatus(pod)
	if runtime.Name != firstfloorContainerName || artifact.Name != "artifact" || status == nil || status.State.Running == nil || !status.Ready || status.ContainerID == "" || !observedImage(runtime.Image, status.ImageID, condition.RuntimeImage) {
		return errors.New("actual immutable native runtime image/container unavailable")
	}
	var init *corev1.ContainerStatus
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == "artifact" {
			init = &pod.Status.InitContainerStatuses[i]
		}
	}
	if init == nil || init.State.Terminated == nil || init.State.Terminated.ExitCode != 0 || !observedImage(artifact.Image, init.ImageID, condition.Artifact.Image) {
		return errors.New("immutable artifact preparation unconfirmed")
	}
	if !slices.Equal(artifact.Command, []string{"sh", "-ec"}) || !slices.Equal(artifact.Args, []string{`cp "$1" "$2"`, "artifact-copy", condition.Artifact.Path, path.Join("/chill-artifact", "model"+path.Ext(condition.Artifact.Path))}) || !artifactProjection(pod, artifact, runtime, condition.Artifact.Path) {
		return errors.New("artifact must use the verified read-only shared-volume projection")
	}
	bundle := pod.Spec.InitContainers[1]
	var bundleStatus *corev1.ContainerStatus
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == "profile" {
			bundleStatus = &pod.Status.InitContainerStatuses[i]
		}
	}
	if bundle.Name != "profile" || bundle.Image != condition.RuntimeImage || bundleStatus == nil || bundleStatus.State.Terminated == nil || bundleStatus.State.Terminated.ExitCode != 0 || !observedImage(bundle.Image, bundleStatus.ImageID, condition.RuntimeImage) {
		return errors.New("immutable profile materialization unconfirmed")
	}
	return nil
}

func validateSourceResources(runtime *corev1.Container, node *corev1.Node, profile *serving.Profile) error {
	condition := &profile.Condition
	request, requestErr := resource.ParseQuantity(condition.CPU.Request)
	limit, limitErr := resource.ParseQuantity(condition.CPU.Limit)
	actualRequest, actualLimit := runtime.Resources.Requests[corev1.ResourceCPU], runtime.Resources.Limits[corev1.ResourceCPU]
	if requestErr != nil || limitErr != nil || request.Cmp(actualRequest) != 0 || limit.Cmp(actualLimit) != 0 || condition.CPU.ExclusivityClaimed {
		return errors.New("actual CPU request/limit does not match measured quota condition")
	}
	memoryLimit := runtime.Resources.Limits[corev1.ResourceMemory]
	memoryRequest := runtime.Resources.Requests[corev1.ResourceMemory]
	nodeMemory := node.Status.Allocatable[corev1.ResourceMemory]
	for _, batch := range profile.Batches {
		if batch.MemoryBytes == 0 || memoryLimit.Sign() <= 0 || memoryRequest.Sign() <= 0 || uint64(memoryLimit.Value()) < batch.MemoryBytes || uint64(memoryRequest.Value()) < batch.MemoryBytes || nodeMemory.Sign() <= 0 || uint64(nodeMemory.Value()) < batch.MemoryBytes {
			return errors.New("measured batch memory exceeds actual current memory domain")
		}
	}
	if profile.RuntimeMetadata == nil {
		return errors.New("verified native device evidence unavailable")
	}
	gpuRequest, gpuLimit := runtime.Resources.Requests["nvidia.com/gpu"], runtime.Resources.Limits["nvidia.com/gpu"]
	if profile.RuntimeMetadata.GPUUUID != "" && (gpuRequest.Value() != 1 || gpuLimit.Value() != 1) {
		return errors.New("actual whole GPU exclusive allocation unavailable")
	}
	return nil
}
func observedImage(requested, actual, frozen string) bool { return requested == frozen && actual != "" }
func artifactProjection(pod *corev1.Pod, artifact, runtime corev1.Container, filename string) bool {
	if path.Dir(filename) != "/artifact" {
		return false
	}
	for _, a := range artifact.VolumeMounts {
		for _, r := range runtime.VolumeMounts {
			if a.Name == r.Name && a.MountPath == "/chill-artifact" && r.MountPath == "/model" && !a.ReadOnly && r.ReadOnly && a.SubPath == "" && r.SubPath == "" && a.SubPathExpr == "" && r.SubPathExpr == "" {
				for _, volume := range pod.Spec.Volumes {
					if volume.Name == a.Name && volume.EmptyDir != nil {
						return true
					}
				}
			}
		}
	}
	return false
}

// Fixed is an immutable non-actuated class fact. Mutable state comes only from
// the latest NodeActuator handoff's independent read-back, never its spec/labels.
func confirmedPower(node *corev1.Node, class *edgev1alpha1.DeviceClass, name string, records []edgev1alpha1.NodeActuation) error {
	mode, err := deviceclass.FindPowerMode(class.Spec.PowerModes, name)
	if err != nil {
		return err
	}
	var latest *edgev1alpha1.NodeActuation
	for i := range records {
		r := &records[i]
		if r.Spec.Node.Name == node.Name && r.Spec.Node.UID == string(node.UID) {
			if latest != nil && r.Spec.StageRequestedAt.Equal(&latest.Spec.StageRequestedAt) && (r.UID != latest.UID || r.Name != latest.Name) {
				return errors.New("current power handoff ordering unconfirmed")
			}
			if latest == nil || r.Spec.StageRequestedAt.After(latest.Spec.StageRequestedAt.Time) {
				latest = r
			}
		}
	}
	if mode.Actuator.Kind == edgev1alpha1.PowerModeActuatorFixed {
		if latest == nil {
			return nil
		}
		return errors.New("Fixed class has conflicting mutable actuation evidence")
	}
	if latest == nil || latest.Spec.DeviceClass != class.Name || latest.Status.ObservedGeneration != latest.Generation || latest.Status.Phase != edgev1alpha1.NodeActuationPhaseReady || latest.Status.VerifiedAt == nil || latest.Status.ReadyAt == nil || latest.Status.TargetObservation == nil {
		return errors.New("current power state unconfirmed by NodeActuator")
	}
	observation := latest.Status.TargetObservation
	if observation.ObservedAt.IsZero() || observation.PowerMode != name || !reflect.DeepEqual(observation.Actuator, mode.Actuator) || !reflect.DeepEqual(observation.Actuator, latest.Spec.Actuator) {
		return errors.New("NodeActuator actual power read-back differs from measured condition")
	}
	return nil
}

func (o *Observer) observeTermination(pod *corev1.Pod) {
	status := firstfloorStatus(pod)
	if status == nil {
		return
	}
	for _, terminal := range []*corev1.ContainerStateTerminated{status.State.Terminated, status.LastTerminationState.Terminated} {
		if terminal == nil || terminal.ContainerID == "" {
			continue
		}
		id := physicalID(pod.UID, terminal.ContainerID)
		if source := o.sources[id]; source != nil {
			source.stopped = true
			if source.cancel != nil {
				source.cancel()
				source.cancel = nil
			}
		}
		if err := o.runtime.ConfirmInstanceStopped(id); err != nil {
			o.issue(id, err)
		}
	}
}
func (o *Observer) watchPods(w watch.Interface) {
	defer o.wg.Done()
	for {
		select {
		case <-o.ctx.Done():
			return
		case event, ok := <-w.ResultChan():
			if !ok {
				o.mu.Lock()
				if o.podWatch == w {
					o.podWatch = nil
					o.issue("pod-watch", errors.New("native Pod status watch ended; refresh required"))
				}
				o.mu.Unlock()
				return
			}
			o.mu.Lock()
			if pod, ok := event.Object.(*corev1.Pod); ok {
				o.observeTermination(pod)
				for id, source := range o.sources {
					if source.uid != pod.UID {
						continue
					}
					status := firstfloorStatus(pod)
					changed := event.Type == watch.Deleted || status == nil || status.ContainerID != source.containerID || status.State.Running == nil
					if changed || pod.DeletionTimestamp != nil || !status.Ready || !podReady(pod) {
						o.runtime.DisableInstance(id, "native Pod readiness/identity unavailable")
					}
					if changed && source.cancel != nil {
						source.cancel()
						source.cancel = nil
					}
				}
			} else if event.Type == watch.Error {
				o.issue("pod-watch", errors.New("native Pod status watch error"))
				_ = o.runtime.InstallInstances(nil)
			}
			o.mu.Unlock()
		}
	}
}
func (o *Observer) startLogs(ctx context.Context, pod *corev1.Pod, owner string) error {
	status := firstfloorStatus(pod)
	id := physicalID(pod.UID, status.ContainerID)
	source := o.sources[id]
	if source != nil && source.stopped {
		return errors.New("terminated physical executor cannot become current")
	}
	if source != nil && owner != "" {
		source.ownerUID = types.UID(owner)
	}
	if source != nil && source.cancel != nil {
		return nil
	}
	logCtx, cancel := context.WithCancel(o.ctx)
	stream, err := o.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: firstfloorContainerName, Follow: true, Timestamps: true}).Stream(logCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("native progress source unavailable: %w", err)
	}
	latest, err := o.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil || !sameNativeContainer(pod, latest) {
		cancel()
		return errors.Join(errors.New("native container changed while opening exact progress source"), stream.Close())
	}
	if source == nil {
		source = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: status.ContainerID, ownerUID: types.UID(owner)}
		o.sources[id] = source
	}
	source.cancel = cancel
	source.stream = stream
	o.wg.Add(1)
	go o.scanLogs(logCtx, id, stream)
	return nil
}
func (o *Observer) scanLogs(ctx context.Context, id string, stream io.ReadCloser) {
	defer o.wg.Done()
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 8192), 64<<10)
	for scanner.Scan() {
		progress, err := firstfloor.ParseProgress(scanner.Bytes())
		if errors.Is(err, firstfloor.ErrNotProgress) {
			continue
		}
		o.mu.Lock()
		if err == nil {
			err = o.runtime.ObserveBackend(id, progress)
		}
		o.issue("logs/"+id, err)
		o.mu.Unlock()
	}
	streamErr := errors.Join(scanner.Err(), stream.Close())
	o.mu.Lock()
	defer o.mu.Unlock()
	if source := o.sources[id]; source != nil && source.stream == stream {
		source.cancel = nil
		source.stream = nil
		if ctx.Err() == nil {
			err := streamErr
			if err == nil {
				err = errors.New("native progress stream ended")
			}
			o.issue("logs/"+id, err)
			o.runtime.DisableInstance(id, err.Error())
		}
	}
}

// CleanupWorkload walks native owner references, never labels. Its Pod finalizer
// retains kubelet termination evidence until observed; disappearance alone is
// not enough to complete cleanup for an executor previously seen running.
func (o *Observer) CleanupWorkload(ctx context.Context, uid types.UID) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return false, errors.New("observer is closed")
	}
	if uid == "" {
		return false, errors.New("Workload UID required")
	}
	if !o.runtime.Drained(string(uid)) {
		return false, nil
	}
	objects, err := o.ownedResources(ctx, uid)
	if err != nil {
		return false, err
	}
	if err := o.bindCleanupSources(uid, objects); err != nil {
		return false, err
	}
	// Hold every child Pod before deleting any controller; foreground GC cannot
	// race away actual termination status. No force deletion is used.
	for _, object := range objects {
		pod, ok := object.(*corev1.Pod)
		if !ok {
			continue
		}
		o.observeTermination(pod)
		if !slices.Contains(pod.Finalizers, sourceFinalizer) {
			if pod.DeletionTimestamp != nil && !podTerminated(pod) {
				return false, fmt.Errorf("Pod %s termination unconfirmed before deletion", pod.Name)
			}
			if pod.DeletionTimestamp == nil {
				before := pod.DeepCopy()
				pod.Finalizers = append(pod.Finalizers, sourceFinalizer)
				if err = o.store.client.Patch(ctx, pod, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
					return false, err
				}
			}
		}
	}
	for _, object := range objects {
		if pod, ok := object.(*corev1.Pod); ok && podTerminated(pod) && slices.Contains(pod.Finalizers, sourceFinalizer) {
			if err = deleteInstanceService(ctx, o.kube, pod); err != nil {
				return false, err
			}
			before := pod.DeepCopy()
			pod.Finalizers = slices.DeleteFunc(pod.Finalizers, func(f string) bool { return f == sourceFinalizer })
			if err = o.store.client.Patch(ctx, pod, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
				return false, err
			}
		}
		if replicaSet, ok := object.(*appsv1.ReplicaSet); ok && workloadOwner(replicaSet.OwnerReferences) == "" {
			continue // Deployment foreground GC owns its actual RS chain.
		}
		if pod, ok := object.(*corev1.Pod); ok && workloadOwner(pod.OwnerReferences) == "" {
			continue
		}
		if object.GetDeletionTimestamp() == nil {
			u, rv := object.GetUID(), object.GetResourceVersion()
			policy := metav1.DeletePropagationForeground
			err = o.store.client.Delete(ctx, object, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &u, ResourceVersion: &rv}, PropagationPolicy: &policy})
			if err != nil && !apierrors.IsNotFound(err) {
				return false, err
			}
		}
	}
	if len(objects) > 0 {
		return false, nil
	}
	for id, source := range o.sources {
		if source.ownerUID == uid && !source.stopped {
			return false, fmt.Errorf("physical executor %s termination unconfirmed", id)
		}
	}
	return true, nil
}

func (o *Observer) bindCleanupSources(uid types.UID, objects []client.Object) error {
	for _, object := range objects {
		pod, ok := object.(*corev1.Pod)
		if !ok {
			continue
		}
		for _, pool := range o.runtime.Pools() {
			for _, instance := range pool.Instances {
				if strings.HasPrefix(instance.ID, string(pod.UID)+"/") {
					for _, workload := range instance.Workloads {
						if workload.UID != string(uid) {
							return errors.New("Workload-owned resource is still shared by another registration")
						}
					}
				}
			}
		}
		for _, source := range o.sources {
			if source.uid == pod.UID {
				source.ownerUID = uid
			}
		}
	}
	return nil
}
func podTerminated(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	// A failed init may leave main containers waiting without ever creating an
	// executor. A nonempty running/unknown container identity is never stopped.
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for _, status := range statuses {
			if status.State.Running != nil || status.State.Terminated == nil && (status.ContainerID != "" || status.State.Waiting == nil) {
				return false
			}
		}
	}
	return true
}
func (o *Observer) sourceResources(ctx context.Context, pods []corev1.Pod) (map[types.UID]client.Object, error) {
	all := make(map[types.UID]client.Object, len(pods))
	for i := range pods {
		all[pods[i].UID] = &pods[i]
	}
	for _, list := range []client.ObjectList{&appsv1.DeploymentList{}, &appsv1.ReplicaSetList{}} {
		if err := o.store.client.List(ctx, list, client.InNamespace(o.config.Namespace)); err != nil {
			return nil, err
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			object := item.(client.Object)
			all[object.GetUID()] = object
		}
	}
	return all, nil
}

func resourceWorkloadOwner(object client.Object, all map[types.UID]client.Object) (string, error) {
	// The implemented source chain is Pod -> ReplicaSet -> Deployment -> Workload.
	// ponytail: foreground GC requires blocking links; nonblocking chains need explicit child cleanup.
	blocking := true
	for range 3 {
		refs := object.GetOwnerReferences()
		if len(refs) == 0 {
			return "", nil
		}
		if owner := workloadOwner(refs); owner != "" {
			if !blocking {
				return owner, errors.New("nonblocking serving controller ownership cannot prove foreground cleanup")
			}
			return owner, nil
		}
		if len(refs) != 1 || refs[0].APIVersion != appsv1.SchemeGroupVersion.String() || (refs[0].Kind != "ReplicaSet" && refs[0].Kind != "Deployment") {
			return "", errors.New("unsupported serving source ownership")
		}
		blocking = blocking && refs[0].BlockOwnerDeletion != nil && *refs[0].BlockOwnerDeletion
		object = all[refs[0].UID]
		if object == nil {
			return "", errors.New("serving source owner is unavailable")
		}
	}
	return "", errors.New("serving source ownership exceeds the implemented chain")
}

func (o *Observer) ownedResources(ctx context.Context, uid types.UID) ([]client.Object, error) {
	var pods corev1.PodList
	if err := o.store.client.List(ctx, &pods, client.InNamespace(o.config.Namespace)); err != nil {
		return nil, err
	}
	all, err := o.sourceResources(ctx, pods.Items)
	if err != nil {
		return nil, err
	}
	result := []client.Object{}
	for _, object := range all {
		owner, err := resourceWorkloadOwner(object, all)
		if err != nil && owner == string(uid) {
			return nil, err
		}
		if err == nil && owner == string(uid) {
			result = append(result, object)
		}
	}
	return result, nil
}
func (o *Observer) Close() error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.cancel()
		if o.podWatch != nil {
			o.podWatch.Stop()
		}
		for _, source := range o.sources {
			if source.cancel != nil {
				source.cancel()
			}
		}
	}
	o.mu.Unlock()
	o.wg.Wait()
	return nil
}

// MoveNodePool is the sole Node membership writer. It observes actual drain,
// removes idle old bindings, then changes only the native membership label.
// It neither selects a destination nor retries/resumes a failed operation.
func (o *Observer) MoveNodePool(ctx context.Context, reference serving.NodeReference, pool string) error {
	o.mu.Lock()
	locked := true
	defer func() {
		if locked {
			o.mu.Unlock()
		}
	}()
	if o.closed {
		return errors.New("observer is closed")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if reference.Name == "" || reference.UID == "" || len(validation.IsValidLabelValue(pool)) != 0 {
		return errors.New("exact Node identity and valid destination Pool are required")
	}
	if o.movingNodes[reference.UID] {
		return errors.New("Node Pool transition already in progress")
	}
	node, err := o.kube.CoreV1().Nodes().Get(ctx, reference.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(node.UID) != reference.UID || node.DeletionTimestamp != nil {
		return errors.New("Node identity is no longer current")
	}
	previous := node.Labels[ServingPoolLabel]
	if previous == pool {
		return o.refreshLocked(ctx)
	}
	o.movingNodes[reference.UID] = true
	defer func() {
		if !locked {
			o.mu.Lock()
			locked = true
		}
		delete(o.movingNodes, reference.UID)
	}()
	if err := o.detachNodeLocked(ctx, reference.UID); err != nil {
		return err
	}
	o.mu.Unlock()
	locked = false
	// Re-read after drain, then use native resourceVersion conflict protection.
	// External mutation is a failure, not a reason to silently retry the patch.
	node, err = o.kube.CoreV1().Nodes().Get(ctx, reference.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(node.UID) != reference.UID || node.DeletionTimestamp != nil || node.Labels[ServingPoolLabel] != previous {
		return errors.New("Node membership changed during drain")
	}
	labels := map[string]any{ServingPoolLabel: pool}
	if pool == "" {
		labels[ServingPoolLabel] = nil
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": node.UID, "resourceVersion": node.ResourceVersion, "labels": labels}})
	if err != nil {
		return err
	}
	if _, err = o.kube.CoreV1().Nodes().Patch(ctx, node.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return err
	}
	current, err := o.kube.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(current.UID) != reference.UID || current.Labels[ServingPoolLabel] != pool {
		return errors.New("new native Node membership remains unconfirmed")
	}
	o.mu.Lock()
	locked = true
	delete(o.movingNodes, reference.UID)
	return o.refreshLocked(ctx)
}

// detachNodeLocked returns with the source lock held, releasing it only while
// waiting for actual outstanding calls so native termination can be observed.
func (o *Observer) detachNodeLocked(ctx context.Context, uid string) error {
	for _, state := range o.runtime.Pools() {
		for _, instance := range state.Instances {
			if instance.Node.UID == uid {
				o.runtime.DisableInstance(instance.ID, "NodePoolTransition")
			}
		}
	}
	// The shared Changed channel is only a wake-up hint. Polling here re-reads
	// actual outstanding calls; elapsed time never establishes drain.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		done, err := o.runtime.DetachNode(uid)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		o.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-o.ctx.Done():
		case <-o.runtime.Changed():
		case <-ticker.C:
		}
		o.mu.Lock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if o.closed {
			return errors.New("observer closed during Node Pool transition")
		}
	}
}
