package operations

import (
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	maps "maps"
	reflect "reflect"
	slices "slices"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	ptr "k8s.io/utils/ptr"
)

func SamePreparedPod(actual, desired *core.Pod) bool {
	if actual.Spec.RestartPolicy != desired.Spec.RestartPolicy || servingstate.WorkloadOwner(actual.OwnerReferences) != servingstate.WorkloadOwner(desired.OwnerReferences) {
		return false
	}
	if actual.Annotations[servingstate.ServingProfileAnnotation] != desired.Annotations[servingstate.ServingProfileAnnotation] || actual.Annotations[servingstate.ServingSlotAnnotation] != desired.Annotations[servingstate.ServingSlotAnnotation] || actual.Spec.NodeName != desired.Spec.NodeName || len(actual.Spec.Containers) != 1 || !samePreparedContainer(actual.Spec.Containers[0], desired.Spec.Containers[0]) || actual.Spec.Containers[0].TerminationMessagePolicy != desired.Spec.Containers[0].TerminationMessagePolicy || len(actual.Spec.InitContainers) != len(desired.Spec.InitContainers) ||
		!slices.EqualFunc(actual.Spec.Volumes, desired.Spec.Volumes, samePreparedVolume) || !slices.Equal(supplementalGroups(actual), supplementalGroups(desired)) {
		return false
	}
	for i := range desired.Spec.InitContainers {
		if !samePreparedContainer(actual.Spec.InitContainers[i], desired.Spec.InitContainers[i]) {
			return false
		}
	}
	return true
}

// samePreparedVolume compares the measured host source and type; the API
// server defaults an omitted HostPath type to the empty type.
func samePreparedVolume(a, b core.Volume) bool {
	if a.Name != b.Name || (a.EmptyDir != nil) != (b.EmptyDir != nil) || (a.HostPath != nil) != (b.HostPath != nil) {
		return false
	}
	return a.HostPath == nil || a.HostPath.Path == b.HostPath.Path && ptr.Deref(a.HostPath.Type, "") == ptr.Deref(b.HostPath.Type, "")
}

func supplementalGroups(pod *core.Pod) []int64 {
	if pod.Spec.SecurityContext == nil {
		return nil
	}
	return pod.Spec.SecurityContext.SupplementalGroups
}

func privilegedContainer(container core.Container) bool {
	return container.SecurityContext != nil && ptr.Deref(container.SecurityContext.Privileged, false)
}

// measuredRuntimeDelivery returns the measured RKNNLite delivery's host
// projection. The runner reads the delivery file; the Pod gets the same host
// access the measured profiling Job projected from it.
func measuredRuntimeDelivery(delivery servingstate.PrepareDelivery) (profilingrun.RuntimeDeliveryProjection, error) {
	if delivery.RKNNLite == nil {
		return profilingrun.RuntimeDeliveryProjection{}, nil
	}
	var frozen profilingrun.CandidateRuntimeDelivery
	if err := json.Unmarshal(delivery.RKNNLite, &frozen); err != nil {
		return profilingrun.RuntimeDeliveryProjection{}, fmt.Errorf("decode measured RKNNLite delivery: %w", err)
	}
	return profilingrun.ProjectRuntimeDelivery(profilingrun.FirstfloorRKNNLiteBackend, frozen)
}

// ValidatePreparation checks a preparation against its measured profile. A
// measurement instance has no profile: it needs its identity, Node, the profile
// it publishes and its memory, and initializes (so warms) only its fixed
// batch; profiling mode needs no batch 1.
func ValidatePreparation(spec servingstate.PrepareSpec) error {
	p := spec.Instance.Profile
	if spec.Measurement() {
		if spec.Name == "" || spec.Instance.Node.Name == "" || spec.Instance.Node.UID == "" || spec.Config.Policy.ProfileID == "" {
			return errors.New("measurement preparation requires a name, a Node and the profile it publishes")
		}
		if !slices.Equal(spec.Config.BatchSizes, []int{spec.Config.Policy.FixedBatchSize}) {
			return errors.New("measurement preparation initializes exactly its fixed batch size")
		}
	} else {
		if p == nil || p.Condition.Firstfloor == nil || p.RuntimeMetadata == nil || spec.Name == "" || spec.Instance.Node.UID != p.Condition.NodeUID {
			return errors.New("measured firstfloor preparation contract required")
		}
		if err := spec.Store.Validate(); err != nil {
			return err
		}
		if spec.Bundle.Store != spec.Store.Store || spec.Bundle.Size <= 0 {
			return errors.New("immutable profile delivery identity required")
		}
		if spec.Config.Policy.ProfileID != p.ID || spec.Config.Mode != servingstate.NativeServingMode || len(spec.Config.Policy.Batches) != 0 || spec.Config.ProfilePath != "/profile/bundle.json" {
			return errors.New("small policy must reference immutable startup profile")
		}
	}
	if spec.MemoryBytes != spec.Instance.Allocation.MemoryBytes || spec.MemoryBytes > uint64(1<<63-1) {
		return errors.New("preparation memory must exactly equal selected bounded allocation")
	}
	if p != nil && spec.Instance.Allocation.GPUUUID != p.RuntimeMetadata.GPUUUID {
		return errors.New("preparation GPU selection differs from measured physical conditions")
	}
	return nil
}

func secretEnvOperation(name, secret, key string) core.EnvVar {
	return core.EnvVar{Name: name, ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: secret}, Key: key}}}
}

func port8080() intstr.IntOrString { return intstr.FromInt32(8080) }

func samePreparedContainer(actual, desired core.Container) bool {
	return actual.Name == desired.Name && actual.Image == desired.Image &&
		slices.Equal(actual.Command, desired.Command) && slices.Equal(actual.Args, desired.Args) &&
		reflect.DeepEqual(actual.Env, desired.Env) &&
		maps.EqualFunc(actual.Resources.Requests, desired.Resources.Requests, resource.Quantity.Equal) &&
		maps.EqualFunc(actual.Resources.Limits, desired.Resources.Limits, resource.Quantity.Equal) &&
		reflect.DeepEqual(actual.VolumeMounts, desired.VolumeMounts) && privilegedContainer(actual) == privilegedContainer(desired)
}
