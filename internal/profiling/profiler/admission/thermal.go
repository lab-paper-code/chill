package admission

import (
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
)

// thermalState interprets retained typed thermal samples for scientific
// admission. The metric observer owns collection; this package owns the
// fail-closed admission meaning of missing, increasing, or reset counters.
func thermalState(samples []metricobserver.Sample, windowStart, windowEnd time.Time) (known, throttled bool) {
	inside := make([]metricobserver.Sample, 0, len(samples))
	for _, sample := range samples {
		if !sample.ObservedAt.Before(windowStart) && !sample.ObservedAt.After(windowEnd) {
			inside = append(inside, sample)
		}
	}
	if len(inside) == 0 {
		return false, false
	}

	available := true
	directComplete := true
	coreComplete := true
	packageComplete := true
	counterReset := false
	var previousCore, previousPackage *uint64
	for _, sample := range inside {
		thermal := sample.Thermal
		if thermal == nil || thermal.ObservationAvailable == nil || !*thermal.ObservationAvailable {
			available = false
		}
		if thermal == nil {
			directComplete, coreComplete, packageComplete = false, false, false
			continue
		}
		if thermal.Active == nil {
			directComplete = false
		} else if *thermal.Active {
			return true, true
		}
		if thermal.CPUCoreThrottleEvents == nil {
			coreComplete = false
		} else {
			if previousCore != nil {
				if *thermal.CPUCoreThrottleEvents > *previousCore {
					return true, true
				}
				if *thermal.CPUCoreThrottleEvents < *previousCore {
					counterReset = true
				}
			}
			value := *thermal.CPUCoreThrottleEvents
			previousCore = &value
		}
		if thermal.CPUPackageThrottleEvents == nil {
			packageComplete = false
		} else {
			if previousPackage != nil {
				if *thermal.CPUPackageThrottleEvents > *previousPackage {
					return true, true
				}
				if *thermal.CPUPackageThrottleEvents < *previousPackage {
					counterReset = true
				}
			}
			value := *thermal.CPUPackageThrottleEvents
			previousPackage = &value
		}
	}
	return available && !counterReset && (directComplete || coreComplete || packageComplete), false
}
