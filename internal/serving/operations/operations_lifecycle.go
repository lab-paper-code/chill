package operations

import (
	context "context"
	errors "errors"
	fmt "fmt"
	slices "slices"
	strings "strings"
	time "time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	serving "github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	workloadobserver "github.com/lab-paper-code/chill/internal/workloadobserver"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

func (o *Operations) RemoveInstance(ctx context.Context, node serving.NodeReference, id string) error {
	return o.removeInstance(ctx, node, "", id, nil)
}

func (o *Operations) StopInstance(ctx context.Context, id string) error {
	if o.Runtime.InstanceRecoveryUnknown(id) {
		return errors.New("UnconfirmedInstance: executor recovery unknown")
	}
	if err := o.Wait(ctx, func() (bool, error) { return o.Runtime.StopSchedulerInstance(id) }); err != nil {
		return err
	}
	o.Cluster.MarkRemoving(id)
	return o.PersistRouting(ctx, id, nil)
}

// detachInstance waits for the drain proof and records the withdrawn routes.
func (o *Operations) detachInstance(ctx context.Context, id string) error {
	if o.Runtime.InstanceRecoveryUnknown(id) {
		return errors.New("UnconfirmedInstance: executor recovery unknown")
	}
	if err := o.Wait(ctx, func() (bool, error) { return o.Runtime.DetachInstance(id) }); err != nil {
		return err
	}
	return o.PersistRouting(ctx, id, nil)
}

func (o *Operations) removeInstance(ctx context.Context, node serving.NodeReference, podUID, id string, proof map[string]native.State) error {
	uid, containerID, _ := strings.Cut(id, "/")
	if podUID != "" {
		if uid != "" && uid != podUID {
			return errors.New("Pod UID differs from physical executor")
		}
		uid = podUID
	}
	if uid == "" || id != "" && containerID == "" {
		return errors.New("exact Pod UID and optional physical executor required")
	}
	if err := o.ValidateOperationBinding(ctx, servingstate.Operation{Kind: servingstate.OperationRemove, Node: node, PodUID: uid, Instance: id}); err != nil {
		return err
	}
	if id != "" {
		if err := o.StopInstance(ctx, id); err != nil {
			return err
		}
		if err := o.detachInstance(ctx, id); err != nil {
			return err
		}
	}
	pods, err := o.Kube.CoreV1().Pods(o.Config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	var pod *core.Pod
	for i := range pods.Items {
		if string(pods.Items[i].UID) == uid {
			pod = &pods.Items[i]
		}
	}
	if pod == nil {
		return errors.New("physical source disappeared without retained termination proof")
	}
	status := servingstate.FirstfloorStatus(pod)
	if err := o.Observer.RestoreRemovalSource(pod, id, containerID); err != nil {
		return err
	}
	if id != "" && status.State.Terminated == nil {
		model, err := o.nativeModel(ctx, pod)
		if err != nil {
			return err
		}
		if err = o.Wait(ctx, func() (bool, error) {
			endpoint, e := servingstate.NativeInstanceEndpoint(ctx, o.Kube, pod)
			if e != nil {
				return false, e
			}
			state, e := o.Native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: model})
			if e != nil {
				return false, e
			}
			if len(state.Requests) != 0 {
				return false, nil
			}
			if proof != nil {
				proof[id] = state
			}
			return true, nil
		}); err != nil {
			return err
		}
	}
	if err = servingstate.PatchSourceFinalizer(ctx, o.Store.Client(), pod, true); err != nil {
		return err
	}
	if pod.DeletionTimestamp == nil {
		u := pod.UID
		err = o.Store.Client().Delete(ctx, pod, &client.DeleteOptions{Preconditions: &meta.Preconditions{UID: &u}})
		if err != nil {
			return err
		}
	}
	return o.finishInstanceRemoval(ctx, pod)
}

func (o *Operations) finishInstanceRemoval(ctx context.Context, pod *core.Pod) error {
	return o.Wait(ctx, func() (bool, error) {
		current := &core.Pod{}
		e := o.Store.Client().Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, current)
		if apierrors.IsNotFound(e) {
			return false, errors.New("physical source lost before termination proof")
		}
		if e != nil {
			return false, e
		}
		if current.UID != pod.UID {
			return false, errors.New("Pod identity changed")
		}
		stopped := o.Observer.ObserveRemovalStopped(current)
		if !stopped || !servingstate.PodTerminated(current) {
			return false, nil
		}
		if e = servingstate.DeleteInstanceService(ctx, o.Kube, current); e != nil {
			return false, e
		}
		if e = servingstate.PatchSourceFinalizer(ctx, o.Store.Client(), current, false); e != nil {
			return false, e
		}
		if e = o.Wait(ctx, func() (bool, error) {
			_, e := o.Kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
			return apierrors.IsNotFound(e), servingstate.IgnoreNotFound(e)
		}); e != nil {
			return false, e
		}
		cm := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name}}
		if e = o.Store.Client().Delete(ctx, cm); e != nil && !apierrors.IsNotFound(e) {
			return false, e
		}
		if e = o.Observer.RetireRemovedSource(pod.UID, o.Runtime); e != nil {
			return false, e
		}
		return true, nil
	})
}

// ChangePower switches a drained Node. In-place changes retain their loaded
// witness; an observed reboot records the directed fact for future remove/rebuild plans.
func (o *Operations) ChangePower(ctx context.Context, node serving.NodeReference, holder, mode string, maxDuration time.Duration) error {
	if mode == "" || maxDuration <= 0 {
		return errors.New("power mode and maximum actuation duration required")
	}
	if err := o.checkRecovery(ctx, node, ""); err != nil {
		return err
	}
	startNode, err := o.AdmittedNode(ctx, node)
	if err != nil {
		return err
	}
	err = o.Observer.BeginNodeMove(ctx, node.UID)
	if err != nil {
		return err
	}
	// Every native instance on this Node is affected; never treat a mode as per UID.
	defer o.Observer.ClearNodeMove(node.UID)
	before, err := o.Capture(ctx, node)
	if err != nil {
		return err
	}
	if len(before.Reasons) > 0 {
		return errors.New("power transition lacks actual loaded source snapshot")
	}
	for _, state := range before.Native {
		if !state.Ready || len(state.Requests) != 0 {
			return errors.New("power transition native source not actually drained/ready")
		}
	}
	powerErr := o.RequestPowerMode(ctx, node, holder, mode, maxDuration)
	endNode, readErr := o.AdmittedNode(ctx, node)
	rebooted := readErr == nil && startNode.Status.NodeInfo.BootID != "" && endNode.Status.NodeInfo.BootID != "" &&
		startNode.Status.NodeInfo.BootID != endNode.Status.NodeInfo.BootID
	if rebooted && before.PowerMode != "" && before.PowerMode != mode {
		o.Observer.RecordPowerReboot(node, serving.PowerReboot{From: before.PowerMode, To: mode,
			BeforeBootID: startNode.Status.NodeInfo.BootID, AfterBootID: endNode.Status.NodeInfo.BootID})
	}
	if err := errors.Join(powerErr, readErr); err != nil {
		return err
	}
	// The mode succeeded, but a reboot cannot preserve old process identities.
	// The existing DAG still removes those sources and prepares target actors.
	if !rebooted {
		if err := o.waitPowerState(ctx, node, before); err != nil {
			return err
		}
	}
	o.Observer.ClearNodeMove(node.UID)
	return o.Observer.Refresh(ctx)
}

// RequestPowerMode writes the desired mode on the Node read it admits, so a
// grant that changed since the read fails the write with a conflict, and
// waits until the actuation agent settles mode within maxDuration of the write.
// A measurement operation writes only on the Node its holder was granted.
func (o *Operations) RequestPowerMode(ctx context.Context, ref serving.NodeReference, holder, mode string, maxDuration time.Duration) error {
	if err := o.checkRecovery(ctx, ref, ""); err != nil {
		return err
	}
	node, err := o.AdmittedNode(ctx, ref)
	if err != nil {
		return err
	}
	if granted, _ := profilingrun.MeasurementHolder(node); o.Config.Measurement && granted != holder {
		return fmt.Errorf("Node %s is granted to %q, not %q", node.Name, granted, holder)
	}
	if err := chillmeta.RequestPowerMode(ctx, o.Store.Client(), node, mode, time.Now()); err != nil {
		return err
	}
	return o.Wait(ctx, func() (bool, error) {
		if node, err = o.AdmittedNode(ctx, ref); err != nil {
			return false, err
		}
		return chillmeta.PowerModeOutcome(node, mode, maxDuration, time.Now())
	})
}

// AdmittedNode reads the exact Node and admits it for this operation.
func (o *Operations) AdmittedNode(ctx context.Context, ref serving.NodeReference) (*core.Node, error) {
	node := &core.Node{}
	if err := o.Store.Client().Get(ctx, client.ObjectKey{Name: ref.Name}, node); err != nil {
		return nil, err
	}
	if string(node.UID) != ref.UID {
		return nil, errors.New("physical Node identity changed")
	}
	return node, o.admitNode(node)
}

// admitNode lets measurement operations act only on a measurement POOL Node and
// every other operation only outside it.
func (o *Operations) admitNode(node *core.Node) error {
	tainted := profilingrun.InMeasurement(node)
	if o.Config.Measurement && !tainted {
		return errors.New("measurement operation requires the " + profilingrun.MeasurementTaintKey + " taint on Node " + node.Name)
	}
	if !o.Config.Measurement && tainted {
		return ErrNodeInMeasurement
	}
	return nil
}

func (o *Operations) waitPowerState(ctx context.Context, node serving.NodeReference, before servingstate.OperationState) error {
	if err := o.Wait(ctx, func() (bool, error) {
		after, e := o.Capture(ctx, node)
		if e != nil {
			return false, e
		}
		if len(after.Reasons) > 0 {
			return false, nil
		}
		if !slices.EqualFunc(before.Pods, after.Pods, workloadobserver.SamePod) || !slices.Equal(before.States, after.States) || len(before.Native) != len(after.Native) {
			return false, errors.New("power transition changed actual loaded state")
		}
		for id, a := range before.Native {
			b, ok := after.Native[id]
			if !ok {
				return false, errors.New("power transition lost physical native actor")
			}
			if !b.Ready {
				return false, nil
			}
			if len(b.Requests) != 0 || a.AcceptedRequests != b.AcceptedRequests || a.CompletedRequests != b.CompletedRequests || a.Executions != b.Executions || !sameLoadedMetadata(a.Metadata, b.Metadata) {
				return false, errors.New("power transition native condition changed or Serving inference occurred")
			}
		}
		return true, nil
	}); err != nil {
		return err
	}
	return nil
}
