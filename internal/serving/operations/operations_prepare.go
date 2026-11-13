package operations

import (
	context "context"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	reflect "reflect"
	slices "slices"
	strconv "strconv"
	strings "strings"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	component "github.com/lab-paper-code/chill/internal/component"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	serving "github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	ptr "k8s.io/utils/ptr"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

func (o *Operations) PrepareInstance(ctx context.Context, spec servingstate.PrepareSpec) (evidence *servingstate.PreparationEvidence, err error) {
	node := spec.Instance.Node
	pod, delivery, err := o.PreparationPod(spec)
	if err != nil {
		return nil, err
	}
	if err := o.checkRecovery(ctx, node, ""); err != nil {
		return nil, err
	}
	decisionID, err := o.EnsurePreparationConfig(ctx, spec, delivery)
	if err != nil {
		return nil, err
	}
	pod.Annotations[servingstate.ServingPolicyAnnotation] = decisionID
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
	evidence = &servingstate.PreparationEvidence{PodUID: string(pod.UID), NodeUID: node.UID, DecisionID: decisionID,
		Created: created, RuntimeImage: spec.Config.RuntimeImage, RunnerCommand: spec.Config.RunnerCommand}
	defer func() {
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		observed := pod
		var failed *servingstate.PreparationPodError
		if errors.As(err, &failed) {
			observed = failed.Pod
		}
		currentNode, _ := o.Kube.CoreV1().Nodes().Get(ctx, node.Name, meta.GetOptions{})
		events, _ := o.Kube.CoreV1().Events(pod.Namespace).List(ctx, meta.ListOptions{FieldSelector: "involvedObject.uid=" + string(pod.UID)})
		var related []core.Event
		if events != nil {
			related = events.Items
		}
		if failure, failed := servingstate.ClassifyPreparationFailure(observed, currentNode, related, err, true); failed {
			evidence.Failure = &failure
		}
	}()
	if created && pod.UID != "" {
		if err = o.Runtime.RecordCreatedPod(string(pod.UID), node); err != nil {
			return evidence, err
		}
	}
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
	observed := o.PrepareObserved
	if spec.Measurement() {
		observed = o.prepareNativeReady
	}
	return evidence, o.Wait(ctx, func() (bool, error) { return observed(ctx, pod) })
}

// PreparationPod builds the actual serving Pod from the frozen actor recipe.
// The returned delivery body is mounted with the actor config, not re-resolved.
// A measurement instance starts without a profile bundle.
func (o *Operations) PreparationPod(spec servingstate.PrepareSpec) (*core.Pod, []byte, error) {
	if err := ValidatePreparation(spec); err != nil {
		return nil, nil, err
	}
	node := spec.Instance.Node
	host, err := measuredRuntimeDelivery(spec.Delivery)
	if err != nil {
		return nil, nil, err
	}
	delivery := []byte(spec.Delivery.RKNNLite)
	runtime, err := native.RuntimeContainer(&spec.Config)
	if err != nil {
		return nil, nil, err
	}
	initResources := core.ResourceRequirements{
		Requests: core.ResourceList{core.ResourceMemory: runtime.Resources.Requests[core.ResourceMemory]},
		Limits:   core.ResourceList{core.ResourceMemory: runtime.Resources.Limits[core.ResourceMemory]},
	}
	runtime.Name = "firstfloor"
	runtime.Command = []string{"/opt/chill/bin/serving-worker"}
	runtime.Args = []string{"--config=/config/config.json", "--bind-address=:8080", "--node-name=" + node.Name}
	runtime.Ports = []core.ContainerPort{{Name: "http", ContainerPort: 8080}}
	runtime.VolumeMounts = append(runtime.VolumeMounts,
		core.VolumeMount{Name: "profile", MountPath: "/profile", ReadOnly: true},
		core.VolumeMount{Name: "config", MountPath: "/config", ReadOnly: true})
	runtime.TerminationMessagePolicy = core.TerminationMessageFallbackToLogsOnError
	runtime.ReadinessProbe = &core.Probe{ProbeHandler: core.ProbeHandler{HTTPGet: &core.HTTPGetAction{Path: "/v2/health/ready", Port: port8080()}}, PeriodSeconds: 1}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: o.Config.Namespace, Name: spec.Name, Labels: map[string]string{"app.kubernetes.io/component": servingstate.ServingInstanceComponent, profilingrun.MeasurementWorkloadLabelKey: profilingrun.ServingInstanceRole}, Annotations: map[string]string{servingstate.ServingProfileAnnotation: spec.Config.Policy.ProfileID, servingstate.ServingWeightAnnotation: strconv.Itoa(int(spec.Instance.Weight)), servingstate.ServingPolicyAnnotation: spec.Config.Policy.DecisionID, servingstate.ServingSlotAnnotation: spec.Instance.ID}, Finalizers: []string{servingstate.SourceFinalizer}}, Spec: core.PodSpec{
		NodeName: node.Name, RestartPolicy: core.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
		InitContainers: []core.Container{
			{Name: "artifact", Image: spec.Config.ArtifactImage, Command: []string{"sh", "-ec"}, Args: append([]string{`cp "$@" /chill-artifact/`, "--"}, spec.Config.ArtifactFiles...), Resources: initResources, VolumeMounts: []core.VolumeMount{{Name: "artifact", MountPath: "/chill-artifact"}}},
			{Name: "preprocessor", Image: spec.Config.PreprocessorImage, Command: []string{"sh", "-ec"}, Args: []string{`cp -R "$1"/. /chill-preprocessor/`, "--", spec.Config.PreprocessorPath}, Resources: initResources, VolumeMounts: []core.VolumeMount{{Name: "preprocessor", MountPath: "/chill-preprocessor"}}},
			{Name: "profile", Image: spec.Config.RuntimeImage, Command: []string{"/opt/chill/bin/serving-entrypoint"}, Args: []string{"--materialize-profile", "--store-name=" + spec.Store.Store, "--store-endpoint=" + spec.Store.Endpoint, "--store-bucket=" + spec.Store.Bucket, "--store-region=" + spec.Store.Region, "--profile-object-key=" + spec.Bundle.Key, "--profile-object-size=" + strconv.FormatInt(spec.Bundle.Size, 10), "--profile-output=/profile/bundle.json"}, Resources: initResources, Env: []core.EnvVar{secretEnvOperation("AWS_ACCESS_KEY_ID", spec.Store.CredentialsSecretName, component.EvidenceStoreAccessKeyKey), secretEnvOperation("AWS_SECRET_ACCESS_KEY", spec.Store.CredentialsSecretName, component.EvidenceStoreSecretKeyKey)}, VolumeMounts: []core.VolumeMount{{Name: "profile", MountPath: "/profile"}}},
		},
		Containers: []core.Container{runtime},
		Volumes:    []core.Volume{{Name: "artifact", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "preprocessor", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "profile", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}, {Name: "config", VolumeSource: core.VolumeSource{ConfigMap: &core.ConfigMapVolumeSource{LocalObjectReference: core.LocalObjectReference{Name: spec.Name}}}}},
	}}
	if spec.Measurement() {
		bundle := func(name string) bool { return name == "profile" }
		pod.Spec.InitContainers = slices.DeleteFunc(pod.Spec.InitContainers, func(c core.Container) bool { return bundle(c.Name) })
		pod.Spec.Volumes = slices.DeleteFunc(pod.Spec.Volumes, func(v core.Volume) bool { return bundle(v.Name) })
		pod.Spec.Containers[0].VolumeMounts = slices.DeleteFunc(pod.Spec.Containers[0].VolumeMounts, func(m core.VolumeMount) bool { return bundle(m.Name) })
	}
	o.placeServingPod(pod, spec.Owner, spec.Instance)
	if delivery != nil {
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, host.VolumeMounts...)
		pod.Spec.Containers[0].SecurityContext = &core.SecurityContext{Privileged: ptr.To(host.Privileged), AllowPrivilegeEscalation: ptr.To(host.Privileged)}
		pod.Spec.Volumes = append(pod.Spec.Volumes, host.Volumes...)
		pod.Spec.SecurityContext = &core.PodSecurityContext{SupplementalGroups: host.SupplementalGroups}
	} else if groups := spec.Delivery.SupplementalGroups; len(groups) != 0 {
		pod.Spec.SecurityContext = &core.PodSecurityContext{SupplementalGroups: slices.Clone(groups)}
	}
	return pod, delivery, nil
}

// PrepareObserved reports whether the runtime holds the prepared Pod's
// executor Ready; a failed Pod ends the preparation.
func (o *Operations) PrepareObserved(ctx context.Context, pod *core.Pod) (bool, error) {
	if _, err := o.preparedPod(ctx, pod); err != nil {
		return false, err
	}
	for _, i := range o.Runtime.Instances() {
		if strings.HasPrefix(i.ID, string(pod.UID)+"/") && i.Ready {
			return true, nil
		}
	}
	return false, nil
}

// prepareNativeReady reports whether a measurement instance's native runtime
// is Ready, read through its Pod-owned Service as Capture reads it. Without a
// catalog profile the runtime never holds the instance Ready. A runtime that
// does not answer yet is not ready.
func (o *Operations) prepareNativeReady(ctx context.Context, pod *core.Pod) (bool, error) {
	current, err := o.preparedPod(ctx, pod)
	if err != nil {
		return false, err
	}
	if status := servingstate.FirstfloorStatus(current); status == nil || status.State.Running == nil || status.ContainerID == "" {
		return false, nil
	}
	endpoint, err := servingstate.NativeInstanceEndpoint(ctx, o.Kube, current)
	if err != nil {
		return false, err
	}
	model, err := servingstate.NativeModel(ctx, o.Store.Client(), current)
	if err != nil {
		return false, err
	}
	state, err := o.Native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: model})
	return err == nil && state.Ready, nil
}

// preparedPod reads the prepared Pod after an observer refresh from within
// the poll interval (the server's source refresh); a failed Pod ends the
// preparation.
func (o *Operations) preparedPod(ctx context.Context, pod *core.Pod) (*core.Pod, error) {
	if err := o.Observer.RefreshWithin(ctx, o.Config.PollInterval); err != nil {
		return nil, err
	}
	current, err := o.Kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	if current.UID != pod.UID {
		return nil, errors.New("prepared Pod identity changed")
	}
	if failure, failed := servingstate.ClassifyPreparationFailure(current, nil, nil, nil, true); failed {
		return nil, &servingstate.PreparationPodError{Pod: current, Reason: fmt.Sprintf("native preparation failed: %s", failure.Reason)}
	}
	return current, nil
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
		previous.Policy = native.Policy{FixedBatchSize: previous.Policy.FixedBatchSize}
		desired.Policy = native.Policy{FixedBatchSize: desired.Policy.FixedBatchSize}
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
