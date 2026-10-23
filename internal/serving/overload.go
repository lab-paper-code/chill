package serving

import (
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// ponytail: fixed 1 s sampling period and 0.9 fraction; make them settings
// only if a measured heap ramp reaches the memory limit between samples.
const (
	overloadInterval     = time.Second
	overloadHeapFraction = 0.9
)

// sampleHeap marks the Runtime overloaded while the heap the last GC found
// live exceeds overloadHeapFraction of the Go memory limit. Without
// GOMEMLIMIT the limit is math.MaxInt64 and the guard never trips.
func (r *Runtime) sampleHeap() {
	live := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(live)
	r.overloaded.Store(float64(live[0].Value.Uint64()) > overloadHeapFraction*float64(debug.SetMemoryLimit(-1)))
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
