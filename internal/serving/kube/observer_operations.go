package kube

import (
	"context"
	"errors"

	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (o *Observer) restoreRemovalSource(pod *core.Pod, id, containerID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	status := firstfloorStatus(pod)
	if id != "" && (status == nil || status.ContainerID != containerID) || id == "" && !podTerminated(pod) && !podAwaitingExecutor(pod) {
		return errors.New("physical source changed before removal")
	}
	if source := o.sources[string(pod.UID)]; id == "" && source != nil && source.containerID != "" && !source.stopped {
		return errors.New("retained physical executor requires exact removal identity")
	}
	o.restoreSources([]core.Pod{*pod})
	return nil
}

func (o *Observer) observeRemovalStopped(pod *core.Pod) bool {
	o.mu.Lock()
	o.observeTermination(pod)
	source := o.sources[string(pod.UID)]
	stopped := source != nil && source.stopped
	o.mu.Unlock()
	return stopped
}

func (o *Observer) retireRemovedSource(uid types.UID, runtime *serving.Runtime) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if source := o.sources[string(uid)]; source != nil {
		source.instance.State.Stopped = true
	}
	if err := o.publishLedger(nil); err != nil {
		return err
	}
	if err := runtime.RetirePod(string(uid)); err != nil {
		return err
	}
	delete(o.sources, string(uid))
	return nil
}

func (o *Observer) beginNodeMove(ctx context.Context, uid string) error {
	o.mu.Lock()
	o.movingNodes[uid] = true
	err := o.detachNodeLocked(ctx, uid)
	o.mu.Unlock()
	return err
}

func (o *Observer) clearNodeMove(uid string) {
	o.mu.Lock()
	delete(o.movingNodes, uid)
	o.mu.Unlock()
}

func (o *Observer) publishPreparedSource(pod *core.Pod, node serving.NodeReference) error {
	o.mu.Lock()
	o.restoreSources([]core.Pod{*pod})
	if source := o.sources[string(pod.UID)]; source != nil {
		source.instance.State.Node = node
	}
	err := o.publishLedger(nil)
	o.mu.Unlock()
	return err
}
