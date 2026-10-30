package kube

import (
	"reflect"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	clusterEventChanged = "changed"
	clusterEventDeleted = "deleted"
)

func (o *Observer) clusterWatchNamespace() string {
	if o.config.OnCluster != nil {
		return metav1.NamespaceAll
	}
	return o.config.Namespace
}

func clusterTime(t metav1.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	at := t.Time.UTC()
	return &at
}

func clusterTermination(t *corev1.ContainerStateTerminated) *serving.ClusterTermination {
	if t == nil {
		return nil
	}
	return &serving.ClusterTermination{Reason: t.Reason, ExitCode: t.ExitCode, StartedAt: clusterTime(t.StartedAt), FinishedAt: clusterTime(t.FinishedAt)}
}

func clusterContainers(containers []corev1.Container, statuses []corev1.ContainerStatus) []serving.ClusterContainer {
	result := make([]serving.ClusterContainer, 0, len(containers))
	for _, container := range containers {
		row := serving.ClusterContainer{Name: container.Name}
		for _, s := range statuses {
			if s.Name != container.Name {
				continue
			}
			count := s.RestartCount
			row.ContainerID = s.ContainerID
			row.ImageID = s.ImageID
			row.RestartCount = &count
			row.Terminated = clusterTermination(s.State.Terminated)
			row.LastTerminated = clusterTermination(s.LastTerminationState.Terminated)
			break
		}
		result = append(result, row)
	}
	return result
}

func clusterPod(p *corev1.Pod) serving.ClusterPod {
	row := serving.ClusterPod{Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), Node: p.Spec.NodeName, CreatedAt: clusterTime(p.CreationTimestamp), Phase: string(p.Status.Phase), InitContainers: clusterContainers(p.Spec.InitContainers, p.Status.InitContainerStatuses), Containers: clusterContainers(p.Spec.Containers, p.Status.ContainerStatuses)}
	if p.DeletionTimestamp != nil {
		row.DeletedAt = clusterTime(*p.DeletionTimestamp)
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == corev1.PodReady {
			ready := string(condition.Status)
			row.Ready = &ready
			break
		}
	}
	return row
}

// observeClusterList is an observed baseline, never a synthetic creation or deletion.
// The observer source lock serializes this snapshot with watch notifications.
func (o *Observer) observeClusterList(pods []corev1.Pod) {
	for i := range pods {
		o.observeClusterPod(&pods[i], "")
	}
}

func (o *Observer) observeClusterPod(p *corev1.Pod, event string) {
	if o.config.OnCluster == nil || p.UID == "" {
		return
	}
	previous, seen := o.clusterPods[p.UID]
	if !seen && (o.config.SelectClusterPod == nil || !o.config.SelectClusterPod(p)) {
		return
	}
	row := clusterPod(p)
	kind := "observed"
	switch event {
	case string(watch.Deleted):
		kind = clusterEventDeleted
	case string(watch.Added):
		if !seen {
			kind = "created"
		} else {
			kind = clusterEventChanged
		}
	default:
		if seen {
			kind = clusterEventChanged
		}
	}
	if kind != clusterEventDeleted && seen && reflect.DeepEqual(previous, row) {
		return
	}
	observation := serving.ClusterObservation{Kind: "pod", At: time.Now().UTC(), Event: kind, Pod: &row}
	if err := o.config.OnCluster(observation); err != nil {
		ctrllog.Log.WithName("observer").Error(err, "record cluster Pod observation", "podUID", p.UID)
		return
	}
	if o.clusterPods == nil {
		o.clusterPods = map[types.UID]serving.ClusterPod{}
	}
	if kind == clusterEventDeleted {
		delete(o.clusterPods, p.UID)
	} else {
		o.clusterPods[p.UID] = row
	}
}

// recordIssueLocked compares actual state to the last successfully recorded state.
// A recording error is logged directly: feeding it back through issue would recurse.
func (o *Observer) recordIssueLocked(source string, at time.Time) {
	if o.config.OnCluster == nil {
		return
	}
	current, active := o.issues[source]
	previous, recorded := o.recordedIssues[source]
	if !active && !recorded || active && recorded && current.Reason == previous.Reason {
		return
	}
	row := serving.ClusterObservation{Kind: "issue", At: at, Source: source, Event: "raised", Reason: current.Reason}
	if recorded {
		row.PreviousReason = previous.Reason
		if active {
			row.Event = clusterEventChanged
		} else {
			row.Event = "resolved"
			row.Reason = previous.Reason
		}
	}
	if err := o.config.OnCluster(row); err != nil {
		ctrllog.Log.WithName("observer").Error(err, "record cluster issue observation", "source", source)
		return
	}
	if o.recordedIssues == nil {
		o.recordedIssues = map[string]ObserverIssue{}
	}
	if active {
		o.recordedIssues[source] = current
	} else {
		delete(o.recordedIssues, source)
	}
}
