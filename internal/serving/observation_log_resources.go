package serving

import "time"

// ResourceTarget is the exact runtime identity issued to a node agent.
type ResourceTarget struct {
 PodUID string `json:"podUID"`
 ContainerID string `json:"containerID"`
}
type ResourceError struct {
 Target string `json:"target"`
 Field string `json:"field"`
 Reason string `json:"reason"`
}
type ContainerResources struct {
 ResourceTarget
 CgroupVersion int `json:"cgroupVersion"`
 CPUUsageNanos *uint64 `json:"cpuUsageNanos,omitempty"`
 ThrottledNanos *uint64 `json:"throttledNanos,omitempty"`
 Periods *uint64 `json:"periods,omitempty"`
 ThrottledPeriods *uint64 `json:"throttledPeriods,omitempty"`
 MemoryCurrentBytes *uint64 `json:"memoryCurrentBytes,omitempty"`
 MemoryPeakBytes *uint64 `json:"memoryPeakBytes,omitempty"`
 Errors []ResourceError `json:"errors,omitempty"`
}
type ThermalResource struct {
 Sensor string `json:"sensor"`
 MilliCelsius int64 `json:"milliCelsius"`
}
type ResourceSample struct {
 SampledAt time.Time `json:"sampledAt"`
 Containers []ContainerResources `json:"containers"`
 Thermal []ThermalResource `json:"thermal"`
 Errors []ResourceError `json:"errors,omitempty"`
}
type ResourceBatch struct {
 Node NodeReference `json:"node"`
 Samples []ResourceSample `json:"samples"`
}
type ResourceObservation struct {
 Kind string `json:"kind"`
 Node *NodeReference `json:"node,omitempty"`
 ReceivedAt *time.Time `json:"receivedAt,omitempty"`
 Samples []ResourceSample `json:"samples,omitempty"`
 At *time.Time `json:"at,omitempty"`
 PodUID string `json:"podUID,omitempty"`
 ProcessStartedAt *time.Time `json:"processStartedAt,omitempty"`
 GCCPUSeconds *float64 `json:"gcCPUSeconds,omitempty"`
 GCCycles *uint64 `json:"gcCycles,omitempty"`
 ObservationDirectoryBytes *int64 `json:"observationDirectoryBytes,omitempty"`
 DirectoryObservedAt *time.Time `json:"directoryObservedAt,omitempty"`
 Errors []ResourceError `json:"errors,omitempty"`
}

// ResourcesRecord uses receipt/write time, never a remote sample's clock.
func (l *ObservationLog) ResourcesRecord(row ResourceObservation) error {
 l.mu.Lock()
 defer l.mu.Unlock()
 return l.appendLocked(observationResourcesStream, l.now(), row)
}
