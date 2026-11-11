package kube

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	profileInitContainerName = "profile"
	nonTerminalPodSelector   = "status.phase!=Succeeded,status.phase!=Failed"
)

type ObserverConfig struct {
	Namespace        string
	RuntimeImage     func(context.Context, *serving.Profile) (string, error)
	OnCluster        func(observation.ClusterObservation) error
	SelectClusterPod func(*corev1.Pod) bool
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
	instance    serving.ReadyInstance
	previous    map[string]serving.ReadyInstance
	podState    *corev1.Pod
	terminated  map[string]bool
	deleted     bool
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
	*observerState
	cluster     *ClusterState
	refreshMu   sync.Mutex
	store       *Store
	kube        kubernetes.Interface
	runtime     *serving.Runtime
	native      *firstfloor.Client
	config      ObserverConfig
	profiles    []serving.Profile
	profileByID map[string]*serving.Profile
	sources     map[string]*nativeSource
	// issueMu guards issues, so log followers record progress and its issues
	// while a refresh holds mu.
	issueMu        sync.Mutex
	issues         map[string]ObserverIssue
	recordedIssues map[string]ObserverIssue
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	podWatch       watch.Interface
	closed         bool
	movingNodes    map[string]bool
	// refreshed is when the last successful refresh finished.
	refreshed   time.Time
	clusterPods map[types.UID]observation.ClusterPod
}

func NewObserver(cluster *ClusterState, store *Store, kube kubernetes.Interface, runtime *serving.Runtime, native *firstfloor.Client, config ObserverConfig) (*Observer, error) {
	if cluster == nil || store == nil || store.client == nil || kube == nil || runtime == nil || native == nil || config.Namespace == "" {
		return nil, errors.New("observer requires direct store, Kubernetes/native clients, runtime and selected namespace")
	}
	if config.OnCluster != nil && config.SelectClusterPod == nil {
		return nil, errors.New("cluster observations require a CHILL Pod selector")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Observer{cluster: cluster, store: store, kube: kube, runtime: runtime, native: native, config: config, sources: map[string]*nativeSource{}, issues: map[string]ObserverIssue{}, movingNodes: map[string]bool{}, observerState: &cluster.observation, ctx: ctx, cancel: cancel}, nil
}

func (o *Observer) SetProfiles(profiles []serving.Profile) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.profiles = slices.Clone(profiles)
	previous := o.profileByID
	o.profileByID = make(map[string]*serving.Profile, len(o.profiles))
	for i := range o.profiles {
		profile := &o.profiles[i]
		canonical := previous[profile.ID]
		if canonical == nil || !reflect.DeepEqual(canonical, profile) {
			frozen := cloneCatalogProfile(*profile)
			// Catalog cloning defaults preparation settings; evidence keeps the
			// original policy and nil/empty slice shapes exactly as published.
			if profile.Condition.Firstfloor != nil {
				frozen.Condition.Firstfloor.DeadlinePolicy = profile.Condition.Firstfloor.DeadlinePolicy
			}
			if len(profile.Condition.PlannedBatchSizes) == 0 {
				frozen.Condition.PlannedBatchSizes = profile.Condition.PlannedBatchSizes
			}
			if access := profile.Condition.RuntimeAccess; access != nil && len(access.SupplementalGroups) == 0 {
				frozen.Condition.RuntimeAccess.SupplementalGroups = access.SupplementalGroups
			}
			canonical = &frozen
		}
		o.profileByID[profile.ID] = canonical
	}
	o.runtime.SetProfiles(o.profiles)
}
func (o *Observer) Profiles() []serving.Profile {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.profiles)
}

// schedulerProfiles captures the catalog and its canonical immutable references
// together. SetProfiles replaces a reference only when that profile changes.
func (o *Observer) schedulerProfiles() ([]serving.Profile, map[string]*serving.Profile) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.profiles), maps.Clone(o.profileByID)
}

// OccupancyProfiles returns the same immutable identities used by scheduler capture.
func (o *Observer) OccupancyProfiles() map[string]*serving.Profile {
	_, profiles := o.schedulerProfiles()
	return profiles
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
	now := time.Now().UTC()
	if err == nil {
		delete(o.issues, source)
	} else {
		if previous, seen := o.issues[source]; source != "refresh" && (!seen || previous.Reason != err.Error()) {
			ctrllog.Log.WithName("observer").Error(err, "native source issue", "source", source)
		}
		o.issues[source] = ObserverIssue{Source: source, Reason: err.Error(), At: now}
	}
	o.recordIssueLocked(source, now)
}

// Refresh publishes the Pod ledger independently of current native readiness.
func (o *Observer) Refresh(ctx context.Context) error {
	_, err := o.refresh(ctx)
	return err
}

type observerRefresh struct {
	nodes            *corev1.NodeList
	classes          edgev1alpha1.DeviceClassList
	clusterPods      *corev1.PodList
	nodesObservedAt  time.Time
	listNanos        time.Duration
	nodesNanos       time.Duration
	podsNanos        time.Duration
	resourcesNanos   time.Duration
	classesNanos     time.Duration
	servicesNanos    time.Duration
	sourceProbeNanos time.Duration
}

// refresh returns this cycle's Node and DeviceClass facts and the complete
// cluster Pod baseline when OnCluster requires one. The caller owns the lists;
// no later refresh or watch updates them.
func (o *Observer) refresh(ctx context.Context) (observerRefresh, error) {
	o.refreshMu.Lock()
	defer o.refreshMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return observerRefresh{}, err
	}
	return o.refreshLocked(ctx)
}

// RefreshWithin reuses a successful refresh that finished less than age ago
// and refreshes otherwise; pollers share the source refresh this way, also
// one that ran while they waited for the lock.
func (o *Observer) RefreshWithin(ctx context.Context, age time.Duration) error {
	o.refreshMu.Lock()
	defer o.refreshMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Since(o.refreshed) < age {
		return nil
	}
	_, err := o.refreshLocked(ctx)
	return err
}
func (o *Observer) refreshLocked(ctx context.Context) (result observerRefresh, err error) {
	if o.closed {
		return result, errors.New("observer is closed")
	}
	// Request start is a conservative lower bound; slow live I/O still ages it.
	listStarted := time.Now()
	result.nodesObservedAt = listStarted.UTC()
	nodes, err := o.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	result.nodes = nodes
	result.nodesNanos = time.Since(listStarted)
	result.listNanos += result.nodesNanos
	if err != nil {
		return result, o.refreshFailure(err)
	}
	podOptions := metav1.ListOptions{}
	if o.podWatch != nil {
		// Restore the full baseline on startup/watch restart. Afterwards the
		// watch records terminal transitions and retained-source GET proves containers.
		podOptions.FieldSelector = nonTerminalPodSelector
	}
	listStarted = time.Now()
	pods, err := o.kube.CoreV1().Pods(o.clusterWatchNamespace()).List(ctx, podOptions)
	result.podsNanos = time.Since(listStarted)
	result.listNanos += result.podsNanos
	if err != nil {
		return result, o.refreshFailure(err)
	}
	o.observeClusterList(pods.Items)
	if o.config.OnCluster != nil {
		// Cluster facts include release control Pods outside the serving namespace;
		// native execution and ownership continue to use only the selected namespace.
		result.clusterPods = pods
		pods = o.sourceNamespacePods(pods)
	}
	listStarted = time.Now()
	resources, err := o.store.SourceResources(ctx, pods.Items, o.config.Namespace)
	result.resourcesNanos = time.Since(listStarted)
	result.listNanos += result.resourcesNanos
	if err != nil {
		return result, o.refreshFailure(err)
	}
	listStarted = time.Now()
	err = o.store.client.List(ctx, &result.classes)
	result.classesNanos = time.Since(listStarted)
	result.listNanos += result.classesNanos
	if err != nil {
		return result, o.refreshFailure(err)
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
	for ref := range o.powerReboots {
		if node := byName[ref.Name]; node == nil || string(node.UID) != ref.UID {
			delete(o.powerReboots, ref)
		}
	}
	sort.Slice(measurement, func(i, j int) bool { return measurement[i].Name < measurement[j].Name })
	o.runtime.SetMeasurementNodes(measurement)
	// Start a native status watch before using this snapshot for execution or cleanup.
	if o.podWatch == nil {
		w, e := o.kube.CoreV1().Pods(o.clusterWatchNamespace()).Watch(o.ctx, metav1.ListOptions{ResourceVersion: pods.ResourceVersion})
		if e != nil {
			return result, o.refreshFailure(e)
		}
		o.podWatch = w
		o.issue("pod-watch", nil)
		o.wg.Add(1)
		go o.watchPods(w)
	}
	profiles := map[string]*serving.Profile{}
	for i := range o.profiles {
		profiles[o.profiles[i].ID] = &o.profiles[i]
	}
	// Share one Service list; each completed native probe re-lists only its
	// UID label, including terminal Pods, to verify identity and duplicates.
	listStarted = time.Now()
	services, servicesErr := o.kube.CoreV1().Services(o.config.Namespace).List(ctx, metav1.ListOptions{})
	result.servicesNanos = time.Since(listStarted)
	result.listNanos += result.servicesNanos
	servicesByName := map[string]*corev1.Service{}
	if servicesErr == nil {
		for i := range services.Items {
			servicesByName[services.Items[i].Name] = &services.Items[i]
		}
	}
	o.restoreSources(pods.Items)
	observed := map[types.UID]bool{}
	// Reconciliation owns only identities retained when this refresh began.
	// A watch or removal may install a replacement while native probes run.
	for _, source := range o.sources {
		observed[source.uid] = false
	}
	var probes sync.WaitGroup
	for i := range pods.Items {
		pod := &pods.Items[i]
		observed[pod.UID] = true
		if source := o.sources[string(pod.UID)]; source != nil {
			if node := byName[pod.Spec.NodeName]; node != nil && source.instance.State.Node.UID == "" {
				source.instance.State.Node = serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			}
		}
		probes.Add(1)
		go func() {
			defer probes.Done()
			instance, publish, probe := o.probeSource(ctx, pod, byName[pod.Spec.NodeName], profiles[pod.Annotations[servingstate.ServingProfileAnnotation]], result.classes.Items, resources, servicesByName, servicesErr)
			var latest *corev1.PodList
			var latestErr error
			if probe != nil {
				latest, latestErr = o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: servingstate.InstancePodUIDLabel + "=" + string(pod.UID)})
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			if probe != nil {
				instance, publish = o.verifySource(*probe, latest, latestErr)
			}
			var ready []serving.ReadyInstance
			if publish {
				ready = append(ready, instance)
			}
			o.issue("publish", o.publishLedger(ready))
		}()
	}
	o.mu.Unlock()
	probeStarted := time.Now()
	probes.Wait()
	result.sourceProbeNanos = time.Since(probeStarted)
	o.mu.Lock()
	if err := o.finishRefreshLocked(ctx, observed); err != nil {
		return result, err
	}
	return result, nil
}

func (o *Observer) sourceNamespacePods(pods *corev1.PodList) *corev1.PodList {
	selected := &corev1.PodList{ListMeta: pods.ListMeta}
	for _, pod := range pods.Items {
		if pod.Namespace == o.config.Namespace {
			selected.Items = append(selected.Items, pod)
		}
	}
	return selected
}

// finishRefreshLocked finalizes the source ledger after every probe has published.
func (o *Observer) finishRefreshLocked(ctx context.Context, observed map[types.UID]bool) error {
	if err := ctx.Err(); err != nil {
		o.refreshed = time.Time{}
		o.issue("refresh", err)
		return err
	}
	o.reconcileSources(ctx, observed)
	if err := o.publishLedger(nil); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		o.refreshed = time.Time{}
		o.issue("refresh", err)
		return err
	}
	o.issue("refresh", nil)
	o.refreshed = time.Now()
	return nil
}

// reconcileSources reads retained Pods even when a filtered listing omitted them.
func (o *Observer) reconcileSources(ctx context.Context, observed map[types.UID]bool) {
	missing := false
	for _, seen := range observed {
		missing = missing || !seen
	}
	var latest *corev1.PodList
	var latestErr error
	byName := map[string]*corev1.Pod{}
	if missing {
		latest, latestErr = o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, metav1.ListOptions{})
		for i := 0; latestErr == nil && i < len(latest.Items); i++ {
			byName[latest.Items[i].Name] = &latest.Items[i]
		}
	}
	// Missing/relabeled Pods are still read for exact old executor termination.
	for uid, seen := range observed {
		source := o.sources[string(uid)]
		if seen || source == nil {
			continue
		}
		pod := byName[source.pod.Name]
		if pod != nil && pod.UID == source.uid {
			o.observeTermination(pod)
		} else if !source.stopped {
			err := latestErr
			if err == nil {
				err = errors.New("source disappeared without confirmed container termination")
			}
			_, _ = o.sourceUnavailable(source.uid, source.pod.String(), err)
		}
	}
}

// probedSource is an instance whose native probe succeeded; it is published
// after the Pod list taken behind the probe confirms the same executor.
type probedSource struct {
	pod      *corev1.Pod
	service  *corev1.Service
	owner    string
	instance serving.ReadyInstance
}

func (o *Observer) restoreSources(pods []corev1.Pod) {
	for i := range pods {
		pod := &pods[i]
		key := string(pod.UID)
		source := o.sources[key]
		if source == nil {
			if pod.UID == "" || pod.Labels["app.kubernetes.io/component"] != servingstate.ServingInstanceComponent {
				continue
			}
			source = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, terminated: map[string]bool{}}
			o.sources[key] = source
		}
		source.podState = pod.DeepCopy()
		if source.terminated == nil {
			source.terminated = map[string]bool{}
		}
		if source.containerID != "" {
			if _, known := source.terminated[source.containerID]; !known {
				source.terminated[source.containerID] = source.stopped
			}
		}
		if status := servingstate.FirstfloorStatus(pod); status != nil && status.ContainerID != "" {
			if source.containerID != "" && source.containerID != status.ContainerID {
				if source.previous == nil {
					source.previous = map[string]serving.ReadyInstance{}
				}
				source.previous[source.containerID] = source.instance
				source.instance = serving.ReadyInstance{State: serving.InstanceState{Node: source.instance.State.Node}}
				source.stopped = false
			}
			if _, known := source.terminated[status.ContainerID]; !known {
				source.terminated[status.ContainerID] = false
			}
			source.containerID = status.ContainerID
		}
		if owner := servingstate.WorkloadOwner(pod.OwnerReferences); owner != "" {
			source.ownerUID = types.UID(owner)
		}
		o.observeTermination(pod)
	}
}

// publishLedger owns the runtime inventory projection. Probe omissions never
// erase physical existence, ownership or the last successful observation.
func (o *Observer) publishLedger(probed []serving.ReadyInstance) error {
	for _, instance := range probed {
		uid, _, _ := strings.Cut(instance.State.ID, "/")
		source := o.sources[uid]
		if source == nil || source.deleted || source.stopped || instance.State.ID != servingstate.PhysicalID(source.uid, source.containerID) {
			continue
		}
		instance.State.PodUID = uid
		instance.State.AttemptedAt = time.Now().UTC()
		source.instance = instance
	}
	instances := make([]serving.ReadyInstance, 0, len(o.sources))
	for _, source := range o.sources {
		if source.podState == nil {
			continue
		}
		pod := source.podState
		instance := source.instance
		instance.State.PodUID = string(source.uid)
		if status := servingstate.FirstfloorStatus(pod); status != nil {
			instance.State.ActualImageID = status.ImageID
		}
		instance.State.TargetID = pod.Annotations[servingstate.ServingSlotAnnotation]
		instance.OwnerWorkloadUID = string(source.ownerUID)
		instance.State.OwnerWorkloadUID = string(source.ownerUID)
		instance.State.Terminated = source.terminated[source.containerID] || source.containerID == "" && source.stopped
		instance.State.Stopped = source.stopped || instance.State.Terminated
		instance.State.Ready = instance.State.Ready && !instance.State.Terminated
		failure, failed := servingstate.ClassifyPreparationFailure(pod, nil, nil, nil, false)
		instance.State.StartupFailure = nil
		if failed {
			instance.State.StartupFailure = &failure
		}
		if source.containerID != "" {
			instance.State.ID = servingstate.PhysicalID(source.uid, source.containerID)
		}
		instance.State.Profile = pod.Annotations[servingstate.ServingProfileAnnotation]
		if instance.State.Node.UID == "" {
			instance.State.Node.Name = pod.Spec.NodeName
		}
		instance.Profile = nil
		for i := range o.profiles {
			profile := &o.profiles[i]
			if profile.ID != instance.State.Profile {
				continue
			}
			instance.Profile = profile
			if profile.RuntimeMetadata != nil {
				m := profile.RuntimeMetadata
				instance.State.Allocation.GPUUUID = m.GPUUUID
			}
		}
		if instance.Profile == nil {
			instance.State.Ready = false
			instance.Execute = nil
			instance.Routing = nil
			instance.Capacity = 0
		}
		for _, container := range pod.Spec.Containers {
			if container.Name == servingstate.FirstfloorContainerName {
				instance.State.Allocation.MemoryBytes = uint64(container.Resources.Limits.Memory().Value())
			}
		}
		source.instance = instance
		instances = append(instances, instance)
		for containerID, previous := range source.previous {
			if previous.State.ID == "" {
				continue
			}
			previous.State.PodUID = string(source.uid)
			previous.State.Ready = false
			previous.State.NativeVerified = false
			if o.profileByID[previous.State.Profile] == nil {
				previous.Profile = nil
				previous.Execute = nil
				previous.Routing = nil
				previous.Capacity = 0
			}
			previous.State.Stopped = source.terminated[containerID]
			previous.State.Terminated = source.terminated[containerID]
			instances = append(instances, previous)
		}
	}
	return o.runtime.InstallInstances(instances)
}

// nativeProbeTimeout bounds one native state read, so one slow executor does
// not hold the source lock for the whole refresh.
const nativeProbeTimeout = 2 * time.Second

func (o *Observer) physicalSourceSpec(ctx context.Context, pod *corev1.Pod, node *corev1.Node) (firstfloor.Spec, error) {
	if err := validatePhysicalSource(pod, node); err != nil {
		return firstfloor.Spec{}, err
	}
	model, err := servingstate.NativeModel(ctx, o.store.client, pod)
	if err != nil {
		return firstfloor.Spec{}, err
	}
	status := servingstate.FirstfloorStatus(pod)
	return firstfloor.Spec{ModelName: model, InstanceID: servingstate.PhysicalID(pod.UID, status.ContainerID),
		Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}}, nil
}

func matchesNativeSource(pod *corev1.Pod, model string, instance *serving.InstanceState) bool {
	observed := instance.Native
	profileID := pod.Annotations[servingstate.ServingProfileAnnotation]
	return observed != nil && observed.Ready && observed.Metadata.Model == model &&
		observed.Metadata.Mode == servingstate.NativeServingMode &&
		profileID != "" && observed.Metadata.ProfileID == profileID
}

func (o *Observer) probeSource(ctx context.Context, pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass, resources map[types.UID]client.Object, services map[string]*corev1.Service, servicesErr error) (serving.ReadyInstance, bool, *probedSource) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observeTermination(pod)
	owner, ownershipErr := resourceWorkloadOwner(pod, resources)
	if source := o.sources[string(pod.UID)]; source != nil && ownershipErr == nil {
		source.ownerUID = types.UID(owner)
	}
	if status := servingstate.FirstfloorStatus(pod); status != nil && status.State.Running != nil && status.ContainerID != "" {
		id := servingstate.PhysicalID(pod.UID, status.ContainerID)
		if retained := o.sources[string(pod.UID)]; retained != nil && !retained.stopped {
			o.issue("logs/"+id, o.startLogs(pod, owner))
		}
	}
	if pod.Labels["app.kubernetes.io/component"] != servingstate.ServingInstanceComponent {
		instance, publish := o.sourceUnavailable(pod.UID, pod.Namespace+"/"+pod.Name, errors.New("Pod is not labelled as a serving instance"))
		return instance, publish, nil
	}
	o.recordRuntimeDeclaration(ctx, pod, profile)
	spec, admissionErr := sourceSpec(pod, node, profile, classes, "")
	err := ownershipErr
	if node != nil && o.movingNodes[string(node.UID)] {
		err = errors.New("NodePowerTransition")
	}
	if err == nil && admissionErr != nil {
		spec, err = o.physicalSourceSpec(ctx, pod, node)
	}
	var service *corev1.Service
	if err == nil {
		if service, err = servingstate.ListedInstanceService(pod, services, servicesErr); err == nil {
			spec.Endpoint, err = servingstate.InstanceServiceEndpoint(service)
		}
	}
	if err == nil {
		probeCtx, cancel := context.WithTimeout(ctx, nativeProbeTimeout)
		o.mu.Unlock()
		instance, probeErr := o.native.Probe(probeCtx, spec)
		readSucceeded := probeCtx.Err() == nil
		if err := probeCtx.Err(); err != nil {
			probeErr = err
		}
		cancel()
		o.mu.Lock()
		source := o.sources[string(pod.UID)]
		if source == nil || source.stopped || source.deleted || servingstate.PhysicalID(source.uid, source.containerID) != spec.InstanceID {
			return serving.ReadyInstance{}, false, nil
		}
		if probeErr == nil || instance.State.Native != nil {
			instance.State.NativeVerified = readSucceeded && matchesNativeSource(pod, spec.ModelName, &instance.State)
			if probeErr != nil {
				instance.State.Reason = probeErr.Error()
			}
			if admissionErr != nil {
				instance.State.Reason = admissionErr.Error()
				instance.State.Ready = false
				instance.Execute = nil
				instance.Routing = nil
				instance.Capacity = 0
			}
			if !instance.State.Ready || probeErr != nil {
				instance.State.Ready = false
				reason := instance.State.Reason
				if reason == "" {
					reason = "native instance is not ready"
				}
				if source := o.sources[string(pod.UID)]; source != nil {
					source.instance.State.Ready = false
					source.instance.State.NativeVerified = false
					source.instance.State.Reason = reason
					source.instance.State.AttemptedAt = time.Now().UTC()
					o.runtime.DisableInstance(servingstate.PhysicalID(source.uid, source.containerID), reason)
				}
			}
			instance.OwnerWorkloadUID = owner
			instance.State.DesiredRuntimeImage = source.instance.State.DesiredRuntimeImage
			instance.State.ReplacementReason = source.instance.State.ReplacementReason
			instance.State.TargetID = pod.Annotations[servingstate.ServingSlotAnnotation]
			instance.State.Stopped = o.removingInstances[spec.InstanceID]
			return serving.ReadyInstance{}, false, &probedSource{pod: pod, service: service, owner: owner, instance: instance}
		}
		err = probeErr
	}
	instance, publish := o.sourceUnavailable(pod.UID, pod.Namespace+"/"+pod.Name, err)
	return instance, publish, nil
}

// verifySource publishes a probed instance only if the Pod list read behind
// the probe shows the same running executor as the only one its Service selects.
func (o *Observer) verifySource(p probedSource, latest *corev1.PodList, latestErr error) (serving.ReadyInstance, bool) {
	source := o.sources[string(p.pod.UID)]
	status := servingstate.FirstfloorStatus(p.pod)
	if source == nil || source.stopped || source.deleted || status == nil || source.containerID != status.ContainerID {
		return serving.ReadyInstance{}, false
	}
	if latestErr != nil {
		return o.sourceUnavailable(p.pod.UID, p.pod.Namespace+"/"+p.pod.Name, latestErr)
	}
	var current *corev1.Pod
	selected := 0
	for i := range latest.Items {
		if latest.Items[i].Labels[servingstate.InstancePodUIDLabel] == string(p.pod.UID) {
			selected++
			current = &latest.Items[i]
		}
	}
	var err error
	// A restart between snapshot and HTTP readiness must not alias executors.
	switch {
	case selected != 1 || current.Name != p.pod.Name || current.Spec.NodeName != p.pod.Spec.NodeName || !servingstate.SameNativeContainer(p.pod, current):
		err = errors.New("native Service does not select only the current physical executor")
	case !servingstate.SameRunningContainer(p.pod, current):
		err = errors.New("native container changed during probe")
	default:
		err = servingstate.ValidateInstanceService(current, p.service)
	}
	if err == nil {
		err = o.startLogs(current, p.owner)
	}
	if err == nil {
		o.issue(p.pod.Namespace+"/"+p.pod.Name, nil)
		return p.instance, true
	}
	return o.sourceUnavailable(p.pod.UID, p.pod.Namespace+"/"+p.pod.Name, err)
}

func (o *Observer) sourceUnavailable(uid types.UID, name string, err error) (serving.ReadyInstance, bool) {
	o.issue(name, err)
	if source := o.sources[string(uid)]; source != nil {
		source.instance.State.Ready = false
		source.instance.State.NativeVerified = false
		source.instance.State.Reason = err.Error()
		source.instance.State.AttemptedAt = time.Now().UTC()
	}
	return serving.ReadyInstance{}, false
}

func (o *Observer) refreshFailure(err error) error {
	o.refreshed = time.Time{}
	o.issue("refresh", err)
	for _, source := range o.sources {
		source.instance.State.Ready = false
		source.instance.State.NativeVerified = false
		source.instance.State.Reason = err.Error()
		source.instance.State.AttemptedAt = time.Now().UTC()
	}
	_ = o.publishLedger(nil)
	return err
}

// Desired images select future preparations; they do not invalidate a live runner.
func (o *Observer) recordRuntimeDeclaration(ctx context.Context, pod *corev1.Pod, profile *serving.Profile) {
	desiredImage, err := o.CurrentRuntimeImage(ctx, profile)
	o.issue("runtime-declaration/"+string(pod.UID), err)
	source := o.sources[string(pod.UID)]
	if source == nil {
		return
	}
	source.instance.State.DesiredRuntimeImage = desiredImage
	source.instance.State.ReplacementReason = ""
	for _, containers := range [][]corev1.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
		for _, container := range containers {
			if desiredImage != "" && (container.Name == servingstate.FirstfloorContainerName || container.Name == profileInitContainerName) && container.Image != desiredImage {
				source.instance.State.ReplacementReason = "RunningOlderRuntimeImage"
			}
		}
	}
}

func (o *Observer) CurrentRuntimeImage(ctx context.Context, profile *serving.Profile) (string, error) {
	if profile == nil || o.config.RuntimeImage == nil {
		return "", errors.New("selected catalog profile and current execution declaration resolver required")
	}
	return o.config.RuntimeImage(ctx, profile)
}

func sourceSpec(pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass, endpoint string) (firstfloor.Spec, error) {
	var empty firstfloor.Spec
	if err := validateSourcePlacement(pod, node, profile, classes); err != nil {
		return empty, err
	}
	condition := &profile.Condition
	weight, e := strconv.ParseInt(pod.Annotations[servingstate.ServingWeightAnnotation], 10, 32)
	if e != nil || weight < 1 {
		return empty, errors.New("explicit positive serving weight required")
	}
	raw, routed := pod.Annotations[servingstate.ServingRoutingAnnotation]
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
	status := servingstate.FirstfloorStatus(pod)
	expected := *profile.RuntimeMetadata
	// Delivery changes only the verified artifact's mount location, not the
	// measured runtime, model bytes, provider, device or execution allocation.
	expected.ModelFiles = deliveredModelFiles(condition.Artifact.Files)
	return firstfloor.Spec{Endpoint: endpoint, ModelName: condition.Actor.Model, InstanceID: servingstate.PhysicalID(pod.UID, status.ContainerID), Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: profile, Weight: int32(weight), Routing: routing, Capacity: int(condition.Firstfloor.MaxQueueSize), ExpectedRuntime: &expected, DecisionID: pod.Annotations[servingstate.ServingPolicyAnnotation]}, nil
}

func validatePhysicalSource(pod *corev1.Pod, node *corev1.Node) error {
	if pod.UID == "" || node == nil || node.UID == "" || node.DeletionTimestamp != nil || pod.Spec.NodeName != node.Name {
		return errors.New("current Node UID unavailable")
	}
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !servingstate.PodReady(pod) {
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
	status := servingstate.FirstfloorStatus(pod)
	if status == nil || status.State.Running == nil || !status.Ready || status.ContainerID == "" || status.ImageID == "" {
		return errors.New("actual immutable native runtime image/container unavailable")
	}
	return nil
}

func validateSourcePlacement(pod *corev1.Pod, node *corev1.Node, profile *serving.Profile, classes []edgev1alpha1.DeviceClass) error {
	if err := validatePhysicalSource(pod, node); err != nil {
		return err
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
	for _, containers := range [][]corev1.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
		for _, container := range containers {
			if _, present := container.Resources.Requests[corev1.ResourceCPU]; present {
				return errors.New("shared CPU execution must not request CPU in any container")
			}
			if _, present := container.Resources.Limits[corev1.ResourceCPU]; present {
				return errors.New("shared CPU execution must not limit CPU in any container")
			}
		}
	}
	runtime, artifact, preprocessor, bundle := pod.Spec.Containers[0], pod.Spec.InitContainers[0], pod.Spec.InitContainers[1], pod.Spec.InitContainers[2]
	status := servingstate.FirstfloorStatus(pod)
	if runtime.Name != servingstate.FirstfloorContainerName || artifact.Name != "artifact" || preprocessor.Name != "preprocessor" || bundle.Name != profileInitContainerName || status == nil || status.State.Running == nil || !status.Ready || status.ContainerID == "" || status.ImageID == "" {
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
	if !initCompleted(pod, bundle, bundle.Image) {
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
	if _, requested := runtime.Resources.Requests[corev1.ResourceCPU]; requested {
		return errors.New("shared CPU execution must not request CPU")
	}
	if _, limited := runtime.Resources.Limits[corev1.ResourceCPU]; limited {
		return errors.New("shared CPU execution must not limit CPU")
	}
	memoryLimit := runtime.Resources.Limits[corev1.ResourceMemory]
	memoryRequest := runtime.Resources.Requests[corev1.ResourceMemory]
	nodeMemory := node.Status.Allocatable[corev1.ResourceMemory]
	// The request reserves scheduling capacity; the limit bounds runtime usage.
	for _, batch := range profile.Batches {
		if batch.MemoryBytes == 0 || memoryLimit.Sign() <= 0 || memoryRequest.Sign() <= 0 || memoryRequest.Cmp(memoryLimit) > 0 || uint64(memoryLimit.Value()) < batch.MemoryBytes || nodeMemory.Sign() <= 0 || uint64(nodeMemory.Value()) < batch.MemoryBytes {
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
	if servingstate.ReadBackMode(node, class) != name {
		return errors.New("Node read-back power mode differs from measured condition")
	}
	return nil
}

func (o *Observer) observeTermination(pod *corev1.Pod) {
	status := servingstate.FirstfloorStatus(pod)
	if source := o.sources[string(pod.UID)]; source != nil && source.containerID == "" && servingstate.PodTerminated(pod) {
		source.stopped = true
	}
	if status == nil {
		return
	}
	for _, terminal := range []*corev1.ContainerStateTerminated{status.State.Terminated, status.LastTerminationState.Terminated} {
		if terminal == nil || terminal.ContainerID == "" {
			continue
		}
		id := servingstate.PhysicalID(pod.UID, terminal.ContainerID)
		if source := o.sources[string(pod.UID)]; source != nil {
			if source.terminated == nil {
				source.terminated = map[string]bool{}
			}
			source.terminated[terminal.ContainerID] = true
			if source.containerID == terminal.ContainerID {
				source.stopped = true
				for _, ended := range source.terminated {
					source.stopped = source.stopped && ended
				}
				source.stopFollow()
			}
		}
		o.issue(id, o.runtime.ConfirmInstanceStopped(id))
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
				o.observeClusterPod(pod, string(event.Type))
				if pod.Namespace != o.config.Namespace {
					o.mu.Unlock()
					continue
				}
				if o.sources[string(pod.UID)] == nil {
					if event.Type == watch.Deleted {
						o.mu.Unlock()
						continue
					}
					current, err := o.kube.CoreV1().Pods(pod.Namespace).Get(o.ctx, pod.Name, metav1.GetOptions{})
					if err != nil || current.UID != pod.UID {
						o.mu.Unlock()
						continue
					}
					pod = current
				}
				o.restoreSources([]corev1.Pod{*pod})
				if source := o.sources[string(pod.UID)]; source != nil {
					status := servingstate.FirstfloorStatus(pod)
					changed := event.Type == watch.Deleted || status == nil || status.ContainerID != source.containerID || status.State.Running == nil
					if changed || pod.DeletionTimestamp != nil || !status.Ready || !servingstate.PodReady(pod) {
						source.instance.State.Ready = false
						source.instance.State.NativeVerified = false
						o.runtime.DisableInstance(servingstate.PhysicalID(source.uid, source.containerID), "native Pod readiness/identity unavailable")
					}
					if changed {
						source.stopFollow()
						source.deleted = event.Type == watch.Deleted
					}
				}
			} else if event.Type == watch.Error {
				o.issue("pod-watch", errors.New("native Pod status watch error"))
				_ = o.refreshFailure(errors.New("native Pod watch failed"))
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
	status := servingstate.FirstfloorStatus(pod)
	source := o.sources[string(pod.UID)]
	if source == nil || source.stopped || source.deleted || status == nil || status.ContainerID == "" || source.containerID != status.ContainerID {
		return errors.New("physical executor is no longer current")
	}
	id := servingstate.PhysicalID(pod.UID, status.ContainerID)
	if owner != "" {
		source.ownerUID = types.UID(owner)
	}
	if source.cancel != nil {
		return nil
	}
	options := &corev1.PodLogOptions{Container: servingstate.FirstfloorContainerName, Follow: true, Timestamps: true}
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
	} else if latest, getErr := o.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); getErr != nil || !servingstate.SameNativeContainer(pod, latest) {
		err = errors.Join(errors.New("native container changed while opening exact progress source"), getErr, stream.Close())
	} else if err = o.scanLogs(id, stream); err == nil {
		err = errors.New("native progress stream ended")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if source := o.sources[strings.SplitN(id, "/", 2)[0]]; source != nil && source.follow == ctx {
		if ctx.Err() == nil {
			o.issue("logs/"+id, err)
			o.runtime.DisableInstance(id, err.Error())
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
		if err != nil {
			err = fmt.Errorf("native progress rejected requestID=%q phase=%q physicalID=%q: %w", progress.RequestID, progress.Phase, id, err)
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
	objects, err := o.store.OwnedResources(ctx, uid, o.config.Namespace)
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
		if !slices.Contains(pod.Finalizers, servingstate.SourceFinalizer) {
			if pod.DeletionTimestamp != nil && !servingstate.PodTerminated(pod) {
				return false, fmt.Errorf("Pod %s termination unconfirmed before deletion", pod.Name)
			}
			if pod.DeletionTimestamp == nil {
				if err = servingstate.PatchSourceFinalizer(ctx, o.store.client, pod, true); err != nil {
					return false, err
				}
			}
		}
	}
	for _, object := range objects {
		if pod, ok := object.(*corev1.Pod); ok && servingstate.PodTerminated(pod) && slices.Contains(pod.Finalizers, servingstate.SourceFinalizer) {
			if err = servingstate.DeleteInstanceService(ctx, o.kube, pod); err != nil {
				return false, err
			}
			if err = servingstate.PatchSourceFinalizer(ctx, o.store.client, pod, false); err != nil {
				return false, err
			}
		}
		if replicaSet, ok := object.(*appsv1.ReplicaSet); ok && servingstate.WorkloadOwner(replicaSet.OwnerReferences) == "" {
			continue // Deployment foreground GC owns its actual RS chain.
		}
		if pod, ok := object.(*corev1.Pod); ok && servingstate.WorkloadOwner(pod.OwnerReferences) == "" {
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
	return o.retireWorkloadSources(uid)
}

func (o *Observer) retireWorkloadSources(uid types.UID) (bool, error) {
	for id, source := range o.sources {
		if source.ownerUID == uid && !source.stopped {
			return false, fmt.Errorf("physical executor %s termination unconfirmed", id)
		}
	}
	if err := o.publishLedger(nil); err != nil {
		return false, err
	}
	for key, source := range o.sources {
		if source.ownerUID != uid {
			continue
		}
		if err := o.runtime.RetirePod(string(source.uid)); err != nil {
			return false, err
		}
		source.stopFollow()
		delete(o.sources, key)
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
		if source := o.sources[string(pod.UID)]; source != nil {
			source.ownerUID = uid
		}
	}
	return nil
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
		if owner := servingstate.WorkloadOwner(refs); owner != "" {
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
			o.runtime.DisableInstance(instance.ID, "NodePowerTransition")
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
