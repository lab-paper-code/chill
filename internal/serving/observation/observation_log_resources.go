package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// ResourceTarget is the exact runtime identity issued to a node agent.
type ResourceTarget struct {
	PodUID      string `json:"podUID"`
	ContainerID string `json:"containerID"`
}
type ResourceError struct {
	Target string `json:"target"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
type ContainerResources struct {
	ResourceTarget
	CgroupVersion      int             `json:"cgroupVersion"`
	CPUUsageNanos      *uint64         `json:"cpuUsageNanos,omitempty"`
	ThrottledNanos     *uint64         `json:"throttledNanos,omitempty"`
	Periods            *uint64         `json:"periods,omitempty"`
	ThrottledPeriods   *uint64         `json:"throttledPeriods,omitempty"`
	MemoryCurrentBytes *uint64         `json:"memoryCurrentBytes,omitempty"`
	MemoryPeakBytes    *uint64         `json:"memoryPeakBytes,omitempty"`
	Errors             []ResourceError `json:"errors,omitempty"`
}
type ThermalResource struct {
	Sensor       string `json:"sensor"`
	MilliCelsius int64  `json:"milliCelsius"`
}
type ResourceSample struct {
	SampledAt  time.Time            `json:"sampledAt"`
	Containers []ContainerResources `json:"containers"`
	Thermal    []ThermalResource    `json:"thermal"`
	Errors     []ResourceError      `json:"errors,omitempty"`
}
type ResourceBatch struct {
	Node    serving.NodeReference `json:"node"`
	Samples []ResourceSample      `json:"samples"`
}
type ResourceObservation struct {
	Kind                      string                 `json:"kind"`
	Node                      *serving.NodeReference `json:"node,omitempty"`
	ReceivedAt                *time.Time             `json:"receivedAt,omitempty"`
	Samples                   []ResourceSample       `json:"samples,omitempty"`
	At                        *time.Time             `json:"at,omitempty"`
	PodUID                    string                 `json:"podUID,omitempty"`
	ProcessStartedAt          *time.Time             `json:"processStartedAt,omitempty"`
	GCCPUSeconds              *float64               `json:"gcCPUSeconds,omitempty"`
	GCCycles                  *uint64                `json:"gcCycles,omitempty"`
	ObservationDirectoryBytes *int64                 `json:"observationDirectoryBytes,omitempty"`
	DirectoryObservedAt       *time.Time             `json:"directoryObservedAt,omitempty"`
	Errors                    []ResourceError        `json:"errors,omitempty"`
}

// ResourcesRecord uses receipt/write time, never a remote sample's clock.
func (l *ObservationLog) ResourcesRecord(row ResourceObservation) error {
	return l.submitRecord(observationResourcesStream, row, nil, time.Time{})
}

// PodMemoryPeak is the largest memory a Pod's containers reached. Lifetime
// reports that a sample carried the kernel's lifetime peak counter (cgroup v2
// memory.peak, v1 memory.max_usage_in_bytes); without it, on kernels that lack
// the counter, Bytes is the largest current-usage sample.
type PodMemoryPeak struct {
	Bytes    uint64 `json:"bytes"`
	Lifetime bool   `json:"lifetime"`
}

// PodMemoryPeak reads the node agent samples received in [from, to) for the
// Pod's containers. A Pod without a memory sample there has no peak.
func (l *ObservationLog) PodMemoryPeak(ctx context.Context, podUID string, from, to time.Time) (PodMemoryPeak, error) {
	var peak PodMemoryPeak
	if podUID == "" || !from.Before(to) {
		return peak, &serving.Error{Code: serving.CodeInvalidQuery, Message: "Pod UID and a positive window are required"}
	}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationResourcesStream: {from, to}})
	if err != nil {
		return peak, err
	}
	defer read.close()
	sampled := false
	err = l.scanStream(read, observationResourcesStream, func(raw []byte) error {
		var row ResourceObservation
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		if row.Kind != "node" || row.ReceivedAt == nil || !serving.ObservationInInterval(*row.ReceivedAt, from, to) {
			return nil
		}
		for _, sample := range row.Samples {
			for _, container := range sample.Containers {
				if container.PodUID != podUID {
					continue
				}
				if container.MemoryPeakBytes != nil {
					peak.Bytes, peak.Lifetime, sampled = max(peak.Bytes, *container.MemoryPeakBytes), true, true
				}
				if container.MemoryCurrentBytes != nil {
					peak.Bytes, sampled = max(peak.Bytes, *container.MemoryCurrentBytes), true
				}
			}
		}
		return nil
	})
	if err == nil && !sampled {
		err = fmt.Errorf("no memory sample of Pod %s received from %s to %s", podUID, from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	}
	return peak, err
}
