package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/watch"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const newTestResourceName = "new"

func observerSourceFixture() (*corev1.Pod, *corev1.Node, serving.Profile, edgev1alpha1.DeviceClass) {
	image := "registry/native@sha256:" + strings.Repeat("a", 64)
	profile := serving.Profile{ID: "chill-system/campaign/campaign-uid", NodeName: "node", RuntimeMetadata: &native.RuntimeMetadata{Runtime: "ort-cpu", IntraOpThreads: 1, InterOpThreads: 1, CPUAffinity: []int{0}}, Condition: profilederivation.CurvePlanSpec{NodeUID: "node-uid", DeviceClass: "cpu", RuntimeFamily: "firstfloor", Backend: "CPUExecutionProvider", RuntimeImage: image, PowerMode: "fixed", Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 4}, CPU: profilederivation.CurveCPUContract{Request: "1", Limit: "1", ORTIntraOpThreads: 1, ORTInterOpThreads: 1}, Artifact: profilederivation.CurveArtifactContract{Image: image, Files: []string{"/artifact/model.onnx"}}, Preprocessor: profilederivation.CurvePreprocessorContract{Image: image, Path: "/preprocessor/pre"}}, Batches: []serving.BatchProfile{{Batch: 1, MemoryBytes: 1024}}}
	class := edgev1alpha1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "cpu"}, Spec: edgev1alpha1.DeviceClassSpec{NodeSelector: map[string]string{"hardware": "cpu"}, Architecture: "amd64", PowerModes: []edgev1alpha1.PowerMode{{Name: "fixed", Actuator: edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed}}}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid", Labels: map[string]string{"hardware": "cpu", corev1.LabelArchStable: "amd64"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceCPU: resource.MustParse("4")}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "chill-system", UID: "pod-uid", Annotations: map[string]string{ServingProfileAnnotation: profile.ID, ServingWeightAnnotation: "1"}, Labels: map[string]string{"app.kubernetes.io/component": "serving-instance"}}, Spec: corev1.PodSpec{NodeName: node.Name, Volumes: []corev1.Volume{{Name: "artifact", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "preprocessor", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}, InitContainers: []corev1.Container{{Name: "artifact", Image: image, Command: []string{"sh", "-ec"}, Args: []string{`cp "$@" /chill-artifact/`, "--", "/artifact/model.onnx"}, VolumeMounts: []corev1.VolumeMount{{Name: "artifact", MountPath: "/chill-artifact"}}}, {Name: "preprocessor", Image: image, Command: []string{"sh", "-ec"}, Args: []string{`cp -R "$1"/. /chill-preprocessor/`, "--", "/preprocessor/pre"}, VolumeMounts: []corev1.VolumeMount{{Name: "preprocessor", MountPath: "/chill-preprocessor"}}}, {Name: "profile", Image: image}}, Containers: []corev1.Container{{Name: "firstfloor", Image: image, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1024")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1024")}}, VolumeMounts: []corev1.VolumeMount{{Name: "artifact", MountPath: "/model", ReadOnly: true}, {Name: "preprocessor", MountPath: "/preprocessor", ReadOnly: true}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "firstfloor", ImageID: image, ContainerID: "containerd://running", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}, InitContainerStatuses: []corev1.ContainerStatus{{Name: "artifact", ImageID: image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, {Name: "preprocessor", ImageID: image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, {Name: "profile", ImageID: image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
	pod.Labels[instancePodUIDLabel] = string(pod.UID)
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	return pod, node, profile, class
}

func observerServiceFixture(pod *corev1.Pod) *corev1.Service {
	service := instanceService(pod)
	service.Spec.ClusterIP = "10.96.0.10"
	return service
}

func TestObserverSourceRequiresActualMatchingFacts(t *testing.T) {
	pod, node, profile, class := observerSourceFixture()
	spec, err := sourceSpec(pod, node, &profile, []edgev1alpha1.DeviceClass{class}, nil, "http://10.96.0.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	if spec.InstanceID != "pod-uid/containerd://running" || spec.Endpoint != "http://10.96.0.10:8080" || spec.Capacity != 4 || spec.Routing != nil {
		t.Fatalf("wrong physical source: %+v", spec)
	}
	routed := pod.DeepCopy()
	routed.Annotations[ServingRoutingAnnotation] = `{"tight":2}`
	if spec, err = sourceSpec(routed, node, &profile, []edgev1alpha1.DeviceClass{class}, nil, "http://10.96.0.10:8080"); err != nil || !maps.Equal(spec.Routing, map[string]int32{"tight": 2}) {
		t.Fatalf("applied routing not restored: %+v %v", spec.Routing, err)
	}
	opaque := pod.DeepCopy()
	opaque.Status.ContainerStatuses[0].ImageID = "opaque-runtime-image"
	opaque.Status.InitContainerStatuses[0].ImageID = "opaque-artifact-image"
	if _, err = sourceSpec(opaque, node, &profile, []edgev1alpha1.DeviceClass{class}, nil, "http://10.96.0.10:8080"); err != nil {
		t.Fatal("actual ImageID was incorrectly used as a digest admission gate", err)
	}
	cases := []struct {
		name   string
		change func(*corev1.Pod, *corev1.Node, *serving.Profile)
	}{
		{"node recreation", func(_ *corev1.Pod, n *corev1.Node, _ *serving.Profile) { n.UID = "replacement" }},
		{"architecture", func(_ *corev1.Pod, n *corev1.Node, _ *serving.Profile) { n.Status.NodeInfo.Architecture = "arm64" }},
		{"explicit weight", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			delete(p.Annotations, ServingWeightAnnotation)
		}},
		{"applied routing", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Annotations[ServingRoutingAnnotation] = "null"
		}},
		{"image evidence missing", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) { p.Status.ContainerStatuses[0].ImageID = "" }},
		{"configured image mismatch", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) { p.Spec.Containers[0].Image = "other-image" }},
		{"artifact read only", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Spec.Containers[0].VolumeMounts[0].ReadOnly = false
		}},
		{"preprocessor read only", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Spec.Containers[0].VolumeMounts[1].ReadOnly = false
		}},
		{"preprocessor source", func(_ *corev1.Pod, _ *corev1.Node, p *serving.Profile) {
			p.Condition.Preprocessor.Path = "/preprocessor/other"
		}},
		{"CPU", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse("2")
		}},
		{"memory", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("512")
		}},
		{"legacy profile", func(_ *corev1.Pod, _ *corev1.Node, p *serving.Profile) { p.Condition.RuntimeFamily = "ort" }},
		{"no identity", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Status.ContainerStatuses[0].ContainerID = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, n, pr, c := observerSourceFixture()
			tc.change(p, n, &pr)
			if _, e := sourceSpec(p, n, &pr, []edgev1alpha1.DeviceClass{c}, nil, "http://10.96.0.10:8080"); e == nil {
				t.Fatal("unconfirmed source became eligible")
			}
		})
	}
	restarted := pod.DeepCopy()
	restarted.Status.ContainerStatuses[0].ContainerID = "containerd://replacement"
	replacement, err := sourceSpec(restarted, node, &profile, []edgev1alpha1.DeviceClass{class}, nil, "http://10.96.0.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.InstanceID == spec.InstanceID || sameRunningContainer(pod, restarted) {
		t.Fatal("container restart aliased the old physical executor")
	}
}

func TestObserverPowerUsesLatestActualReadBack(t *testing.T) {
	_, node, _, class := observerSourceFixture()
	target := edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 2}}
	class.Spec.PowerModes = []edgev1alpha1.PowerMode{{Name: "mode", Actuator: target}}
	now := metav1.NewMicroTime(time.Now().UTC())
	record := edgev1alpha1.NodeActuation{ObjectMeta: metav1.ObjectMeta{Name: "actuation", Generation: 1}, Spec: edgev1alpha1.NodeActuationSpec{Node: edgev1alpha1.ProfilingRunObjectReference{Name: node.Name, UID: string(node.UID)}, DeviceClass: class.Name, PowerMode: "mode", Actuator: target, StageRequestedAt: metav1.NewTime(now.Time)}, Status: edgev1alpha1.NodeActuationStatus{ObservedGeneration: 1, Phase: edgev1alpha1.NodeActuationPhaseReady, VerifiedAt: &now, ReadyAt: &now, TargetObservation: &edgev1alpha1.NodeActuationObservation{PowerMode: "mode", Actuator: target, ObservedAt: now}}}
	if err := confirmedPower(node, &class, "mode", nil); err == nil {
		t.Fatal("desired mode was treated as actual power")
	}
	if err := confirmedPower(node, &class, "mode", []edgev1alpha1.NodeActuation{record}); err != nil {
		t.Fatal(err)
	}
	pending := record.DeepCopy()
	pending.Name = newTestResourceName
	pending.Spec.StageRequestedAt = metav1.NewTime(now.Add(time.Second))
	pending.Status.Phase = edgev1alpha1.NodeActuationPhasePending
	if err := confirmedPower(node, &class, "mode", []edgev1alpha1.NodeActuation{record, *pending}); err == nil {
		t.Fatal("stale Ready evidence hid a current transition")
	}
	record.Status.TargetObservation.PowerMode = "other"
	if err := confirmedPower(node, &class, "mode", []edgev1alpha1.NodeActuation{record}); err == nil {
		t.Fatal("desired target hid an actual mode mismatch")
	}
}

func newTestObserver(t *testing.T, objects ...client.Object) *Observer {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithObjects(objects...).Build()}
	observer, err := NewObserver(store, kubefake.NewSimpleClientset(), &serving.Runtime{}, &firstfloor.Client{}, ObserverConfig{Namespace: "chill-system"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	return observer
}

func TestObserverTerminationRequiresExactContainerEvidence(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	observer := newTestObserver(t)
	id := physicalID(pod.UID, "containerd://old")
	observer.sources[id] = &nativeSource{uid: pod.UID, containerID: "containerd://old"}
	pod.Status.ContainerStatuses[0].Ready = false
	observer.observeTermination(pod)
	if observer.sources[id].stopped {
		t.Fatal("readiness loss became stop proof")
	}
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ContainerID: "containerd://unrelated"}
	observer.observeTermination(pod)
	if observer.sources[id].stopped {
		t.Fatal("different container stopped retained executor")
	}
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.ContainerID = "containerd://old"
	observer.observeTermination(pod)
	if !observer.sources[id].stopped {
		t.Fatal("native previous-container termination was lost")
	}
}

func TestSourceFinalizerDeltaSurvivesStatusRace(t *testing.T) {
	for _, present := range []bool{true, false} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("present=%v/replacement=%v", present, replacement), func(t *testing.T) {
				ctx := context.Background()
				pod, _, _, _ := observerSourceFixture()
				if !present {
					pod.Finalizers = []string{sourceFinalizer}
				}
				scheme := runtime.NewScheme()
				if err := corev1.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithObjects(pod).
					WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
						current := &corev1.Pod{}
						if err := c.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
							return err
						}
						// A real status-subresource write lands after the caller's read.
						current.Status.Message = "kubelet status advanced"
						if err := c.Status().Update(ctx, current); err != nil {
							return err
						}
						current.Finalizers = append(current.Finalizers, "other.example/protection")
						if replacement {
							current.UID = "replacement-pod"
						}
						if err := c.Update(ctx, current); err != nil {
							return err
						}
						// The fake client applies strategic patches but omits immutable
						// UID validation. Exercise Kubernetes' actual validation too.
						before, err := json.Marshal(current)
						if err != nil {
							return err
						}
						delta, err := patch.Data(object)
						if err != nil {
							return err
						}
						merged, err := strategicpatch.StrategicMergePatch(before, delta, corev1.Pod{})
						if err != nil {
							return err
						}
						updated := &corev1.Pod{}
						if err := json.Unmarshal(merged, updated); err != nil {
							return err
						}
						if errs := apivalidation.ValidateObjectMetaUpdate(&updated.ObjectMeta, &current.ObjectMeta, field.NewPath("metadata")); len(errs) != 0 {
							return apierrors.NewInvalid(schema.GroupKind{Kind: "Pod"}, current.Name, errs)
						}
						return c.Patch(ctx, object, patch, opts...)
					}}).Build()
				if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
					t.Fatal(err)
				}
				err := patchSourceFinalizer(ctx, c, pod, present)
				if replacement && !apierrors.IsInvalid(err) {
					t.Fatalf("replacement UID was not rejected: %v", err)
				}
				if !replacement && err != nil {
					t.Fatal(err)
				}
				current := &corev1.Pod{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
					t.Fatal(err)
				}
				if current.Status.Message != "kubelet status advanced" || !slices.Contains(current.Finalizers, "other.example/protection") {
					t.Fatal("metadata delta overwrote status or another finalizer")
				}
				if !replacement && slices.Contains(current.Finalizers, sourceFinalizer) != present {
					t.Fatalf("source finalizer presence = %v, want %v", current.Finalizers, present)
				}
				if replacement && (current.UID != "replacement-pod" || slices.Contains(current.Finalizers, sourceFinalizer) == present) {
					t.Fatal("failed UID precondition mutated replacement Pod")
				}
			})
		}
	}
}

func TestObserverCleanupHoldsTerminationAndPreservesSharedResources(t *testing.T) {
	ctx := context.Background()
	owned, _, _, _ := observerSourceFixture()
	uid := types.UID("workload-uid")
	owned.OwnerReferences = []metav1.OwnerReference{{APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "Workload", Name: "workload", UID: uid}}
	shared := owned.DeepCopy()
	shared.Name = "shared"
	shared.UID = "shared-uid"
	shared.OwnerReferences = nil
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "not-native-serving-capacity", Namespace: owned.Namespace, OwnerReferences: owned.OwnerReferences}}
	observer := newTestObserver(t, owned, shared, config)
	done, err := observer.CleanupWorkload(ctx, uid)
	if err != nil || done {
		t.Fatalf("cleanup before termination = %v, %v", done, err)
	}
	current := &corev1.Pod{}
	key := client.ObjectKeyFromObject(owned)
	if err = observer.store.client.Get(ctx, key, current); err != nil {
		t.Fatal(err)
	}
	if current.DeletionTimestamp == nil || !slices.Contains(current.Finalizers, sourceFinalizer) {
		t.Fatal("native termination evidence was not retained")
	}
	done, err = observer.CleanupWorkload(ctx, uid)
	if err != nil || done {
		t.Fatalf("unconfirmed termination completed cleanup: %v %v", done, err)
	}
	current.Status.Phase = corev1.PodSucceeded
	current.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: current.Status.ContainerStatuses[0].ContainerID}}
	if err = observer.store.client.Status().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	_, err = observer.CleanupWorkload(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err = observer.store.client.Get(ctx, key, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("terminated owned Pod remains: %v", err)
	}
	done, err = observer.CleanupWorkload(ctx, uid)
	if err != nil || !done {
		t.Fatalf("cleanup after disappearance = %v %v", done, err)
	}
	if err = observer.store.client.Get(ctx, client.ObjectKeyFromObject(shared), &corev1.Pod{}); err != nil {
		t.Fatal("shared resource was removed", err)
	}
	if err = observer.store.client.Get(ctx, client.ObjectKeyFromObject(config), &corev1.ConfigMap{}); err != nil {
		t.Fatal("hypothetical owned configuration was swept", err)
	}
	observer.sources["lost"] = &nativeSource{ownerUID: uid}
	if done, err = observer.CleanupWorkload(ctx, uid); err == nil || done {
		t.Fatal("disappearance without stop proof completed cleanup")
	}
}
func TestObserverOwnershipIncludesUnlabelledControllersAndDirectReplicaSets(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	uid := types.UID("workload-uid")
	workloadRef := []metav1.OwnerReference{{APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "Workload", Name: "workload", UID: uid}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: pod.Namespace, UID: "deployment-uid", OwnerReferences: workloadRef}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "replicas", Namespace: pod.Namespace, UID: "replicas-uid", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}}
	pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
	direct := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "direct", Namespace: pod.Namespace, UID: "direct-uid", OwnerReferences: workloadRef}}
	observer := newTestObserver(t, pod, deployment, rs, direct)
	owned, err := observer.ownedResources(context.Background(), uid)
	if err != nil || len(owned) != 4 {
		t.Fatalf("unlabelled ownership lost: %v %+v", err, owned)
	}
	all := map[types.UID]client.Object{rs.UID: rs, deployment.UID: deployment}
	if owner, err := resourceWorkloadOwner(pod, all); err != nil || owner != string(uid) {
		t.Fatalf("dedicated executor became shared: %q %v", owner, err)
	}
	delete(all, deployment.UID)
	if _, err := resourceWorkloadOwner(pod, all); err == nil {
		t.Fatal("missing owner became shared capacity")
	}
	pod.OwnerReferences[0].BlockOwnerDeletion = nil
	nonblocking := newTestObserver(t, pod, deployment, rs, direct)
	if done, err := nonblocking.CleanupWorkload(context.Background(), uid); err == nil || done {
		t.Fatal("nonblocking child chain was accepted for foreground cleanup")
	}
	retained := &appsv1.Deployment{}
	if err := nonblocking.store.client.Get(context.Background(), client.ObjectKeyFromObject(deployment), retained); err != nil || retained.DeletionTimestamp != nil {
		t.Fatalf("cleanup removed a parent before proving its child retention: %+v %v", retained, err)
	}
	if _, err = observer.CleanupWorkload(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if err = observer.store.client.Get(context.Background(), client.ObjectKeyFromObject(direct), &appsv1.ReplicaSet{}); !apierrors.IsNotFound(err) {
		t.Fatalf("direct ReplicaSet survives cleanup: %v", err)
	}
}

func TestObserverCleanupRejectsNonblockingReplicaSetParent(t *testing.T) {
	ctx := context.Background()
	pod, _, _, _ := observerSourceFixture()
	uid := types.UID("workload-uid")
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: pod.Namespace, UID: "deployment-uid",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "Workload", UID: uid}}}}
	parent := *metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))
	blocking := false
	parent.BlockOwnerDeletion = &blocking
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "replicas", Namespace: pod.Namespace, UID: "replicas-uid", OwnerReferences: []metav1.OwnerReference{parent}}}
	pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(replicaSet, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
	observer := newTestObserver(t, pod, replicaSet, deployment)
	if done, err := observer.CleanupWorkload(ctx, uid); err == nil || done {
		t.Fatal("nonblocking intermediate owner accepted foreground cleanup")
	}
	for _, object := range []client.Object{pod, replicaSet, deployment} {
		if err := observer.store.client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatal("cleanup removed a resource before validating the complete chain", err)
		}
		if object.GetDeletionTimestamp() != nil || slices.Contains(object.GetFinalizers(), sourceFinalizer) {
			t.Fatal("cleanup mutated a resource before validating the complete chain")
		}
	}
}

func TestObserverRefreshRetainsUnlabelledParentSource(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	uid := types.UID("workload-uid")
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "deployment", UID: "deployment-uid",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "Workload", UID: uid}}}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "replicas", UID: "replicas-uid",
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}}
	pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(replicaSet, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
	pod.Labels = nil
	status := &pod.Status.ContainerStatuses[0]
	status.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: status.ContainerID}}
	id := physicalID(pod.UID, status.ContainerID)
	observer := newTestObserver(t)
	observer.sources[id] = &nativeSource{uid: pod.UID, containerID: status.ContainerID}
	resources := map[types.UID]client.Object{replicaSet.UID: replicaSet, deployment.UID: deployment}
	if _, publish := observer.refreshSource(context.Background(), pod, nil, nil, nil, nil, resources); publish {
		t.Fatal("unlabelled retained source became eligible")
	}
	if source := observer.sources[id]; source.ownerUID != uid || !source.stopped {
		t.Fatal("serving label filter hid parent ownership or exact termination evidence")
	}
}

func TestObserverFilteredRefreshPreservesExactRetainedTermination(t *testing.T) {
	for _, watching := range []bool{false, true} {
		for _, proof := range []string{"exact", "replacement Pod", "other container"} {
			t.Run(fmt.Sprintf("watching=%v/%s", watching, proof), func(t *testing.T) {
				pod, _, _, _ := observerSourceFixture()
				id := physicalID(pod.UID, pod.Status.ContainerStatuses[0].ContainerID)
				source := &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: pod.Status.ContainerStatuses[0].ContainerID}
				pod.Labels = nil
				pod.Status.Phase = corev1.PodSucceeded
				status := &pod.Status.ContainerStatuses[0]
				status.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: status.ContainerID}}
				switch proof {
				case "replacement Pod":
					pod.UID = "replacement"
				case "other container":
					status.State.Terminated.ContainerID = "containerd://other"
				}
				observer := newTestObserver(t)
				observer.sources[id] = source
				kube := kubefake.NewSimpleClientset(pod)
				// The client-go fake otherwise ignores server-side field selection.
				kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					result := &corev1.PodList{}
					selector := action.(ktesting.ListAction).GetListRestrictions().Fields
					if selector.Matches(fields.Set{"status.phase": string(pod.Status.Phase)}) {
						result.Items = append(result.Items, *pod)
					}
					return true, result, nil
				})
				observer.kube = kube
				if watching {
					observer.podWatch = watch.NewFake()
				}
				if err := observer.Refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
				if source.stopped != (proof == "exact") {
					t.Fatalf("retained physical executor termination = %v with %s", source.stopped, proof)
				}
			})
		}
	}
}

func TestObserverRefreshDoesNotWaitForBlockedLogStream(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	containerID := pod.Status.ContainerStatuses[0].ContainerID
	id := physicalID(pod.UID, containerID)
	observer := newTestObserver(t)
	observer.sources[id] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: containerID}
	kube := kubefake.NewSimpleClientset(pod)
	opened := make(chan *corev1.PodLogOptions, 1)
	// A kubelet that never answers the log request until the observer closes.
	kube.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "log" {
			opened <- action.(ktesting.GenericAction).GetValue().(*corev1.PodLogOptions)
			<-observer.ctx.Done()
		}
		return false, nil, nil
	})
	observer.kube = kube
	refreshed := make(chan error, 1)
	go func() { refreshed <- observer.Refresh(context.Background()) }()
	var options *corev1.PodLogOptions
	select {
	case options = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("log follow not started")
	}
	select {
	case err := <-refreshed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Refresh waited for the log stream")
	}
	if !observer.mu.TryLock() {
		t.Fatal("opening log stream holds the observer lock")
	}
	source := observer.sources[id]
	following := source.follow != nil
	observer.mu.Unlock()
	if !following {
		t.Fatal("pending log follow lost")
	}
	if options.SinceTime == nil || !options.SinceTime.Time.Equal(processStart) || !options.Follow {
		t.Fatalf("restarted log follow replays earlier container output: %+v", options)
	}
}

func TestObserverScannerSkipsProgressForUnknownRequests(t *testing.T) {
	manager, _ := newManagerHarness(t)
	definition := &serving.Definition{Name: "observed", UID: "observed-uid", Generation: 1, Model: serving.Model{Name: "model", UID: "model-uid", Generation: 1}, SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := manager.observationLog.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	requestID := "request-1"
	for _, event := range []serving.RequestEvent{
		{RequestID: requestID, WorkloadName: definition.Name, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "received", At: now, ReceivedAt: now},
		{RequestID: requestID, WorkloadName: definition.Name, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "accepted", At: now, ReceivedAt: now},
		{RequestID: requestID, WorkloadName: definition.Name, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "response_finished", At: now.Add(time.Millisecond), ReceivedAt: now, ElapsedNanos: int64(time.Millisecond), Outcome: "completed"},
	} {
		if err := manager.observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	lines := []string{"unrelated firstfloor startup output"}
	for _, phase := range []string{"queued", "running", "finished"} {
		data, err := json.Marshal(native.Progress{RequestID: requestID, WorkloadUID: definition.UID, Definition: definition.Identity(), Phase: phase, At: now, ExecutionComplete: phase == "finished"})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, "CHILL_PROGRESS "+string(data))
	}
	lines = append(lines, `CHILL_PROGRESS {"requestID":"request-1","phase":"fake"}`)
	id := "pod-uid/containerd://actual"
	stream := io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	observer := manager.observer
	observer.mu.Lock()
	observer.sources[id] = &nativeSource{}
	observer.mu.Unlock()
	if err := observer.scanLogs(id, stream); err != nil {
		t.Fatal(err)
	}
	result, err := manager.observationLog.Query(definition.UID, now.Add(-time.Second), now.Add(time.Second), nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Requests {
		if row.QueuedNanos != nil || row.RunningNanos != nil || row.ExecutedNanos != nil {
			t.Fatalf("settled request gained replayed progress: %+v", row)
		}
	}
	if result.Outcomes["completed"] != 1 {
		t.Fatalf("response outcome changed: %+v", result)
	}
}

func TestObserverTerminalPreparationDoesNotInventAStartedExecutor(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	pod.Status.Phase = corev1.PodFailed
	pod.Status.InitContainerStatuses[0].State.Terminated.ExitCode = 1
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}
	pod.Status.ContainerStatuses[0].ContainerID = ""
	if !podTerminated(pod) {
		t.Fatal("failed preparation retained a never-created executor")
	}
	pod.Status.ContainerStatuses[0].ContainerID = "containerd://unknown"
	if podTerminated(pod) {
		t.Fatal("failed Pod phase was treated as proof that an unknown executor stopped")
	}
}

func TestObserverCleanupFollowsActualDeploymentOwnerChain(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "native", Namespace: pod.Namespace, UID: "deployment-uid", Labels: pod.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "Workload", UID: "workload-uid"}}}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "native-rs", Namespace: pod.Namespace, UID: "replicaset-uid", Labels: pod.Labels, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}}
	pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(replicaSet, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
	observer := newTestObserver(t, deployment, replicaSet, pod)
	objects, err := observer.ownedResources(context.Background(), "workload-uid")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 {
		t.Fatalf("actual native owner chain incomplete: %d", len(objects))
	}
	if _, err = observer.CleanupWorkload(context.Background(), "workload-uid"); err != nil {
		t.Fatal(err)
	}
	if err = observer.store.client.Get(context.Background(), client.ObjectKeyFromObject(replicaSet), &appsv1.ReplicaSet{}); err != nil {
		t.Fatal("RS was directly deleted instead of left to native Deployment GC", err)
	}
}
