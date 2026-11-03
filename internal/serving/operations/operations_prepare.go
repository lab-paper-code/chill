package operations

import (
	context "context"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	component "github.com/lab-paper-code/chill/internal/component"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	serving "github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	resource "k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	ptr "k8s.io/utils/ptr"
	reflect "reflect"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	slices "slices"
	strconv "strconv"
	strings "strings"
)

func (o *Operations) PrepareInstance(ctx context.Context, spec servingstate.PrepareSpec) (evidence *servingstate.PreparationEvidence, err error) {
	if err := ValidatePreparation(spec); err != nil {
		return nil, err
	}
	p := spec.Instance.Profile
	node := spec.Instance.Node
	desiredImage, err := o.Observer.CurrentRuntimeImage(ctx, p)
	if err != nil {
		return nil, err
	}
	host, delivery, err := measuredRuntimeDelivery(p)
	if err != nil {
		return nil, err
	}
	decisionID, err := o.EnsurePreparationConfig(ctx, spec, delivery)
	if err != nil {
		return nil, err
	}
	requests := core.ResourceList{core.ResourceMemory: *resource.NewQuantity(int64(spec.MemoryBytes), resource.BinarySI)}
	limits := core.ResourceList{core.ResourceMemory: *resource.NewQuantity(int64(spec.MemoryBytes), resource.BinarySI)}
	initResources := core.ResourceRequirements{Requests: requests, Limits: limits}
	if spec.Instance.Allocation.GPUUUID != "" {
		requests = requests.DeepCopy()
		limits = limits.DeepCopy()
		requests["nvidia.com/gpu"] = resource.MustParse("1")
		limits["nvidia.com/gpu"] = resource.MustParse("1")
	}
	if host.AcceleratorResource != "" {
		requests = requests.DeepCopy()
		limits = limits.DeepCopy()
		requests[host.AcceleratorResource] = resource.MustParse("1")
		limits[host.AcceleratorResource] = resource.MustParse("1")
	}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: o.Config.Namespace, Name: spec.Name, Labels: map[string]string{"app.kubernetes.io/component": servingstate.ServingInstanceComponent, profilingrun.MeasurementWorkloadLabelKey: profilingrun.ServingInstanceRole}, Annotations: map[string]string{servingstate.ServingProfileAnnotation: p.ID, servingstate.ServingWeightAnnotation: strconv.Itoa(int(spec.Instance.Weight)), servingstate.ServingPolicyAnnotation: decisionID, servingstate.ServingSlotAnnotation: spec.Instance.ID}, Finalizers: []string{servingstate.SourceFinalizer}}, Spec: core.PodSpec{NodeName: node.Name, RestartPolicy: core.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), InitContainers: []core.Container{
		{Name: "artifact", Image: p.Condition.Artifact.Image, Command: []string{"sh", "-ec"}, Args: append([]string{`cp "$@" /chill-artifact/`, "--"}, p.Condition.Artifact.Files...), Resources: initResources, VolumeMounts: []core.VolumeMount{{Name: "artifact", MountPath: "/chill-artifact"}}},
		{Name: "preprocessor", Image: p.Condition.Preprocessor.Image, Command: []string{"sh", "-ec"}, Args: []string{`cp -R "$1"/. /chill-preprocessor/`, "--", p.Condition.Preprocessor.Path}, Resources: initResources, VolumeMounts: []core.VolumeMount{{Name: "preprocessor", MountPath: "/chill-preprocessor"}}},
		{Name: "profile", Image: desiredImage, Command: []string{"/opt/chill/bin/serving-entrypoint"}, Args: []string{"--materialize-profile", "--store-name=" + spec.Store.Store, "--store-endpoint=" + spec.Store.Endpoint, "--store-bucket=" + spec.Store.Bucket, "--store-region=" + spec.Store.Region, "--profile-object-key=" + spec.Bundle.Key, "--profile-object-size=" + strconv.FormatInt(spec.Bundle.Size, 10), "--profile-output=/profile/bundle.json"}, Resources: initResources, Env: []core.EnvVar{secretEnvOperation("AWS_ACCESS_KEY_ID", spec.Store.CredentialsSecretName, component.EvidenceStoreAccessKeyKey), secretEnvOperation("AWS_SECRET_ACCESS_KEY", spec.Store.CredentialsSecretName, component.EvidenceStoreSecretKeyKey)}, VolumeMounts: []core.VolumeMount{{Name: "profile", MountPath: "/profile"}}},
	}, Containers: []core.Container{{Name: "firstfloor", Image: desiredImage, Command: []string{"/opt/chill/bin/serving-worker"}, Args: []string{"--config=/config/config.json", "--bind-address=:8080", "--node-name=" + node.Name}, Ports: []core.ContainerPort{{Name: "http", ContainerPort: 8080}}, Resources: core.ResourceRequirements{Requests: requests, Limits: limits}, VolumeMounts: []core.VolumeMount{{Name: "artifact", MountPath: "/model", ReadOnly: true}, {Name: "preprocessor", MountPath: "/preprocessor", ReadOnly: true}, {Name: "profile", MountPath: "/profile", ReadOnly: true}, {Name: "config", MountPath: "/config", ReadOnly: true}}, TerminationMessagePolicy: core.TerminationMessageFallbackToLogsOnError, ReadinessProbe: &core.Probe{ProbeHandler: core.ProbeHandler{HTTPGet: &core.HTTPGetAction{Path: "/v2/health/ready", Port: port8080()}}, PeriodSeconds: 1}}}, Volumes: []core.Volume{{Name: "artifact", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "preprocessor", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "profile", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "config", VolumeSource: core.VolumeSource{ConfigMap: &core.ConfigMapVolumeSource{LocalObjectReference: core.LocalObjectReference{Name: spec.Name}}}}}}}
	o.placeServingPod(pod, spec.Owner, spec.Instance)
	if spec.Instance.Allocation.GPUUUID != "" {
		pod.Spec.Containers[0].Env = []core.EnvVar{{Name: "CUDA_VISIBLE_DEVICES", Value: spec.Instance.Allocation.GPUUUID}}
	}
	if delivery != nil {
		runtime := &pod.Spec.Containers[0]
		runtime.VolumeMounts = append(runtime.VolumeMounts, host.VolumeMounts...)
		runtime.SecurityContext = &core.SecurityContext{Privileged: ptr.To(host.Privileged), AllowPrivilegeEscalation: ptr.To(host.Privileged)}
		pod.Spec.Volumes = append(pod.Spec.Volumes, host.Volumes...)
		pod.Spec.SecurityContext = &core.PodSecurityContext{SupplementalGroups: host.SupplementalGroups}
	} else if access := p.Condition.RuntimeAccess; access != nil && len(access.SupplementalGroups) != 0 {
		// Reproduce the measured device-group access (e.g. Jetson video group for CUDA).
		pod.Spec.SecurityContext = &core.PodSecurityContext{SupplementalGroups: slices.Clone(access.SupplementalGroups)}
	}
	// ponytail: admitted just before the Pod create; the remaining window closes with M3 single-writer fencing.
	if _, err = o.AdmittedNode(ctx, node); err != nil {
		return nil, err
	}
	if err = o.Store.Client().Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	created := err == nil
	if apierrors.IsAlreadyExists(err) {
		existing := &core.Pod{}
		if err = o.Store.Client().Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, existing); err != nil {
			return nil, err
		}
		if !SamePreparedPod(existing, pod) {
			return nil, errors.New("existing preparation Pod differs")
		}
		pod = existing
	}
	evidence = &servingstate.PreparationEvidence{PodUID: string(pod.UID), NodeUID: node.UID, DecisionID: decisionID, Created: created}
	err = o.Observer.PublishPreparedSource(pod, spec.Instance.Node)
	if err != nil {
		return evidence, err
	}
	if err = o.EnsureInstanceService(ctx, pod); err != nil {
		return evidence, err
	}
	if pod.Annotations[servingstate.ServingPolicyAnnotation] != decisionID {
		previous := pod.DeepCopy()
		pod.Annotations[servingstate.ServingPolicyAnnotation] = decisionID
		if err = o.Store.Client().Patch(ctx, pod, client.MergeFromWithOptions(previous, client.MergeFromWithOptimisticLock{})); err != nil {
			return evidence, err
		}
	}
	return evidence, o.Wait(ctx, func() (bool, error) { return o.PrepareObserved(ctx, pod) })
}

// PrepareObserved reports whether the runtime holds the prepared Pod's
// executor Ready; a failed Pod ends the preparation. It reuses an observer
// refresh from within the poll interval (the server's source refresh).
func (o *Operations) PrepareObserved(ctx context.Context, pod *core.Pod) (bool, error) {
	if err := o.Observer.RefreshWithin(ctx, o.Config.PollInterval); err != nil {
		return false, err
	}
	current, err := o.Kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
	if err != nil {
		return false, err
	}
	if current.UID != pod.UID {
		return false, errors.New("prepared Pod identity changed")
	}
	if current.Status.Phase == core.PodFailed {
		return false, fmt.Errorf("native preparation Pod failed%s", terminationMessages(current))
	}
	for _, i := range o.Runtime.Instances() {
		if strings.HasPrefix(i.ID, string(pod.UID)+"/") && i.Ready {
			return true, nil
		}
	}
	return false, nil
}

func (o *Operations) EnsurePreparationConfig(ctx context.Context, spec servingstate.PrepareSpec, delivery []byte) (string, error) {
	raw, err := json.Marshal(spec.Config)
	if err != nil {
		return "", err
	}
	if len(raw) > 64<<10 {
		return "", errors.New("native mutable config exceeds small policy bound")
	}
	cm := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: o.Config.Namespace, Name: spec.Name}, Data: map[string]string{"config.json": string(raw)}}
	if delivery != nil {
		cm.Data["delivery.json"] = string(delivery)
	}
	if err = o.Store.Client().Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", err
	}
	if apierrors.IsAlreadyExists(err) {
		existing := &core.ConfigMap{}
		if err = o.Store.Client().Get(ctx, types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, existing); err != nil {
			return "", err
		}
		var previous native.Config
		if err = json.Unmarshal([]byte(existing.Data["config.json"]), &previous); err != nil {
			return "", err
		}
		decisionID := previous.Policy.DecisionID
		desired := spec.Config
		previous.Policy = native.Policy{DeadlinePolicy: native.EffectiveDeadlinePolicy(previous.Policy.DeadlinePolicy), FixedBatchSize: previous.Policy.FixedBatchSize}
		desired.Policy = native.Policy{DeadlinePolicy: native.EffectiveDeadlinePolicy(desired.Policy.DeadlinePolicy), FixedBatchSize: desired.Policy.FixedBatchSize}
		if !reflect.DeepEqual(previous, desired) || existing.Data["delivery.json"] != cm.Data["delivery.json"] {
			return "", errors.New("existing immutable preparation execution config differs")
		}
		return decisionID, nil
	}
	return spec.Config.Policy.DecisionID, nil
}

// placeServingPod lets a measurement instance tolerate the measurement taint
// and records the Workload that owns the instance. A manually prepared Pod
// serves only its owner until a route records the applied distribution; a
// planner target (it names its Workload) waits for the planner's route.
func (o *Operations) placeServingPod(pod *core.Pod, owner *serving.WorkloadReference, instance serving.SchedulerInstance) {
	if o.Config.Measurement {
		pod.Spec.Tolerations = []core.Toleration{profilingrun.MeasurementToleration()}
	}
	routing := map[string]int32{}
	if owner != nil {
		pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: owner.Name, UID: types.UID(owner.UID)}}
		if instance.WorkloadUID == "" {
			routing[owner.UID] = instance.Weight
		}
	}
	raw, _ := json.Marshal(routing) // a string-to-int32 map always encodes
	pod.Annotations[servingstate.ServingRoutingAnnotation] = string(raw)
}
