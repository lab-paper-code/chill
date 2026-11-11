package main

import (
	"time"

	"github.com/lab-paper-code/chill/internal/serving/live"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func recordCluster(
	log *observation.ObservationLog, publisher *livePublisher, row observation.ClusterObservation,
) error {
	if err := log.ClusterRecord(row); err != nil {
		return err
	}
	if publisher != nil {
		publisher.cluster(row)
	}
	return nil
}

func serverStartCluster(
	podUID, namespace, name string, started time.Time, opts options,
) observation.ClusterObservation {
	return observation.ClusterObservation{
		Kind: "pod", At: started, Event: "server-start",
		SourceInterval: &opts.sourceInterval, ProfileInterval: &opts.profileInterval,
		Pod: &observation.ClusterPod{UID: podUID, Namespace: namespace, Name: name},
	}
}

func (p *livePublisher) cluster(row observation.ClusterObservation) {
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
