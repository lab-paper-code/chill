package serving

import (
	"runtime"
	"testing"
	"time"
)

// A held CapacityRow must not keep its replay's gaps cache alive: the cache
// holds one []float64 per distinct (seed, window population) and the table
// holds every row for the process lifetime. Measured on the live server as
// 1.3 GB of 2.6 GB in use (control-358 outage, capacity.go:321).
func TestCapacityRowDoesNotRetainReplayGaps(t *testing.T) {
	s, snap := capacityFixture()
	s.CapacityWindow = 6 * time.Second // populations above the tail-sample floor vary per rate
	const rows = 3
	inuse := func() uint64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapInuse
	}
	// The rows live only inside the closure; KeepAlive holds them past the
	// first reading, which liveness analysis would otherwise let the GC skip.
	withRows := func() uint64 {
		held := make([]CapacityRow, 0, rows)
		for range rows {
			held = append(held, capacityBuildForTest(t, s, snap))
		}
		if held[0].ConditionalService == nil || held[0].ConditionalService.Queries == 0 {
			t.Fatalf("fixture row carries no conditional evidence: %+v", held[0].ConditionalService)
		}
		reading := inuse()
		runtime.KeepAlive(held)
		return reading
	}()
	withoutRows := inuse()
	// Three rows at this window would retain well over 2 MiB of gaps if the
	// row still pointed into its replay; the rows themselves are kilobytes.
	if withRows > withoutRows && withRows-withoutRows > 2<<20 {
		t.Fatalf("held rows retain %d bytes", withRows-withoutRows)
	}
}
