package operations

import (
	context "context"
	errors "errors"

	serving "github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClearNode removes every CHILL serving Pod on the Node: through the native
// removal when the runtime observes the instance, otherwise by deleting the Pod.
func (o *Operations) ClearNode(ctx context.Context, node serving.NodeReference) error {
	if err := o.checkRecovery(ctx, node, ""); err != nil {
		return err
	}
	pods, err := o.Kube.CoreV1().Pods(o.Config.Namespace).List(ctx, meta.ListOptions{
		LabelSelector: "app.kubernetes.io/component=" + servingstate.ServingInstanceComponent,
	})
	if err != nil {
		return err
	}
	observed := map[string]bool{}
	for _, i := range o.Runtime.Instances() {
		observed[i.ID] = true
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != node.Name {
			continue
		}
		if status := servingstate.FirstfloorStatus(pod); status != nil && observed[servingstate.PhysicalID(pod.UID, status.ContainerID)] {
			if err = o.RemoveInstance(ctx, node, servingstate.PhysicalID(pod.UID, status.ContainerID)); err != nil {
				return err
			}
			continue
		}
		// Unobserved executors still need the same retained termination proof.
		// A preparation without an executor may request DELETE, but still waits
		// for terminal Pod status before releasing its source finalizer.
		if err = o.removeInstance(ctx, node, string(pod.UID), "", nil); err != nil {
			return err
		}
	}
	return nil
}

// checkRecovery guards actual physical operations, including Pods discovered
// after the last observer refresh. Route-only work uses exact destination guards.
func (o *Operations) checkRecovery(ctx context.Context, node serving.NodeReference, onlyPodUID string) error {
	if !o.Runtime.Recovery().Unknown {
		return nil
	}
	for _, instance := range o.Runtime.Instances() {
		if instance.Node.UID != node.UID && instance.Node.Name != node.Name {
			continue
		}
		if onlyPodUID != "" && instance.PodUID != onlyPodUID {
			continue
		}
		if instance.RecoveryUnknown && !instance.Terminated {
			return errors.New("UnconfirmedInstance: executor recovery unknown")
		}
	}
	pods, err := o.Kube.CoreV1().Pods(o.Config.Namespace).List(ctx, meta.ListOptions{
		LabelSelector: "app.kubernetes.io/component=" + servingstate.ServingInstanceComponent,
	})
	if err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != node.Name || onlyPodUID != "" && string(pod.UID) != onlyPodUID || servingstate.PodTerminated(pod) {
			continue
		}
		key := string(pod.UID)
		if status := servingstate.FirstfloorStatus(pod); status != nil && status.ContainerID != "" {
			key = servingstate.PhysicalID(pod.UID, status.ContainerID)
		}
		if o.Runtime.InstanceRecoveryUnknown(key) {
			return errors.New("UnconfirmedInstance: executor recovery unknown")
		}
	}
	return nil
}
