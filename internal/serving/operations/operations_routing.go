package operations

import (
	context "context"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	serving "github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	reflect "reflect"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	time "time"
)

func (o *Operations) ApplyTarget(ctx context.Context, target serving.SchedulerCandidate, result *servingstate.OperationResult) error {
	uids := result.Operation.WorkloadUIDs
	if result.Operation.WorkloadUID != "" {
		uids = []string{result.Operation.WorkloadUID}
	}
	if uids != nil && len(uids) == 0 {
		return nil
	}
	validate := o.Runtime.ValidateSchedulerWorkloadsTarget
	if result.Operation.Kind == servingstate.OperationUnroute {
		validate = o.Runtime.ValidateSchedulerUnrouteTarget
	}
	if err := validate(target, uids); err != nil {
		return err
	}
	if result.Operation.Kind != servingstate.OperationUnroute {
		uids = servingstate.PositiveWorkloadScope(target, uids)
		if len(uids) == 0 {
			return nil
		}
		if result.Operation.WorkloadUID == "" {
			result.Operation.WorkloadUIDs = uids
		}
	}
	result.Before.Routing, _ = o.Runtime.SchedulerRouting()
	if result.Before.Native == nil {
		result.Before.Native = map[string]native.State{}
	}
	for _, instance := range o.Runtime.Instances() {
		if instance.Native != nil {
			if _, observed := result.Before.Native[instance.ID]; !observed {
				result.Before.Native[instance.ID] = *instance.Native
			}
		}
	}
	defer func() {
		result.Actual.At = time.Now().UTC()
		result.Actual.Routing, _ = o.Runtime.SchedulerRouting()
	}()
	for _, instance := range target.Instances {
		if !result.Operation.ControlsWorkload(instance.WorkloadUID) || target.Routing[instance.WorkloadUID][instance.ID] <= 0 {
			continue
		}
		if instance.Profile == nil || instance.Profile.Condition.Firstfloor == nil {
			return errors.New("route target missing measured profile")
		}
		policy := native.Policy{DecisionID: target.ID, ProfileID: instance.Profile.ID,
			DeadlinePolicy: native.EffectiveDeadlinePolicy(instance.Profile.Condition.Firstfloor.DeadlinePolicy),
			MaxBatchSize:   instance.MaxBatch, MaxBatchWaitMS: float64(instance.MaxWait) / float64(time.Millisecond), FixedBatchSize: instance.FixedBatchSize}
		if err := o.ApplyPolicy(ctx, instance.ID, policy, result); err != nil {
			return fmt.Errorf("apply policy to %s: %w", instance.ID, err)
		}
	}
	if err := o.Observer.Refresh(ctx); err != nil {
		return err
	}
	var applyErr error
	if result.Operation.Kind == servingstate.OperationUnroute {
		applyErr = o.Runtime.ApplySchedulerUnrouteTarget(target, uids)
	} else {
		applyErr = o.Runtime.ApplySchedulerWorkloadsTarget(target, uids)
	}
	if applyErr != nil {
		return applyErr
	}
	weights, _ := o.Runtime.SchedulerRouting()
	o.Cluster.ClearRoutedRemovals(result.Operation, weights)
	// Explicit manual declarations must not overwrite the Pod defaults release restores.
	if result.Operation.WorkloadUID == "" {
		if err := o.PersistRouting(ctx, "", result.Operation.WorkloadUIDs); err != nil {
			return err
		}
	}
	result.Progress = append(result.Progress, "routing applied and read back")
	return nil
}

// PersistRouting records declared routes on the selected instance, or all known
// instances when only is empty. A non-nil UID scope preserves every other Pod seed.
func (o *Operations) PersistRouting(ctx context.Context, only string, uids []string) error {
	if uids != nil && len(uids) == 0 {
		return nil
	}
	pods, err := o.Kube.CoreV1().Pods(o.Config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	declared := o.Runtime.InstanceRouting()
	for i := range pods.Items {
		pod := &pods.Items[i]
		status := servingstate.FirstfloorStatus(pod)
		if status == nil {
			continue
		}
		id := servingstate.PhysicalID(pod.UID, status.ContainerID)
		routing, known := declared[id]
		if (only != "" && id != only) || (only == "" && !known) {
			continue
		}
		if uids != nil {
			var seeds map[string]int32
			if raw := pod.Annotations[servingstate.ServingRoutingAnnotation]; raw != "" {
				if err := json.Unmarshal([]byte(raw), &seeds); err != nil {
					return err
				}
			}
			changed := false
			for _, uid := range uids {
				weight, present := routing[uid]
				before, existed := seeds[uid]
				if present == existed && (!present || weight == before) {
					continue
				}
				changed = true
				if present {
					if seeds == nil {
						seeds = map[string]int32{}
					}
					seeds[uid] = weight
				} else {
					delete(seeds, uid)
				}
			}
			if !changed {
				continue
			}
			routing = seeds
		}
		if routing == nil {
			routing = map[string]int32{}
		}
		raw, err := json.Marshal(routing)
		if err != nil {
			return err
		}
		if pod.Annotations[servingstate.ServingRoutingAnnotation] == string(raw) {
			continue
		}
		previous := pod.DeepCopy()
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[servingstate.ServingRoutingAnnotation] = string(raw)
		if err = o.Store.Client().Patch(ctx, pod, client.MergeFromWithOptions(previous, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
	}
	return nil
}

func (o *Operations) ApplyPolicy(ctx context.Context, instanceID string, policy native.Policy, result *servingstate.OperationResult) error {
	pods, err := o.Kube.CoreV1().Pods(o.Config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	var selected *core.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		status := servingstate.FirstfloorStatus(p)
		if status != nil && servingstate.PhysicalID(p.UID, status.ContainerID) == instanceID {
			selected = p
			break
		}
	}
	if selected == nil {
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Annotations[servingstate.ServingSlotAnnotation] != instanceID {
				continue
			}
			if selected != nil {
				return errors.New("policy target slot has multiple physical instances")
			}
			selected = p
		}
	}
	if selected == nil {
		return errors.New("actual policy target unavailable")
	}
	cm := &core.ConfigMap{}
	if err = o.Store.Client().Get(ctx, types.NamespacedName{Namespace: selected.Namespace, Name: selected.Name}, cm); err != nil {
		return err
	}
	var config native.Config
	if err = json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
		return err
	}
	if config.Policy.ProfileID != policy.ProfileID {
		return errors.New("policy cannot change immutable profile")
	}
	if native.EffectiveDeadlinePolicy(config.Policy.DeadlinePolicy) != native.EffectiveDeadlinePolicy(policy.DeadlinePolicy) || config.Policy.FixedBatchSize != policy.FixedBatchSize {
		return errors.New("policy cannot change immutable deadline policy or fixed batch size")
	}
	policyChanged := !reflect.DeepEqual(config.Policy, policy)
	config.Policy = policy
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	before := cm.DeepCopy()
	cm.Data["config.json"] = string(raw)
	if err = o.Store.Client().Patch(ctx, cm, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	if policyChanged {
		result.Progress = append(result.Progress, instanceID+": desired policy configuration persisted")
	}
	previous := selected.DeepCopy()
	selected.Annotations[servingstate.ServingPolicyAnnotation] = policy.DecisionID
	if err = o.Store.Client().Patch(ctx, selected, client.MergeFromWithOptions(previous, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	if previous.Annotations[servingstate.ServingPolicyAnnotation] != policy.DecisionID {
		result.Progress = append(result.Progress, instanceID+": desired policy annotation persisted")
	}
	return o.Wait(ctx, func() (bool, error) {
		current, e := o.Kube.CoreV1().Pods(selected.Namespace).Get(ctx, selected.Name, meta.GetOptions{})
		if e != nil {
			return false, e
		}
		if !servingstate.SameRunningContainer(selected, current) {
			return false, errors.New("policy target physical executor changed")
		}
		endpoint, e := servingstate.NativeInstanceEndpoint(ctx, o.Kube, current)
		if e != nil {
			return false, e
		}
		state, e := o.Native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: config.Model})
		if e != nil {
			return false, e
		}
		actual := state.Metadata.Policy
		actual.Batches = nil
		expected := policy
		expected.Batches = nil
		if !state.Ready || state.Metadata.ProfileID != policy.ProfileID || state.Metadata.DecisionID != policy.DecisionID || !reflect.DeepEqual(actual, expected) {
			return false, nil
		}
		if result.Actual.Native == nil {
			result.Actual.Native = map[string]native.State{}
		}
		result.Actual.Native[servingstate.PhysicalID(current.UID, servingstate.FirstfloorStatus(current).ContainerID)] = state
		result.Progress = append(result.Progress, instanceID+": native policy acknowledged")
		return true, nil
	})
}
