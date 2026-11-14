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

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
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

const (
	testReplacementUID            = "replacement"
	testUnprobedReplacementReason = "replacement-not-probed"
)

func observerSourceFixture() (*corev1.Pod, *corev1.Node, serving.Profile, edgev1alpha1.DeviceClass) {
	image := "registry/native@sha256:" + strings.Repeat("a", 64)
	profile := serving.Profile{ID: "chill-system/campaign/campaign-uid", NodeName: "node", RuntimeMetadata: &native.RuntimeMetadata{Runtime: "ort-cpu", IntraOpThreads: 1, InterOpThreads: 1, CPUAffinity: []int{0}}, Condition: profilederivation.CurvePlanSpec{NodeUID: "node-uid", DeviceClass: "cpu", RuntimeFamily: "firstfloor", Backend: "CPUExecutionProvider", RuntimeImage: image, PowerMode: "fixed", Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 4}, Artifact: profilederivation.CurveArtifactContract{Image: image, Files: []string{"/artifact/model.onnx"}}, Preprocessor: profilederivation.CurvePreprocessorContract{Image: image, Path: "/preprocessor/pre"}}, Batches: []serving.BatchProfile{{Batch: 1, MemoryBytes: 1024}}}
	profile.Batches[0].InputSlopeJoules, profile.Power = catalogTestSlope("1"), catalogTestPower("1")
	profile.Condition.LogicalModel = "mobilenet"
	profile.Condition.Actor.Model = profile.Condition.LogicalModel
	class := edgev1alpha1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "cpu"}, Spec: edgev1alpha1.DeviceClassSpec{NodeSelector: map[string]string{"hardware": "cpu"}, Architecture: "amd64", PowerModes: []edgev1alpha1.PowerMode{{Name: "fixed", Actuator: edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed}}}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid", Labels: map[string]string{"hardware": "cpu", corev1.LabelArchStable: "amd64"}, Annotations: map[string]string{chillmeta.OnlineCPUs: "0-3"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceCPU: resource.MustParse("4")}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "chill-system", UID: "pod-uid", Annotations: map[string]string{servingstate.ServingProfileAnnotation: profile.ID, servingstate.ServingWeightAnnotation: "1"}, Labels: map[string]string{"app.kubernetes.io/component": "serving-instance"}}, Spec: corev1.PodSpec{NodeName: node.Name, Volumes: []corev1.Volume{{Name: "artifact", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "preprocessor", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}, InitContainers: []corev1.Container{{Name: "artifact", Image: image, Command: []string{"sh", "-ec"}, Args: []string{`cp "$@" /chill-artifact/`, "--", "/artifact/model.onnx"}, VolumeMounts: []corev1.VolumeMount{{Name: "artifact", MountPath: "/chill-artifact"}}}, {Name: "preprocessor", Image: image, Command: []string{"sh", "-ec"}, Args: []string{`cp -R "$1"/. /chill-preprocessor/`, "--", "/preprocessor/pre"}, VolumeMounts: []corev1.VolumeMount{{Name: "preprocessor", MountPath: "/chill-preprocessor"}}}, {Name: "profile", Image: image}}, Containers: []corev1.Container{{Name: "firstfloor", Image: image, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1024")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1024")}}, VolumeMounts: []corev1.VolumeMount{{Name: "artifact", MountPath: "/model", ReadOnly: true}, {Name: "preprocessor", MountPath: "/preprocessor", ReadOnly: true}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "firstfloor", ImageID: image, ContainerID: "containerd://running", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}, InitContainerStatuses: []corev1.ContainerStatus{{Name: "artifact", ImageID: image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, {Name: "preprocessor", ImageID: image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, {Name: "profile", ImageID: image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
	pod.Labels[servingstate.InstancePodUIDLabel] = string(pod.UID)
	pod.Annotations[servingstate.ServingRoutingAnnotation] = "{}"
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	return pod, node, profile, class
}

func observerServiceFixture(pod *corev1.Pod) *corev1.Service {
	service := servingstate.InstanceService(pod)
	service.Spec.ClusterIP = "10.96.0.10"
	return service
}

func TestObserverSourceRequiresActualMatchingFacts(t *testing.T) {
	pod, node, profile, class := observerSourceFixture()
	spec, err := sourceSpec(pod, node, &profile, []edgev1alpha1.DeviceClass{class}, "http://10.96.0.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	if spec.InstanceID != "pod-uid/containerd://running" || spec.Endpoint != "http://10.96.0.10:8080" || spec.Capacity != 4 || spec.Routing == nil || len(spec.Routing) != 0 {
		t.Fatalf("wrong physical source: %+v", spec)
	}
	routed := pod.DeepCopy()
	routed.Annotations[servingstate.ServingRoutingAnnotation] = `{"tight":2}`
	if spec, err = sourceSpec(routed, node, &profile, []edgev1alpha1.DeviceClass{class}, "http://10.96.0.10:8080"); err != nil || !maps.Equal(spec.Routing, map[string]int32{"tight": 2}) {
		t.Fatalf("applied routing not restored: %+v %v", spec.Routing, err)
	}
	const measuredImage = "registry/native:measured"
	for _, imageID := range []string{"", "opaque-runtime-image", "containerd://sha256:rebuilt"} {
		actual := pod.DeepCopy()
		actual.Spec.Containers[0].Image = measuredImage
		actual.Spec.InitContainers[2].Image = measuredImage
		actual.Status.ContainerStatuses[0].ImageID = imageID
		measured := profile
		measured.Condition.RuntimeImage = measuredImage
		if _, err = sourceSpec(actual, node, &measured, []edgev1alpha1.DeviceClass{class}, "http://10.96.0.10:8080"); err != nil {
			t.Fatalf("runtime image observation %q gated a measured tag: %v", imageID, err)
		}
	}
	cases := []struct {
		name   string
		change func(*corev1.Pod, *corev1.Node, *serving.Profile)
	}{
		{"node recreation", func(_ *corev1.Pod, n *corev1.Node, _ *serving.Profile) { n.UID = testReplacementUID }},
		{"architecture", func(_ *corev1.Pod, n *corev1.Node, _ *serving.Profile) { n.Status.NodeInfo.Architecture = "arm64" }},
		{"explicit weight", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			delete(p.Annotations, servingstate.ServingWeightAnnotation)
		}},
		{"applied routing", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Annotations[servingstate.ServingRoutingAnnotation] = "null"
		}},
		{"applied routing absent", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			delete(p.Annotations, servingstate.ServingRoutingAnnotation)
		}},
		{"applied routing names two Workloads", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Annotations[servingstate.ServingRoutingAnnotation] = `{"tight":1,"relaxed":1}`
		}},
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
		{"init CPU admission injection", func(p *corev1.Pod, _ *corev1.Node, _ *serving.Profile) {
			p.Spec.InitContainers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}
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
			if _, e := sourceSpec(p, n, &pr, []edgev1alpha1.DeviceClass{c}, "http://10.96.0.10:8080"); e == nil {
				t.Fatal("unconfirmed source became eligible")
			}
		})
	}
	restarted := pod.DeepCopy()
	restarted.Status.ContainerStatuses[0].ContainerID = "containerd://replacement"
	replacement, err := sourceSpec(restarted, node, &profile, []edgev1alpha1.DeviceClass{class}, "http://10.96.0.10:8080")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.InstanceID == spec.InstanceID || servingstate.SameRunningContainer(pod, restarted) {
		t.Fatal("container restart aliased the old physical executor")
	}
}

func TestObserverPowerUsesActualReadBack(t *testing.T) {
	_, node, _, class := observerSourceFixture()
	target := edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 2}}
	other := edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0}}
	class.Spec.PowerModes = []edgev1alpha1.PowerMode{{Name: "mode", Actuator: target}, {Name: "other", Actuator: other}}
	node.Labels[chillmeta.PowerMode] = "mode"
	if err := confirmedPower(node, &class, "mode"); err == nil {
		t.Fatal("desired mode was treated as actual power")
	}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	node.Annotations[chillmeta.PowerActuator] = string(raw)
	if err := confirmedPower(node, &class, "mode"); err != nil {
		t.Fatal(err)
	}
	node.Labels[chillmeta.PowerModeState] = chillmeta.PowerModePending
	if err := confirmedPower(node, &class, "mode"); err == nil {
		t.Fatal("read-back hid a switch in progress")
	}
	delete(node.Labels, chillmeta.PowerModeState)
	node.Annotations[chillmeta.PowerActuator] = nvpModel0
	if err := confirmedPower(node, &class, "mode"); err == nil {
		t.Fatal("read-back of another mode confirmed this one")
	}
	if err := confirmedPower(node, &class, "other"); err != nil {
		t.Fatalf("read-back not mapped to its DeviceClass mode: %v", err)
	}
}

func testExecutionFacts() *corev1.ConfigMap {
	_, _, profile, _ := observerSourceFixture()
	payload, _ := json.Marshal(map[string]any{
		"schemaVersion": "chill.dacs.io/runtime-declaration.v1alpha2", "image": profile.Condition.RuntimeImage,
		"architecture": "amd64", "runtime": map[string]any{"family": "firstfloor", "backends": []string{"CPUExecutionProvider", "ort-cpu", "rknnlite"}},
		"verification": "exact-image-runtime-inspection-v1",
	})
	document, _ := json.Marshal(map[string]any{
		"schemaVersion": "chill.dacs.io/profiler-execution-facts.v1alpha8", "metricObserverImage": profile.Condition.RuntimeImage,
		"declarations": []any{map[string]any{"deviceClass": "cpu", "runtime": map[string]string{"payload": string(payload)}}},
	})
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "execution-facts", Namespace: "chill-system", UID: "facts-uid", ResourceVersion: "1"},
		Data: map[string]string{"execution-facts.yaml": string(document)}}
}

func testObserverConfig(store *Store) ObserverConfig {
	return ObserverConfig{Namespace: "chill-system", RuntimeImage: func(ctx context.Context, profile *serving.Profile) (string, error) {
		c := profile.Condition
		return profilerkube.ResolveRuntimeImage(ctx, store.client, profilerkube.CandidateResolverOptions{
			Namespace: "chill-system", ConfigMapName: "execution-facts", ConfigMapKey: "execution-facts.yaml",
		}, c.DeviceClass, profile.NodeName, c.RuntimeFamily, c.Backend)
	}}
}

func newTestObserver(t *testing.T, objects ...client.Object) *Observer {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	objects = append(objects, testExecutionFacts())
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithObjects(objects...).Build()}
	manager, _ := newManagerHarness(t)
	kube := kubefake.NewSimpleClientset()
	cluster := NewClusterState(store, kube, testObserverConfig(store).Namespace)
	observer, err := NewObserver(cluster, store, kube, manager.runtime, &firstfloor.Client{}, testObserverConfig(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	return observer
}

func TestObserverTerminationRequiresExactContainerEvidence(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	observer := newTestObserver(t)
	observer.sources[string(pod.UID)] = &nativeSource{uid: pod.UID, containerID: "containerd://old"}
	pod.Status.ContainerStatuses[0].Ready = false
	observer.observeTermination(pod)
	if observer.sources[string(pod.UID)].stopped {
		t.Fatal("readiness loss became stop proof")
	}
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ContainerID: "containerd://unrelated"}
	observer.observeTermination(pod)
	if observer.sources[string(pod.UID)].stopped {
		t.Fatal("different container stopped retained executor")
	}
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.ContainerID = "containerd://old"
	observer.observeTermination(pod)
	if !observer.sources[string(pod.UID)].stopped {
		t.Fatal("native previous-container termination was lost")
	}
}

func TestObserverWatchInvalidationSurvivesLedgerPublicationAC37(t *testing.T) {
	for _, condition := range []string{"container unready", "pod unready", "deleting"} {
		t.Run(condition, func(t *testing.T) {
			pod, _, _, _ := observerSourceFixture()
			observer := newTestObserver(t)
			observer.restoreSources([]corev1.Pod{*pod})
			source := observer.sources[string(pod.UID)]
			source.instance.State.Ready = true
			switch condition {
			case "container unready":
				pod.Status.ContainerStatuses[0].Ready = false
			case "pod unready":
				pod.Status.Conditions = nil
			case "deleting":
				now := metav1.Now()
				pod.DeletionTimestamp = &now
			}
			w := watch.NewRaceFreeFake()
			w.Modify(pod)
			w.Stop()
			observer.wg.Add(1)
			observer.watchPods(w)
			if source.instance.State.Ready || source.stopped {
				t.Fatal("watch must close dispatch eligibility without inventing termination")
			}
			if err := observer.publishLedger(nil); err != nil {
				t.Fatal(err)
			}
			for _, instance := range observer.runtime.Instances() {
				if instance.Ready {
					t.Fatal("ledger publication reactivated an invalidated source")
				}
			}
		})
	}
}

func TestSourceFinalizerDeltaSurvivesStatusRace(t *testing.T) {
	for _, present := range []bool{true, false} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("present=%v/replacement=%v", present, replacement), func(t *testing.T) {
				ctx := context.Background()
				pod, _, _, _ := observerSourceFixture()
				if !present {
					pod.Finalizers = []string{servingstate.SourceFinalizer}
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
				err := servingstate.PatchSourceFinalizer(ctx, c, pod, present)
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
				if !replacement && slices.Contains(current.Finalizers, servingstate.SourceFinalizer) != present {
					t.Fatalf("source finalizer presence = %v, want %v", current.Finalizers, present)
				}
				if replacement && (current.UID != "replacement-pod" || slices.Contains(current.Finalizers, servingstate.SourceFinalizer) == present) {
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
	if current.DeletionTimestamp == nil || !slices.Contains(current.Finalizers, servingstate.SourceFinalizer) {
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
	owned, err := observer.store.OwnedResources(context.Background(), uid, observer.config.Namespace)
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
		if object.GetDeletionTimestamp() != nil || slices.Contains(object.GetFinalizers(), servingstate.SourceFinalizer) {
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
	observer := newTestObserver(t)
	observer.sources[string(pod.UID)] = &nativeSource{uid: pod.UID, containerID: status.ContainerID}
	resources := map[types.UID]client.Object{replicaSet.UID: replicaSet, deployment.UID: deployment}
	if _, publish, probed := observer.probeSource(context.Background(), pod, nil, nil, nil, resources, nil, nil); publish || probed != nil {
		t.Fatal("unlabelled retained source became eligible")
	}
	if source := observer.sources[string(pod.UID)]; source.ownerUID != uid || !source.stopped {
		t.Fatal("serving label filter hid parent ownership or exact termination evidence")
	}
}

func TestObserverReconcileRetainedSourceWithoutPodSnapshot(t *testing.T) {
	for _, missing := range []string{"absent", testReplacementUID, "read failure"} {
		t.Run(missing, func(t *testing.T) {
			pod, _, _, _ := observerSourceFixture()
			observedAt := time.Now().Add(-time.Minute)
			source := &nativeSource{
				pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
				uid: pod.UID, containerID: servingstate.FirstfloorStatus(pod).ContainerID, ownerUID: "workload-uid",
				instance: serving.ReadyInstance{State: serving.InstanceState{
					Ready: true, NativeVerified: true, RecoveryUnknown: true, ObservedAt: observedAt,
				}},
			}
			observer := newTestObserver(t)
			observer.sources[string(source.uid)] = source
			independent := &nativeSource{uid: "independent", instance: serving.ReadyInstance{State: serving.InstanceState{Ready: true, NativeVerified: true}}}
			observer.sources[string(independent.uid)] = independent
			kube := kubefake.NewSimpleClientset()
			var replacement *nativeSource
			if missing == testReplacementUID {
				pod.UID = testReplacementUID
				if _, err := kube.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				replacement = &nativeSource{pod: source.pod, uid: pod.UID, instance: serving.ReadyInstance{State: serving.InstanceState{Ready: true, NativeVerified: true, Reason: testUnprobedReplacementReason}}}
				observer.sources[string(replacement.uid)] = replacement
			}
			if missing == "read failure" {
				kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("retained Pod read unavailable")
				})
			}
			observer.kube = kube
			observer.reconcileSources(context.Background(), map[types.UID]bool{source.uid: false, independent.uid: true})
			state := source.instance.State
			if state.Ready || state.NativeVerified || state.Reason == "" || !state.AttemptedAt.After(observedAt) {
				t.Fatalf("retained executor stayed available without current proof: %+v", state)
			}
			if source.podState != nil || source.stopped || source.deleted || source.ownerUID != "workload-uid" ||
				!state.RecoveryUnknown || !state.ObservedAt.Equal(observedAt) || observer.sources[string(source.uid)] != source {
				t.Fatalf("unavailability discarded retained responsibility: %+v", source)
			}
			if !independent.instance.State.Ready || !independent.instance.State.NativeVerified {
				t.Fatal("retained-source failure invalidated independent verified source")
			}
			if replacement != nil && (!replacement.instance.State.Ready || !replacement.instance.State.NativeVerified || replacement.instance.State.Reason != testUnprobedReplacementReason) {
				t.Fatal("old refresh acted on replacement UID")
			}
			issues := observer.Issues()
			if len(issues) != 1 || issues[0].Source != source.pod.String() {
				t.Fatalf("retained-source identity missing from issue: %+v", issues)
			}
		})
	}
}

func TestObserverFilteredRefreshPreservesExactRetainedTermination(t *testing.T) {
	for _, watching := range []bool{false, true} {
		for _, proof := range []string{"exact", "replacement Pod", "other container"} {
			t.Run(fmt.Sprintf("watching=%v/%s", watching, proof), func(t *testing.T) {
				pod, _, _, _ := observerSourceFixture()
				source := &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: pod.Status.ContainerStatuses[0].ContainerID, podState: pod.DeepCopy()}
				pod.Labels = nil
				pod.Status.Phase = corev1.PodSucceeded
				status := &pod.Status.ContainerStatuses[0]
				status.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: status.ContainerID}}
				switch proof {
				case "replacement Pod":
					pod.UID = testReplacementUID
				case "other container":
					status.State.Terminated.ContainerID = "containerd://other"
				}
				observer := newTestObserver(t)
				observer.sources[string(source.uid)] = source
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
				confirmed := false
				for _, state := range observer.runtime.Instances() {
					if state.PodUID == string(source.uid) {
						confirmed = state.Terminated
						if state.Terminated && (state.Ready || !state.Stopped) {
							t.Fatal("terminal physical ledger remained executable")
						}
					}
				}
				if confirmed != (proof == "exact") {
					t.Fatalf("ledger terminal proof = %v with %s", confirmed, proof)
				}
			})
		}
	}
}

func TestObserverTerminalFactIsPerContainerAC45(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	current := pod.Status.ContainerStatuses[0].ContainerID
	previous := "containerd://previous"
	pod.Status.Phase = corev1.PodFailed
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: current}}
	observer := newTestObserver(t)
	source := &nativeSource{uid: pod.UID, containerID: current, podState: pod,
		terminated: map[string]bool{previous: false},
		previous: map[string]serving.ReadyInstance{
			previous: {State: serving.InstanceState{ID: servingstate.PhysicalID(pod.UID, previous)}},
		}}
	observer.sources[string(pod.UID)] = source
	observer.observeTermination(pod)
	if source.stopped {
		t.Fatal("one container's termination proved the entire Pod stopped")
	}
	if err := observer.publishLedger(nil); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, state := range observer.runtime.Instances() {
		seen[state.ID] = state.Terminated
		if state.Terminated && (state.Ready || !state.Stopped) {
			t.Fatal("terminated container remained executable")
		}
	}
	if !seen[servingstate.PhysicalID(pod.UID, current)] || seen[servingstate.PhysicalID(pod.UID, previous)] {
		t.Fatalf("terminal proof crossed physical identities: %v", seen)
	}
}

func TestObserverRefreshDoesNotWaitForBlockedLogStream(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	containerID := pod.Status.ContainerStatuses[0].ContainerID
	observer := newTestObserver(t)
	observer.sources[string(pod.UID)] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: containerID}
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
	source := observer.sources[string(pod.UID)]
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
	id := "pod-uid/containerd://actual"
	for _, event := range []serving.RequestEvent{
		{RequestID: requestID, WorkloadName: definition.Name, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "received", At: now, ReceivedAt: now},
		{RequestID: requestID, WorkloadName: definition.Name, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "accepted", At: now, ReceivedAt: now},
		{RequestID: requestID, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "assigned", At: now, ReceivedAt: now,
			Assignment: &serving.Assignment{Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Instance: id, DispatchedAt: now}},
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
	stream := io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	observer := manager.observer
	observer.mu.Lock()
	observer.sources[strings.SplitN(id, "/", 2)[0]] = &nativeSource{}
	observer.mu.Unlock()
	if err := observer.scanLogs(id, stream); err != nil {
		t.Fatal(err)
	}
	result, err := manager.observationLog.Query(context.Background(), definition.UID, now.Add(-time.Second), now.Add(time.Second), nil, nil, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Requests) != 1 {
		t.Fatalf("native replay duplicated the HTTP request: %+v", result.Requests)
	}
	for _, row := range result.Requests {
		if row.QueuedNanos != nil || row.RunningNanos != nil || row.ExecutedNanos != nil ||
			row.NativeQueuedAt != nil || row.NativeRunningAt != nil || row.NativeFinishedAt != nil || row.NativeExecutionComplete {
			t.Fatalf("settled request gained replayed progress: %+v", row)
		}
	}
	if result.Outcomes["completed"] != 1 {
		t.Fatalf("response outcome changed: %+v", result)
	}
	// The runtime no longer owns the Call, but the response row is still open.
	// Preserve all physical evidence separately without fabricating HTTP stages.
	nativeRows := 0
	_, err = manager.observationLog.ScanOccupancyRows(context.Background(), definition.UID, "node-uid", now.Add(-time.Second), now.Add(time.Second), func(row serving.RequestRow) {
		if row.Kind != serving.RequestRowNative {
			return
		}
		nativeRows++
		if row.RequestID != requestID || row.Instance != id || row.QueuedNanos != nil ||
			row.RunningNanos != nil || row.ExecutedNanos != nil || !row.ReceivedAt.IsZero() {
			t.Fatalf("native supplement invented legacy HTTP evidence: %+v", row)
		}
		if row.NativeQueuedAt == nil && row.NativeRunningAt == nil && row.NativeFinishedAt == nil {
			t.Fatalf("native supplement lost direct physical evidence: %+v", row)
		}
	})
	if err != nil || nativeRows != 3 {
		t.Fatalf("unknown-runtime physical evidence lost: rows=%d err=%v", nativeRows, err)
	}
}

func TestObserverRejectedProgressIdentifiesPhysicalRequest(t *testing.T) {
	observer := newTestObserver(t)
	id := "pod-uid/containerd://actual"
	stream := io.NopCloser(strings.NewReader(`CHILL_PROGRESS {"requestID":"request-1","phase":"fake"}` + "\n"))
	if err := observer.scanLogs(id, stream); err != nil {
		t.Fatal(err)
	}
	issues := observer.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected rejected progress diagnostic: %+v", issues)
	}
	for _, want := range []string{`requestID="request-1"`, `phase="fake"`, fmt.Sprintf("physicalID=%q", id)} {
		if !strings.Contains(issues[0].Reason, want) {
			t.Fatalf("rejection lost %s: %+v", want, issues)
		}
	}
}

// A refresh holds o.mu across API lists and native probes; progress lines
// reach the runtime meanwhile, and the issue they raise is visible.
func TestObserverScannerProgressDoesNotWaitForRefresh(t *testing.T) {
	manager, _ := newManagerHarness(t)
	observer := manager.observer
	stream := io.NopCloser(strings.NewReader(`CHILL_PROGRESS {"requestID":"request-1","phase":"fake"}` + "\n"))
	scanned := make(chan error, 1)
	observer.mu.Lock()
	go func() { scanned <- observer.scanLogs("pod-uid/containerd://actual", stream) }()
	var err error
	select {
	case err = <-scanned:
		observer.mu.Unlock()
	case <-time.After(5 * time.Second):
		observer.mu.Unlock()
		<-scanned
		t.Fatal("progress waited for the refresh lock")
	}
	if err != nil {
		t.Fatal(err)
	}
	if issues := observer.Issues(); len(issues) != 1 || issues[0].Source != "logs/pod-uid/containerd://actual" {
		t.Fatalf("invalid progress issue not recorded: %+v", issues)
	}
}

func TestObserverTerminalPreparationDoesNotInventAStartedExecutor(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	pod.Status.Phase = corev1.PodFailed
	pod.Status.InitContainerStatuses[0].State.Terminated.ExitCode = 1
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}
	pod.Status.ContainerStatuses[0].ContainerID = ""
	if !servingstate.PodTerminated(pod) {
		t.Fatal("failed preparation retained a never-created executor")
	}
	pod.Status.ContainerStatuses[0].ContainerID = "containerd://unknown"
	if servingstate.PodTerminated(pod) {
		t.Fatal("failed Pod phase was treated as proof that an unknown executor stopped")
	}
}

func TestObserverCleanupFollowsActualDeploymentOwnerChain(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "native", Namespace: pod.Namespace, UID: "deployment-uid", Labels: pod.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "Workload", UID: "workload-uid"}}}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "native-rs", Namespace: pod.Namespace, UID: "replicaset-uid", Labels: pod.Labels, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}}
	pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(replicaSet, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
	observer := newTestObserver(t, deployment, replicaSet, pod)
	objects, err := observer.store.OwnedResources(context.Background(), "workload-uid", observer.config.Namespace)
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

func TestObserverMeasuredImageStillRequiresPhysicalSafety(t *testing.T) {
	pod, node, profile, class := observerSourceFixture()
	if _, err := sourceSpec(pod, node, &profile, []edgev1alpha1.DeviceClass{class}, ""); err != nil {
		t.Fatal("measured executor rejected", err)
	}
	for _, name := range []string{"materializer failed", "Node", "GPU allocation"} {
		t.Run(name, func(t *testing.T) {
			p, n := pod.DeepCopy(), node.DeepCopy()
			pr := cloneCatalogProfile(profile)
			switch name {
			case "materializer failed":
				p.Status.InitContainerStatuses[2].State.Terminated.ExitCode = 1
			case "Node":
				n.UID = testReplacementUID
			case "GPU allocation":
				pr.RuntimeMetadata.GPUUUID = "GPU-measured"
			}
			if _, err := sourceSpec(p, n, &pr, []edgev1alpha1.DeviceClass{class}, ""); err == nil {
				t.Fatal("failed preparation or physical safety mismatch accepted")
			}
		})
	}
}

func TestNativeSourceRequiresPreparedMode(t *testing.T) {
	pod, _, _, _ := observerSourceFixture()
	for _, preparedMode := range []string{servingstate.NativeServingMode, servingstate.NativeProfilingMode} {
		for _, reportedMode := range []string{servingstate.NativeServingMode, servingstate.NativeProfilingMode} {
			t.Run(preparedMode+" reports "+reportedMode, func(t *testing.T) {
				prepared := &native.Config{Model: "model", Mode: preparedMode}
				instance := serving.InstanceState{Native: &native.State{Ready: true,
					Metadata: native.Metadata{Model: prepared.Model, Mode: reportedMode,
						ProfileID: pod.Annotations[servingstate.ServingProfileAnnotation]}}}
				if got := matchesNativeSource(pod, prepared, &instance); got != (preparedMode == reportedMode) {
					t.Fatalf("prepared mode %q, reported mode %q: verified=%t", preparedMode, reportedMode, got)
				}
			})
		}
	}
}
