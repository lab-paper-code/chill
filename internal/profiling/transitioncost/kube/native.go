package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	workloadkube "github.com/lab-paper-code/chill/internal/workloadobserver/kube"
	core "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (o *Observer) nativeNode(ctx context.Context) (*transitioncost.NativeNodeObservation, error) {
	node := &core.Node{}
	if err := o.reader.Get(ctx, client.ObjectKey{Name: o.request.TargetNode}, node); err != nil {
		return nil, fmt.Errorf("observe native physical Node: %w", err)
	}
	if string(node.UID) != o.request.TargetNodeUID || node.DeletionTimestamp != nil {
		return nil, errors.New("native physical Node identity changed")
	}
	pods := &core.PodList{}
	if err := o.reader.List(ctx, pods); err != nil {
		return nil, err
	}
	observed := &transitioncost.NativeNodeObservation{NodeName: node.Name, NodeUID: string(node.UID)}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node.Name || pod.Status.Phase == core.PodSucceeded || pod.Status.Phase == core.PodFailed {
			continue
		}
		projected, err := workloadkube.ProjectPod(pod, node.Name, []string{profilingrun.MeasurementWorkloadLabelKey})
		if err != nil {
			return nil, err
		}
		observed.Pods = append(observed.Pods, projected)
		var container *core.Container
		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == "firstfloor" {
				container = &pod.Spec.Containers[i]
				break
			}
		}
		if container == nil {
			continue
		}
		instance, err := o.nativeInstance(ctx, &pod, container)
		if err != nil {
			return nil, err
		}
		observed.Instances = append(observed.Instances, instance)
	}
	observed.ReceivedAt = o.now().UTC()
	slices.SortFunc(observed.Pods, func(a, b workloadobserver.Pod) int { return strings.Compare(a.UID, b.UID) })
	slices.SortFunc(observed.Instances, func(a, b transitioncost.NativeInstanceObservation) int {
		if a.PodUID < b.PodUID {
			return -1
		}
		if a.PodUID > b.PodUID {
			return 1
		}
		return 0
	})
	return observed, nil
}

func (o *Observer) nativeInstance(ctx context.Context, pod *core.Pod,
	container *core.Container) (transitioncost.NativeInstanceObservation, error) {
	var status *core.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == container.Name {
			status = &pod.Status.ContainerStatuses[i]
			break
		}
	}
	if pod.DeletionTimestamp != nil || !podReady(pod.Status.Conditions) || status == nil ||
		status.State.Running == nil || status.ContainerID == "" || !status.Ready {
		return transitioncost.NativeInstanceObservation{}, errors.New(
			"all coexistent native sources must have actual running/readiness identities")
	}
	port := int32(0)
	for _, p := range container.Ports {
		if p.Name == "http" && p.HostPort == 0 {
			port = p.ContainerPort
		}
	}
	if port < 1 || net.ParseIP(pod.Status.PodIP) == nil {
		return transitioncost.NativeInstanceObservation{}, errors.New("native Pod HTTP endpoint unavailable")
	}
	endpoint := "http://" + net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(port))) + "/v2/models/chill_model/state"
	state, err := o.readNativeState(ctx, endpoint)
	if err != nil {
		return transitioncost.NativeInstanceObservation{}, err
	}
	if err := o.verifyNativeIncarnation(ctx, pod, container.Name, status.ContainerID); err != nil {
		return transitioncost.NativeInstanceObservation{}, err
	}
	gpuRequest, gpuLimit := container.Resources.Requests["nvidia.com/gpu"], container.Resources.Limits["nvidia.com/gpu"]
	return transitioncost.NativeInstanceObservation{
		Namespace: pod.Namespace, PodName: pod.Name, PodUID: string(pod.UID),
		ContainerID: status.ContainerID, StartedAt: status.State.Running.StartedAt.Time, RestartCount: status.RestartCount,
		Image: container.Image, ImageID: status.ImageID,
		CPURequestMilli:    container.Resources.Requests.Cpu().MilliValue(),
		CPULimitMilli:      container.Resources.Limits.Cpu().MilliValue(),
		MemoryRequestBytes: container.Resources.Requests.Memory().Value(),
		MemoryLimitBytes:   container.Resources.Limits.Memory().Value(),
		GPURequest:         gpuRequest.Value(), GPULimit: gpuLimit.Value(), State: state,
	}, nil
}

func (o *Observer) readNativeState(ctx context.Context, endpoint string) (native.State, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return native.State{}, err
	}
	response, err := o.HTTPClient.Do(request)
	if err != nil {
		return native.State{}, fmt.Errorf("native state read: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	err = errors.Join(readErr, response.Body.Close())
	if err != nil {
		return native.State{}, err
	}
	if response.StatusCode != http.StatusOK || len(raw) > 4<<20 {
		return native.State{}, errors.New("native state returned failed/oversize response")
	}
	var state native.State
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&state); err != nil {
		return native.State{}, err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return native.State{}, errors.New("native state has trailing JSON")
	}
	return state, nil
}

func (o *Observer) verifyNativeIncarnation(ctx context.Context, pod *core.Pod, containerName, containerID string) error {
	// Re-read Pod incarnation after HTTP. A replacement cannot alias the actor
	// whose counters were just obtained.
	current := &core.Pod{}
	if err := o.reader.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
		return err
	}
	same := false
	for _, s := range current.Status.ContainerStatuses {
		if s.Name == containerName && s.ContainerID == containerID && s.State.Running != nil && s.Ready {
			same = true
		}
	}
	if current.UID != pod.UID || current.DeletionTimestamp != nil || !same ||
		!reflect.DeepEqual(current.Spec.Containers, pod.Spec.Containers) {
		return errors.New("native actor/allocation changed during HTTP observation")
	}
	return nil
}
