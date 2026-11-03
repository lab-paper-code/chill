package operations

import (
	context "context"
	json "encoding/json"
	fmt "fmt"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	serving "github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ValidateRoutingPolicies checks immutable native policy compatibility before a
// manual declaration or physical policy can be changed.
func (o *Operations) ValidateRoutingPolicies(ctx context.Context, target serving.SchedulerCandidate, workloadUID string) error {
	pods, err := o.Kube.CoreV1().Pods(o.Config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	for _, instance := range target.Instances {
		if instance.WorkloadUID != workloadUID {
			continue
		}
		found := false
		for _, pod := range pods.Items {
			status := servingstate.FirstfloorStatus(&pod)
			if status == nil || servingstate.PhysicalID(pod.UID, status.ContainerID) != instance.ID {
				continue
			}
			found = true
			cm, err := o.Kube.CoreV1().ConfigMaps(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
			if err != nil {
				return err
			}
			var config native.Config
			if err = json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
				return err
			}
			if instance.Profile == nil || instance.Profile.Condition.Firstfloor == nil || config.Policy.ProfileID != instance.Profile.ID || native.EffectiveDeadlinePolicy(config.Policy.DeadlinePolicy) != native.EffectiveDeadlinePolicy(instance.Profile.Condition.Firstfloor.DeadlinePolicy) || config.Policy.FixedBatchSize != instance.FixedBatchSize {
				return fmt.Errorf("instance %s has incompatible immutable native policy", instance.ID)
			}
		}
		if !found {
			return fmt.Errorf("actual policy target %s unavailable", instance.ID)
		}
	}
	return nil
}
