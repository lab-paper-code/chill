package serving

import (
 "errors"
 "time"
)

// ClusterObservation preserves observed Pod identity and observer issue transitions.
// At is the server observation time, not a reconstructed Kubernetes event time.
type ClusterObservation struct {
 Kind string `json:"kind"`
 At time.Time `json:"at"`
 Event string `json:"event"`
 Pod *ClusterPod `json:"pod,omitempty"`
 Source string `json:"source,omitempty"`
 Reason string `json:"reason,omitempty"`
 PreviousReason string `json:"previousReason,omitempty"`
}

type ClusterPod struct {
 Namespace string `json:"namespace,omitempty"`
 Name string `json:"name,omitempty"`
 UID string `json:"uid"`
 Node string `json:"node,omitempty"`
 CreatedAt *time.Time `json:"creationTimestamp,omitempty"`
 DeletedAt *time.Time `json:"deletionTimestamp,omitempty"`
 Phase string `json:"phase,omitempty"`
 Ready *string `json:"ready,omitempty"`
 InitContainers []ClusterContainer `json:"initContainers,omitempty"`
 Containers []ClusterContainer `json:"containers,omitempty"`
}

type ClusterContainer struct {
 Name string `json:"name"`
 ContainerID string `json:"containerID,omitempty"`
 ImageID string `json:"imageID,omitempty"`
 RestartCount *int32 `json:"restartCount,omitempty"`
 Terminated *ClusterTermination `json:"terminated,omitempty"`
 LastTerminated *ClusterTermination `json:"lastTerminated,omitempty"`
}

type ClusterTermination struct {
 Reason string `json:"reason,omitempty"`
 ExitCode int32 `json:"exitCode"`
 StartedAt *time.Time `json:"startedAt,omitempty"`
 FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func (l *ObservationLog) ClusterRecord(row ClusterObservation) error {
 if row.At.IsZero() { return errors.New("cluster observation time is required") }
 switch row.Kind {
 case "pod":
  if row.Pod == nil || row.Pod.UID == "" { return errors.New("cluster Pod UID is required") }
  switch row.Event { case "observed", "created", "changed", "deleted", "server-start": default: return errors.New("invalid cluster Pod event") }
 case "issue":
  if row.Source == "" || row.Reason == "" { return errors.New("cluster issue source and reason are required") }
  switch row.Event { case "raised", "changed", "resolved": default: return errors.New("invalid cluster issue event") }
 default: return errors.New("invalid cluster observation kind")
 }
 l.mu.Lock()
 defer l.mu.Unlock()
 return l.appendLocked(observationClusterStream, l.now(), row)
}
