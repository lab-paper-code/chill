package serving

import (
	"math"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// ponytail: fixed 1 s sampling period; make it a setting only if a heap ramp
// measured faster than this reaches the memory limit between samples.
const overloadInterval = time.Second

// sampleHeap marks the Runtime overloaded while the heap the last GC found
// live exceeds the configured fraction of the Go memory limit. Without a
// memory limit (GOMEMLIMIT unset) it never does.
func (r *Runtime) sampleHeap() {
	limit := debug.SetMemoryLimit(-1)
	if limit == math.MaxInt64 {
		r.overloaded.Store(false)
		return
	}
	live := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(live)
	r.overloaded.Store(float64(live[0].Value.Uint64()) > r.config.OverloadHeapFraction*float64(limit))
}

func (r *Runtime) watchHeap() {
	ticker := time.NewTicker(overloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.sampleHeap()
		}
	}
}
