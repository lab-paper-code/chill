package main

import (
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

func recordCluster(log *serving.ObservationLog, publisher *livePublisher, row serving.ClusterObservation) error {
	if err := log.ClusterRecord(row); err != nil {
		return err
	}
	if publisher != nil {
		publisher.cluster(row)
	}
	return nil
}

func serverStartCluster(podUID, namespace, name string) serving.ClusterObservation {
	return serving.ClusterObservation{
		Kind: "pod", At: time.Now().UTC(), Event: "server-start",
		Pod: &serving.ClusterPod{UID: podUID, Namespace: namespace, Name: name},
	}
}

func (p *livePublisher) cluster(row serving.ClusterObservation) {
	event := &live.ClusterEvent{
		Kind: row.Kind, At: liveAt(row.At), Event: row.Event, Source: row.Source,
		Reason: row.Reason, PreviousReason: row.PreviousReason,
	}
	if pod := row.Pod; pod != nil {
		event.Pod = &live.ClusterPod{
			Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID, Node: pod.Node, Phase: pod.Phase, Ready: pod.Ready,
		}
	}
	p.publish(row.At, live.Delta{}, &live.Event{Kind: "cluster", Cluster: event})
}
