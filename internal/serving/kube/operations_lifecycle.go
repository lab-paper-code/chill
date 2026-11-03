package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"slices"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (o *Operations) RemoveInstance(ctx context.Context, node serving.NodeReference, id string) error {
	return o.removeInstance(ctx, node, "", id, nil)
}

func (o *Operations) StopInstance(ctx context.Context, id string) error {
	if err := o.wait(ctx, func() (bool, error) { return o.runtime.StopSchedulerInstance(id) }); err != nil {
		return err
	}
	o.cluster.MarkRemoving(id)
	return o.persistRouting(ctx, id, nil)
}

// detachInstance waits for the drain proof and records the withdrawn routes.
func (o *Operations) detachInstance(ctx context.Context, id string) error {
	if err := o.wait(ctx, func() (bool, error) { return o.runtime.DetachInstance(id) }); err != nil {
		return err
	}
	return o.persistRouting(ctx, id, nil)
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
	if err := o.validateOperationBinding(ctx, servingstate.Operation{Kind: servingstate.OperationRemove, Node: node, PodUID: uid, Instance: id}); err != nil {
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
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, meta.ListOptions{})
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
	status := firstfloorStatus(pod)
	if err := o.observer.restoreRemovalSource(pod, id, containerID); err != nil {
		return err
	}
	if id != "" && status.State.Terminated == nil {
		if err = o.wait(ctx, func() (bool, error) {
			endpoint, e := nativeInstanceEndpoint(ctx, o.kube, pod)
			if e != nil {
				return false, e
			}
			state, e := o.native.State(ctx, sdk.Spec{Endpoint: endpoint, ModelName: "chill_model"})
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
	if err = patchSourceFinalizer(ctx, o.store.client, pod, true); err != nil {
		return err
	}
	if pod.DeletionTimestamp == nil {
		u := pod.UID
		err = o.store.client.Delete(ctx, pod, &client.DeleteOptions{Preconditions: &meta.Preconditions{UID: &u}})
		if err != nil {
			return err
		}
	}
	return o.finishInstanceRemoval(ctx, pod)
}

func (o *Operations) finishInstanceRemoval(ctx context.Context, pod *core.Pod) error {
	return o.wait(ctx, func() (bool, error) {
		current := &core.Pod{}
		e := o.store.client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, current)
		if apierrors.IsNotFound(e) {
			return false, errors.New("physical source lost before termination proof")
		}
		if e != nil {
			return false, e
		}
		if current.UID != pod.UID {
			return false, errors.New("Pod identity changed")
		}
		stopped := o.observer.observeRemovalStopped(current)
		if !stopped || !podTerminated(current) {
			return false, nil
		}
		if e = deleteInstanceService(ctx, o.kube, current); e != nil {
			return false, e
		}
		if e = patchSourceFinalizer(ctx, o.store.client, current, false); e != nil {
			return false, e
		}
		if e = o.wait(ctx, func() (bool, error) {
			_, e := o.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
			return apierrors.IsNotFound(e), eIfNotFound(e)
		}); e != nil {
			return false, e
		}
		cm := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name}}
		if e = o.store.client.Delete(ctx, cm); e != nil && !apierrors.IsNotFound(e) {
			return false, e
		}
		if e = o.observer.retireRemovedSource(pod.UID, o.runtime); e != nil {
			return false, e
		}
		return true, nil
	})
}

// patchSourceFinalizer changes only our primitive-list member. The API server
// rejects a different immutable UID, but unrelated status/RV and other
// finalizer updates do not invalidate this metadata-only delta.
func patchSourceFinalizer(ctx context.Context, c client.Client, pod *core.Pod, present bool) error {
	if pod.UID == "" {
		return errors.New("physical Pod UID required for source finalizer")
	}
	metadata := map[string]any{"uid": pod.UID}
	if present {
		metadata["finalizers"] = []string{sourceFinalizer}
	} else {
		metadata["$deleteFromPrimitiveList/finalizers"] = []string{sourceFinalizer}
	}
	patch, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return err
	}
	return c.Patch(ctx, pod, client.RawPatch(types.StrategicMergePatchType, patch))
}

func eIfNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// ChangePower switches the drained Node to mode and confirms that the loaded
// state did not change. maxDuration is the mode's maxActuationDurationSeconds.
func (o *Operations) ChangePower(ctx context.Context, node serving.NodeReference, holder, mode string, maxDuration time.Duration) error {
	if mode == "" || maxDuration <= 0 {
		return errors.New("power mode and maximum actuation duration required")
	}
	_, err := o.admittedNode(ctx, node)
	if err != nil {
		return err
	}
	err = o.observer.beginNodeMove(ctx, node.UID)
	if err != nil {
		return err
	}
	// Every native instance on this Node is affected; never treat a mode as per UID.
	defer o.observer.clearNodeMove(node.UID)
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
	if err = o.requestPowerMode(ctx, node, holder, mode, maxDuration); err != nil {
		return err
	}
	if err := o.waitPowerState(ctx, node, before); err != nil {
		return err
	}
	o.observer.clearNodeMove(node.UID)
	return o.observer.Refresh(ctx)
}

// requestPowerMode writes the desired mode on the Node read it admits, so a
// grant that changed since the read fails the write with a conflict, and
// waits until the actuation agent settles mode within maxDuration of the write.
// A measurement operation writes only on the Node its holder was granted.
func (o *Operations) requestPowerMode(ctx context.Context, ref serving.NodeReference, holder, mode string, maxDuration time.Duration) error {
	node, err := o.admittedNode(ctx, ref)
	if err != nil {
		return err
	}
	if granted, _ := profilingrun.MeasurementHolder(node); o.config.Measurement && granted != holder {
		return fmt.Errorf("Node %s is granted to %q, not %q", node.Name, granted, holder)
	}
	if err := chillmeta.RequestPowerMode(ctx, o.store.client, node, mode, time.Now()); err != nil {
		return err
	}
	return o.wait(ctx, func() (bool, error) {
		if node, err = o.admittedNode(ctx, ref); err != nil {
			return false, err
		}
		return chillmeta.PowerModeOutcome(node, mode, maxDuration, time.Now())
	})
}

// admittedNode reads the exact Node and admits it for this operation.
func (o *Operations) admittedNode(ctx context.Context, ref serving.NodeReference) (*core.Node, error) {
	node := &core.Node{}
	if err := o.store.client.Get(ctx, client.ObjectKey{Name: ref.Name}, node); err != nil {
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
	if o.config.Measurement && !tainted {
		return errors.New("measurement operation requires the " + profilingrun.MeasurementTaintKey + " taint on Node " + node.Name)
	}
	if !o.config.Measurement && tainted {
		return ErrNodeInMeasurement
	}
	return nil
}

func (o *Operations) waitPowerState(ctx context.Context, node serving.NodeReference, before servingstate.OperationState) error {
	if err := o.wait(ctx, func() (bool, error) {
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

func nodeClassMatches(node *core.Node, class *edge.DeviceClass) bool {
	if len(class.Spec.NodeSelector) == 0 {
		return false
	}
	for key, value := range class.Spec.NodeSelector {
		if node.Labels[key] != value {
			return false
		}
	}
	return true
}
