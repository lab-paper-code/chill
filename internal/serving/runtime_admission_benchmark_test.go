package serving

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkDispatchAdmission(b *testing.B) {
	for _, test := range []struct {
		name               string
		noCapacity, reused bool
	}{
		{name: "Unknown100x1024"},
		{name: "Unknown100x1024Reused", reused: true},
		{name: "NoCapacity100x1024", noCapacity: true},
	} {
		b.Run(test.name, func(b *testing.B) {
			log, err := newRuntimeTestLog(RuntimeTestObservationConfig{Directory: b.TempDir(), Store: "evidence", Retention: time.Hour, Writer: &observationArchive{directory: b.TempDir()}})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = log.Close(context.Background()) })
			_, snapshot := schedulerFixture()
			d := snapshot.Workloads[0]
			p := snapshot.Instances[0].Profile
			now := time.Now()
			elapsed := int64(time.Second)
			w := &runtimeWorkload{definition: d, identity: d.Identity(), calls: map[string]*Call{}}
			r := &Runtime{config: RuntimeConfig{ObservationLog: log}, workloads: map[string]*runtimeWorkload{d.UID: w}, instances: map[string]*runtimeInstance{}, calls: map[string]*Call{}}
			for j := range 1024 {
				c := &Call{receipt: Receipt{ID: fmt.Sprintf("waiting-%d", j), UID: d.UID, ReceivedAt: now, Deadline: now.Add(time.Hour)}, definition: d, identity: w.identity}
				w.queue = append(w.queue, c)
				w.calls[c.receipt.ID] = c
				r.calls[c.receipt.ID] = c
			}
			for n := range 100 {
				i := &runtimeInstance{ReadyInstance: ReadyInstance{State: InstanceState{ID: fmt.Sprintf("executor-%d", n), Ready: true, ObservedAt: now, Native: runtimeTestNative(p)}, Profile: p, Capacity: 1}, admissionBatches: p.NativeBundle().Batches}
				if test.noCapacity {
					i.outstanding = 1
				}
				for j := range 1024 {
					q := admissionTestRequest(fmt.Sprintf("native-%d-%d", n, j))
					if j == 1023 {
						q.Phase = phaseRunning
						q.BatchID = 1
						q.BatchSize = 1
						q.ExchangeElapsedNanos = &elapsed
					}
					i.State.Native.Requests = append(i.State.Native.Requests, q)
				}
				r.instances[i.State.ID] = i
				w.route.entries = append(w.route.entries, routeEntry{instance: i})
				w.route.weights = append(w.route.weights, 1)
				w.route.current = append(w.route.current, 0)
				w.route.available = append(w.route.available, false)
			}
			if test.reused {
				r.mu.Lock()
				r.dispatchLocked()
				r.mu.Unlock()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				r.mu.Lock()
				r.dispatchLocked()
				r.mu.Unlock()
			}
		})
	}
}
