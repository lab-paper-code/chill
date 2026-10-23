package kube

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
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
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	ServingProfileAnnotation = "edge.dacs.io/serving-profile"
	ServingWeightAnnotation  = "edge.dacs.io/serving-weight"
	// ServingRoutingAnnotation holds the last applied weight per Workload UID.
	ServingRoutingAnnotation = "edge.dacs.io/serving-routing"
	ServingPolicyAnnotation  = "edge.dacs.io/serving-policy"
	ServingSlotAnnotation    = "edge.dacs.io/serving-slot"
	sourceFinalizer          = "edge.dacs.io/serving-source-termination"
	firstfloorContainerName  = "firstfloor"
	profileInitContainerName = "profile"
	nonTerminalPodSelector   = "status.phase!=Succeeded,status.phase!=Failed"
)

type ObserverConfig struct {
	Namespace string
}
type ObserverIssue struct {
	Source string    `json:"source"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// processStart bounds log follows so a restarted ChillServer skips progress
// that an earlier process already recorded.
var processStart = time.Now()

type nativeSource struct {
	pod         types.NamespacedName
	uid         types.UID
	containerID string
	ownerUID    types.UID
	stopped     bool
	// follow identifies the current progress log follow.
	follow context.Context
	cancel context.CancelFunc
}

func (s *nativeSource) stopFollow() {
	if s.cancel != nil {
		s.cancel()
	}
	s.follow, s.cancel = nil, nil
}

// Observer reads uncached Kubernetes facts and the native server. It never
// prepares capacity, changes power, or turns desired state into readiness.
// ponytail: one source lock serializes refresh/cleanup for this single owner;
// split locks only if observed cluster size makes refresh contention material.
type Observer struct {
	mu       sync.Mutex
	store    *Store
	kube     kubernetes.Interface
	runtime  *serving.Runtime
	native   *firstfloor.Client
	config   ObserverConfig
	profiles []serving.Profile
	sources  map[string]*nativeSource
	// issueMu guards issues, so log followers record progress and its issues
	// while a refresh holds mu.
	issueMu           sync.Mutex
	issues            map[string]ObserverIssue
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	podWatch          watch.Interface
	closed            bool
	movingNodes       map[string]bool
	removingInstances map[string]bool
	// refreshed is when the last successful refresh finished.
	refreshed time.Time
	// verified holds the instances the last refresh probed Ready; a failed
	// Kubernetes read republishes them once. The Pod watch and log follow
	// remove an instance whose executor changes.
	verified map[string]serving.ReadyInstance
}

func NewObserver(store *Store, kube kubernetes.Interface, runtime *serving.Runtime, native *firstfloor.Client, config ObserverConfig) (*Observer, error) {
	if store == nil || store.client == nil || kube == nil || runtime == nil || native == nil || config.Namespace == "" {
		return nil, errors.New("observer requires direct store, Kubernetes/native clients, runtime and selected namespace")
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
	o.issueMu.Lock()
	defer o.issueMu.Unlock()
	result := make([]ObserverIssue, 0, len(o.issues))
	for _, issue := range o.issues {
		result = append(result, issue)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Source < result[j].Source })
	return result
}
func (o *Observer) issue(source string, err error) {
	o.issueMu.Lock()
	defer o.issueMu.Unlock()
	if err == nil {
		delete(o.issues, source)
		return
	}
	// Each new reason is logged once; the refresh caller logs its own failure.
	if previous, seen := o.issues[source]; source != "refresh" && (!seen || previous.Reason != err.Error()) {
		ctrllog.Log.WithName("observer").Error(err, "native source issue", "source", source)
	}
	o.issues[source] = ObserverIssue{Source: source, Reason: err.Error(), At: time.Now().UTC()}
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

// Refresh publishes only the current verified set. Node or Pod list and probe
// failures close assignment, but do not claim that any accepted backend request
// has stopped. A failed Service or post-probe Pod read keeps the last probed
// readiness of a running executor for that one refresh.
func (o *Observer) Refresh(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refreshLocked(ctx)
}

// RefreshWithin reuses a successful refresh that finished less than age ago
// and refreshes otherwise; pollers share the source refresh this way, also
// one that ran while they waited for the lock.
func (o *Observer) RefreshWithin(ctx context.Context, age time.Duration) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if time.Since(o.refreshed) < age {
		return nil
	}
	return o.refreshLocked(ctx)
}
func (o *Observer) refreshLocked(ctx context.Context) error {
	if o.closed {
		return errors.New("observer is closed")
	}
	started := time.Now()
	nodes, err := o.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return o.refreshFailure(err)
	}
	podOptions := metav1.ListOptions{}
	if o.podWatch != nil {
		// Restore every old executor on startup/watch restart. Afterwards the
		// retained-source GET below still proves exact terminal containers.
		podOptions.FieldSelector = nonTerminalPodSelector
	}
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, podOptions)
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
	measurement := []serving.NodeReference{}
	byName := map[string]*corev1.Node{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		byName[node.Name] = node
		if profilingrun.InMeasurement(node) && node.UID != "" {
			measurement = append(measurement, serving.NodeReference{Name: node.Name, UID: string(node.UID)})
		}
	}
	sort.Slice(measurement, func(i, j int) bool { return measurement[i].Name < measurement[j].Name })
	o.runtime.SetMeasurementNodes(measurement)
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
	// One Service list and one Pod list after the probes verify every
	// instance, so the refresh cost does not grow with the instance count.
	services, servicesErr := o.kube.CoreV1().Services(o.config.Namespace).List(ctx, metav1.ListOptions{})
	servicesByName := map[string]*corev1.Service{}
	if servicesErr == nil {
		for i := range services.Items {
			servicesByName[services.Items[i].Name] = &services.Items[i]
		}
	}
	ready := make([]serving.ReadyInstance, 0, len(pods.Items))
	var probed []probedSource
	observed := map[types.UID]bool{}
	o.restoreSources(pods.Items)
	for i := range pods.Items {
		pod := &pods.Items[i]
		observed[pod.UID] = true
		instance, publish, probe := o.probeSource(ctx, pod, byName[pod.Spec.NodeName], profiles[pod.Annotations[ServingProfileAnnotation]], classes.Items, resources, servicesByName, servicesErr)
		if probe != nil {
			probed = append(probed, *probe)
		} else if publish {
			ready = append(ready, instance)
		}
	}
	ready = append(ready, o.verifyProbed(ctx, probed, observed)...)
	// A kept readiness is not verified again, so it outlives one refresh only.
	o.verified = map[string]serving.ReadyInstance{}
	for _, instance := range ready {
		if instance.State.Ready && !instance.State.ObservedAt.Before(started) {
			o.verified[instance.State.ID] = instance
		}
	}
	if err = o.runtime.InstallInstances(ready); err != nil {
		return err
	}
	o.issue("refresh", nil)
	o.refreshed = time.Now()
	return nil
}

// verifyProbed reads the namespace Pods once behind the probes. Unfiltered: a
// terminal Pod still selected by a Service, and the termination of a missing
// retained source, are both in it.
func (o *Observer) verifyProbed(ctx context.Context, probed []probedSource, observed map[types.UID]bool) []serving.ReadyInstance {
	missing := false
	for _, source := range o.sources {
		missing = missing || !observed[source.uid]
	}
	var latest *corev1.PodList
	var latestErr error
	byName := map[string]*corev1.Pod{}
	if len(probed) > 0 || missing {
		latest, latestErr = o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, metav1.ListOptions{})
		for i := 0; latestErr == nil && i < len(latest.Items); i++ {
			byName[latest.Items[i].Name] = &latest.Items[i]
		}
	}
	ready := make([]serving.ReadyInstance, 0, len(probed))
	for _, p := range probed {
		if instance, publish := o.verifySource(p, latest, latestErr); publish {
			ready = append(ready, instance)
		}
	}
	// Missing/relabeled Pods are still read for exact old executor termination.
	for id, source := range o.sources {
		if observed[source.uid] {
			continue
		}
		pod := byName[source.pod.Name]
		if pod != nil && pod.UID == source.uid {
			o.observeTermination(pod)
		} else if !source.stopped {
			o.issue(id, errors.New("source disappeared without confirmed container termination"))
		}
	}
	return ready
}

// probedSource is an instance whose native probe succeeded; it is published
// after the Pod list taken behind the probe confirms the same executor.
type probedSource struct {
	pod      *corev1.Pod
	node     *corev1.Node
	service  *corev1.Service
	owner    string
	instance serving.ReadyInstance
}

func (o *Observer) restoreSources(pods []corev1.Pod) {
	// Restored accepted assignments identify exact physical sources, not routes.
	// Join all namespace Pods by UID so relabeling cannot hide drain evidence.
	for _, instance := range o.runtime.Instances() {
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

// nativeProbeTimeout bounds one native state read, so one slow executor does
// not hold the source lock for the whole refresh.
const nativeProbeTimeout = 2 * time.Second

func (o *Observer) probeSource(ctx context.Context, pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass, resources map[types.UID]client.Object, services map[string]*corev1.Service, servicesErr error) (serving.ReadyInstance, bool, *probedSource) {
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
			o.issue("logs/"+id, o.startLogs(pod, owner))
		}
	}
	if pod.Labels["app.kubernetes.io/component"] != servingInstanceComponent {
		return serving.ReadyInstance{}, false, nil
	}
	spec, err := sourceSpec(pod, node, profile, classes, "")
	if err == nil {
		err = ownershipErr
	}
	if node != nil && o.movingNodes[string(node.UID)] {
		err = errors.New("NodePowerTransition")
	}
	var service *corev1.Service
	if err == nil {
		if service, err = listedInstanceService(pod, services, servicesErr); err == nil {
			spec.Endpoint, err = instanceServiceEndpoint(service)
		} else if servicesErr != nil {
			instance, publish := o.sourceReadFailed(pod, node, spec.InstanceID, owner, spec.Profile.ID, err)
			return instance, publish, nil
		}
	}
	if err == nil {
		probeCtx, cancel := context.WithTimeout(ctx, nativeProbeTimeout)
		instance, probeErr := o.native.Probe(probeCtx, spec)
		cancel()
		if probeErr == nil {
			instance.OwnerWorkloadUID = owner
			instance.State.TargetID = pod.Annotations[ServingSlotAnnotation]
			instance.State.Stopped = o.removingInstances[spec.InstanceID]
			return serving.ReadyInstance{}, false, &probedSource{pod: pod, node: node, service: service, owner: owner, instance: instance}
		}
		err = probeErr
	}
	instance, publish := o.sourceUnavailable(pod, node, err)
	return instance, publish, nil
}

// verifySource publishes a probed instance only if the Pod list read behind
// the probe shows the same running executor as the only one its Service selects.
func (o *Observer) verifySource(p probedSource, latest *corev1.PodList, latestErr error) (serving.ReadyInstance, bool) {
	if latestErr != nil {
		return o.sourceReadFailed(p.pod, p.node, p.instance.State.ID, p.owner, p.instance.State.Profile, latestErr)
	}
	var current *corev1.Pod
	selected := 0
	for i := range latest.Items {
		if latest.Items[i].Labels[instancePodUIDLabel] == string(p.pod.UID) {
			selected++
			current = &latest.Items[i]
		}
	}
	var err error
	// A restart between snapshot and HTTP readiness must not alias executors.
	switch {
	case selected != 1 || current.Name != p.pod.Name || current.Spec.NodeName != p.pod.Spec.NodeName || !sameNativeContainer(p.pod, current):
		err = errors.New("native Service does not select only the current physical executor")
	case !sameRunningContainer(p.pod, current):
		err = errors.New("native container changed during probe")
	default:
		err = validateInstanceService(current, p.service)
	}
	if err == nil {
		err = o.startLogs(current, p.owner)
	}
	if err == nil {
		o.issue(p.pod.Namespace+"/"+p.pod.Name, nil)
		return p.instance, true
	}
	return o.sourceUnavailable(p.pod, p.node, err)
}

func (o *Observer) sourceUnavailable(pod *corev1.Pod, node *corev1.Node, err error) (serving.ReadyInstance, bool) {
	o.issue(pod.Namespace+"/"+pod.Name, err)
	if status := firstfloorStatus(pod); status != nil && status.ContainerID != "" && node != nil {
		return serving.ReadyInstance{State: serving.InstanceState{ID: physicalID(pod.UID, status.ContainerID), Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: pod.Annotations[ServingProfileAnnotation], Reason: err.Error(), ObservedAt: time.Now().UTC()}}, true
	}
	return serving.ReadyInstance{}, false
}

// sourceReadFailed republishes the last verified readiness of an executor the
// current snapshot still shows ready, with the same owner and profile, when a
// Kubernetes read failed: the failure says nothing about the executor.
func (o *Observer) sourceReadFailed(pod *corev1.Pod, node *corev1.Node, id, owner, profile string, err error) (serving.ReadyInstance, bool) {
	if last, ok := o.verified[id]; ok && last.OwnerWorkloadUID == owner && last.State.Profile == profile {
		o.issue(pod.Namespace+"/"+pod.Name, fmt.Errorf("verified readiness kept after a failed read: %w", err))
		last.State.Stopped = o.removingInstances[id]
		return last, true
	}
	return o.sourceUnavailable(pod, node, err)
}

func (o *Observer) disable(id, reason string) {
	delete(o.verified, id)
	o.runtime.DisableInstance(id, reason)
}

func (o *Observer) refreshFailure(err error) error {
	o.issue("refresh", err)
	o.verified = nil
	o.runtime.SetMeasurementNodes(nil)
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

func sourceSpec(pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass, endpoint string) (firstfloor.Spec, error) {
	var empty firstfloor.Spec
	if err := validateSourcePlacement(pod, node, profile, classes); err != nil {
		return empty, err
	}
	condition := &profile.Condition
	weight, e := strconv.ParseInt(pod.Annotations[ServingWeightAnnotation], 10, 32)
	if e != nil || weight < 1 {
		return empty, errors.New("explicit positive serving weight required")
	}
	raw, routed := pod.Annotations[ServingRoutingAnnotation]
	if !routed {
		return empty, errors.New("applied serving routing annotation required")
	}
	var routing map[string]int32
	if e = json.Unmarshal([]byte(raw), &routing); e != nil || routing == nil {
		return empty, errors.New("invalid applied serving routing")
	}
	if len(routing) > 1 {
		return empty, errors.New("applied serving routing must name at most one Workload")
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
	expected.ModelFiles = deliveredModelFiles(condition.Artifact.Files)
	return firstfloor.Spec{Endpoint: endpoint, ModelName: "chill_model", InstanceID: physicalID(pod.UID, status.ContainerID), Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: profile, Weight: int32(weight), Routing: routing, Capacity: int(condition.Firstfloor.MaxQueueSize), ExpectedRuntime: &expected, DecisionID: pod.Annotations[ServingPolicyAnnotation]}, nil
}

func validateSourcePlacement(pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass) error {
	if pod.UID == "" || node == nil || node.UID == "" || node.DeletionTimestamp != nil {
		return errors.New("current Node UID unavailable")
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
	return confirmedPower(node, class, condition.PowerMode)
}

// deliveredModelFiles is the sorted set of model files the runner loads: every
// frozen artifact file copied to /model under its basename.
func deliveredModelFiles(files []string) []string {
	delivered := make([]string, len(files))
	for i, file := range files {
		delivered[i] = path.Join("/model", path.Base(file))
	}
	slices.Sort(delivered)
	return delivered
}

func validateSourceContainers(pod *corev1.Pod, condition *profilederivation.CurvePlanSpec) error {
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 3 || len(pod.Spec.EphemeralContainers) != 0 || pod.Spec.HostNetwork {
		return errors.New("native source requires isolated firstfloor runner, artifact, preprocessor and immutable profile init containers")
	}
	runtime, artifact, preprocessor, bundle := pod.Spec.Containers[0], pod.Spec.InitContainers[0], pod.Spec.InitContainers[1], pod.Spec.InitContainers[2]
	status := firstfloorStatus(pod)
	if runtime.Name != firstfloorContainerName || artifact.Name != "artifact" || preprocessor.Name != "preprocessor" || bundle.Name != profileInitContainerName || status == nil || status.State.Running == nil || !status.Ready || status.ContainerID == "" || !observedImage(runtime.Image, status.ImageID, condition.RuntimeImage) {
		return errors.New("actual immutable native runtime image/container unavailable")
	}
	if !initCompleted(pod, artifact, condition.Artifact.Image) {
		return errors.New("immutable artifact preparation unconfirmed")
	}
	for _, file := range condition.Artifact.Files {
		if path.Dir(file) != "/artifact" {
			return errors.New("artifact must use the verified read-only shared-volume projection")
		}
	}
	if !slices.Equal(artifact.Command, []string{"sh", "-ec"}) || !slices.Equal(artifact.Args, append([]string{`cp "$@" /chill-artifact/`, "--"}, condition.Artifact.Files...)) || !sharedProjection(pod, artifact, runtime, "/chill-artifact", "/model") {
		return errors.New("artifact must use the verified read-only shared-volume projection")
	}
	if !initCompleted(pod, preprocessor, condition.Preprocessor.Image) {
		return errors.New("immutable preprocessor preparation unconfirmed")
	}
	if !slices.Equal(preprocessor.Command, []string{"sh", "-ec"}) || !slices.Equal(preprocessor.Args, []string{`cp -R "$1"/. /chill-preprocessor/`, "--", condition.Preprocessor.Path}) || !sharedProjection(pod, preprocessor, runtime, "/chill-preprocessor", "/preprocessor") {
		return errors.New("preprocessor must use the verified read-only shared-volume projection")
	}
	if !initCompleted(pod, bundle, condition.RuntimeImage) {
		return errors.New("immutable profile materialization unconfirmed")
	}
	return nil
}

// initCompleted confirms the init container ran the frozen image and exited 0.
func initCompleted(pod *corev1.Pod, init corev1.Container, image string) bool {
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == init.Name {
			return status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 && observedImage(init.Image, status.ImageID, image)
		}
	}
	return false
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
func sharedProjection(pod *corev1.Pod, init, runtime corev1.Container, initPath, runtimePath string) bool {
	for _, a := range init.VolumeMounts {
		for _, r := range runtime.VolumeMounts {
			if a.Name == r.Name && a.MountPath == initPath && r.MountPath == runtimePath && !a.ReadOnly && r.ReadOnly && a.SubPath == "" && r.SubPath == "" && a.SubPathExpr == "" && r.SubPathExpr == "" {
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

// confirmedPower requires the Node to be in the measured mode now, as
// readBackMode reads it.
func confirmedPower(node *corev1.Node, class *edgev1alpha1.DeviceClass, name string) error {
	if _, err := deviceclass.FindPowerMode(class.Spec.PowerModes, name); err != nil {
		return err
	}
	if readBackMode(node, class) != name {
		return errors.New("Node read-back power mode differs from measured condition")
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
			source.stopFollow()
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
						o.disable(id, "native Pod readiness/identity unavailable")
					}
					if changed {
						source.stopFollow()
					}
				}
			} else if event.Type == watch.Error {
				o.issue("pod-watch", errors.New("native Pod status watch error"))
				o.verified = nil
				_ = o.runtime.InstallInstances(nil)
			}
			o.mu.Unlock()
		}
	}
}

// startLogs records the running executor and starts its progress follow. The
// log stream opens in the follow goroutine because kubelets (KubeEdge edged in
// particular) may answer slowly; Refresh and the lock never wait on it. SinceTime
// keeps progress written while the stream opens, and exchange results complete
// requests without it.
func (o *Observer) startLogs(pod *corev1.Pod, owner string) error {
	status := firstfloorStatus(pod)
	id := physicalID(pod.UID, status.ContainerID)
	source := o.sources[id]
	if source != nil && source.stopped {
		return errors.New("terminated physical executor cannot become current")
	}
	if source == nil {
		source = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: status.ContainerID, ownerUID: types.UID(owner)}
		o.sources[id] = source
	} else if owner != "" {
		source.ownerUID = types.UID(owner)
	}
	if source.cancel != nil {
		return nil
	}
	options := &corev1.PodLogOptions{Container: firstfloorContainerName, Follow: true, Timestamps: true}
	if since := o.runtime.ProgressSince(id, processStart); !since.IsZero() {
		options.SinceTime = &metav1.Time{Time: since}
	}
	source.follow, source.cancel = context.WithCancel(o.ctx)
	o.wg.Add(1)
	go o.followLogs(source.follow, id, pod.DeepCopy(), options)
	return nil
}
func (o *Observer) followLogs(ctx context.Context, id string, pod *corev1.Pod, options *corev1.PodLogOptions) {
	defer o.wg.Done()
	stream, err := o.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, options).Stream(ctx)
	if err != nil {
		err = fmt.Errorf("native progress source unavailable: %w", err)
	} else if latest, getErr := o.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); getErr != nil || !sameNativeContainer(pod, latest) {
		err = errors.Join(errors.New("native container changed while opening exact progress source"), getErr, stream.Close())
	} else if err = o.scanLogs(id, stream); err == nil {
		err = errors.New("native progress stream ended")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if source := o.sources[id]; source != nil && source.follow == ctx {
		if ctx.Err() == nil {
			o.issue("logs/"+id, err)
			o.disable(id, err.Error())
		}
		source.stopFollow()
	}
}
func (o *Observer) scanLogs(id string, stream io.ReadCloser) error {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 8192), 64<<10)
	for scanner.Scan() {
		progress, err := firstfloor.ParseProgress(scanner.Bytes())
		if errors.Is(err, firstfloor.ErrNotProgress) {
			continue
		}
		if err == nil {
			err = o.runtime.ObserveBackend(id, progress)
		}
		o.issue("logs/"+id, err)
	}
	return errors.Join(scanner.Err(), stream.Close())
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
				if err = patchSourceFinalizer(ctx, o.store.client, pod, true); err != nil {
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
			if err = patchSourceFinalizer(ctx, o.store.client, pod, false); err != nil {
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
		for _, instance := range o.runtime.Instances() {
			if strings.HasPrefix(instance.ID, string(pod.UID)+"/") {
				for _, workload := range instance.Workloads {
					if workload.UID != string(uid) {
						return errors.New("Workload-owned resource is still shared by another registration")
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
			source.stopFollow()
		}
	}
	o.mu.Unlock()
	o.wg.Wait()
	return nil
}

// detachNodeLocked returns with the source lock held, releasing it only while
// waiting for actual outstanding calls so native termination can be observed.
func (o *Observer) detachNodeLocked(ctx context.Context, uid string) error {
	for _, instance := range o.runtime.Instances() {
		if instance.Node.UID == uid {
			o.disable(instance.ID, "NodePowerTransition")
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
			return errors.New("observer closed during Node power transition")
		}
	}
}
