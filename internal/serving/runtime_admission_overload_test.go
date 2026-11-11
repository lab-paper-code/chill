package serving

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

type admissionOverloadCounts struct{ OnTime, Late, Rejected, Timeout, Executed, ExecutedPastSLO int }

type admissionOverloadFixture struct {
	ProfileID    string         `json:"profileID"`
	MaxBatchSize int            `json:"maxBatchSize"`
	Batches      []BatchProfile `json:"batches"`
}

func measuredAdmissionFixture(t *testing.T) admissionOverloadFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/jetsono-ic-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture admissionOverloadFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for j := range fixture.Batches {
		b := &fixture.Batches[j]
		b.ServiceDistribution, err = firstfloor.NewServiceDistribution(b.ServiceSamples)
		if err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

// This fixture exercises Runtime.chooseLocked against a virtual native actor.
// Arrivals are i/rate seconds for 30 seconds; the cohort drains through its original
// two-second lifetime. Physical service uses the complete measured trial/gap
// mixture and PCG(23,47), with no transport or POST reserve. Policy fill budget
// is frozen at 1s. Initial state: one startup batch of 16 (its first measured
// startup service), plus 16 queued requests received 500ms before the cohort.
// Those initial requests occupy the executor but never enter the offered count.
// The legacy arm reproduces keep by suppressing native SLO filtering, while
// retaining the original one-second SLO for outcome classification.
func runAdmissionOverload(t *testing.T, fixture admissionOverloadFixture, mode string, rate int) admissionOverloadCounts {
	t.Helper()
	offered := rate * 30
	base := time.Unix(2000000000, 0).UTC()
	d := &Definition{Name: "measured", UID: "measured", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}, Admission: Admission{Policy: AdmissionPolicyReject, MaxQueuedRequests: 1024}}
	if mode == "Delay" {
		d.Admission.Policy = AdmissionPolicyDelay
	}
	p := &Profile{ID: fixture.ProfileID, Batches: fixture.Batches}
	policy := firstfloor.Policy{ProfileID: p.ID, MaxBatchSize: fixture.MaxBatchSize, MaxBatchWaitMS: 1000, Batches: p.NativeBundle().Batches}
	i := &runtimeInstance{ReadyInstance: ReadyInstance{State: InstanceState{ID: "jetsono", Ready: true, ObservedAt: base, Native: &firstfloor.State{Ready: true, Metadata: firstfloor.Metadata{Policy: policy}}}, Profile: p, Capacity: 256}, admissionBatches: policy.Batches}
	w := &runtimeWorkload{definition: d, identity: d.Identity(), calls: map[string]*Call{}}
	w.route = inputRoute{entries: []routeEntry{{instance: i}}, weights: []int32{1}, current: []int64{0}, available: []bool{false}}
	r := &Runtime{workloads: map[string]*runtimeWorkload{d.UID: w}, instances: map[string]*runtimeInstance{i.State.ID: i}, calls: map[string]*Call{}}
	native := i.State.Native
	outcomes := map[string]string{}
	executed := map[string]bool{}
	cohort := map[string]*Call{}
	counts := admissionOverloadCounts{}
	rng := rand.New(rand.NewPCG(23, 47))
	now := time.Duration(0)
	batchID := uint64(1)
	var batchFinish time.Duration
	var lastEnd *time.Duration
	for _, b := range fixture.Batches {
		if b.Batch == 16 {
			batchFinish = time.Duration(b.ServiceSamples[0].ServiceDurationNanos)
		}
	}
	if batchFinish <= 0 {
		t.Fatal("measured initial batch missing")
	}
	addNative := func(c *Call, running bool) {
		q := admissionCallState(c, base.Add(now))
		q.Context.LifetimeMS = 2000
		q.ArrivalRate = float64(rate)
		if mode == "legacy" {
			q.Context.SLO.TargetMS = 1e12
			q.Context.AdmissionPolicy = "Delay"
		}
		if running {
			q.Phase = phaseRunning
			q.BatchID = 1
			q.BatchSize = 16
			zero := int64(0)
			q.ExchangeElapsedNanos = &zero
		}
		native.Requests = append(native.Requests, q)
		c.instance = i
		r.calls[c.receipt.ID] = c
		i.outstanding++
	}
	for j := range 32 {
		received := base.Add(-500 * time.Millisecond)
		c := &Call{receipt: Receipt{ID: fmt.Sprintf("initial-%02d", j), UID: d.UID, ReceivedAt: received, Deadline: received.Add(2 * time.Second)}, definition: d, identity: w.identity}
		addNative(c, j < 16)
	}
	complete := func(id, outcome string) {
		if _, ok := cohort[id]; ok && outcomes[id] == "" {
			outcomes[id] = outcome
		}
	}
	removePhysical := func(id string) {
		delete(r.calls, id)
		i.outstanding--
	}
	arrival := 0
	for range 20000 {
		// Native completion owns physical release even after a final timeout.
		if batchFinish > 0 && now >= batchFinish {
			rest := native.Requests[:0]
			for _, q := range native.Requests {
				if q.Phase != phaseRunning {
					rest = append(rest, q)
					continue
				}
				if c := cohort[q.Context.RequestID]; c != nil {
					late := base.Add(now).Sub(c.receipt.ReceivedAt) > time.Second
					if late {
						counts.ExecutedPastSLO++
					}
					if outcomes[q.Context.RequestID] == "" {
						if late {
							complete(q.Context.RequestID, "late")
						} else {
							complete(q.Context.RequestID, "onTime")
						}
					}
				}
				removePhysical(q.Context.RequestID)
			}
			native.Requests = rest
			batchFinish = 0
			end := now
			lastEnd = &end
		}
		// Expire queued work and HTTP lifetimes; running occupancy survives.
		for id, c := range cohort {
			if outcomes[id] == "" && !base.Add(now).Before(c.receipt.Deadline) {
				complete(id, "timeout")
			}
		}
		w.queue = slices.DeleteFunc(w.queue, func(c *Call) bool {
			if outcomes[c.receipt.ID] != "" {
				delete(r.calls, c.receipt.ID)
				return true
			}
			return false
		})
		rest := native.Requests[:0]
		for _, q := range native.Requests {
			if q.Phase != phaseRunning && q.Context.SenderElapsedMS+q.ElapsedMS >= 2000 {
				complete(q.Context.RequestID, "timeout")
				removePhysical(q.Context.RequestID)
			} else {
				rest = append(rest, q)
			}
		}
		native.Requests = rest
		if arrival < offered && now >= time.Duration(int64(arrival)*int64(time.Second)/int64(rate)) {
			at := base.Add(time.Duration(int64(arrival) * int64(time.Second) / int64(rate)))
			id := fmt.Sprintf("offered-%03d", arrival)
			c := &Call{receipt: Receipt{ID: id, UID: d.UID, ReceivedAt: at, Deadline: at.Add(2 * time.Second)}, definition: d, identity: w.identity}
			cohort[id] = c
			r.calls[id] = c
			w.queue = append(w.queue, c)
			arrival++
		}
		i.State.ObservedAt = base.Add(now)
		if lastEnd != nil {
			idle := int64(now - *lastEnd)
			native.LastServiceEndAgeNanos = &idle
		}
		for _, delayed := range [...]bool{false, true} {
			index := 0
			for range len(w.queue) {
				if index >= len(w.queue) {
					break
				}
				c := w.queue[index]
				if c.deferred != delayed {
					index++
					continue
				}
				choice := admissionChoice{entry: &w.route.entries[0]}
				if mode != "legacy" {
					choice = r.chooseLocked(w.route, c, base.Add(now))
				}
				if choice.entry == nil {
					if choice.state == CodeDeadlineInfeasible && !c.deferred {
						if d.Admission.Policy == AdmissionPolicyReject {
							complete(c.receipt.ID, "rejected")
							delete(r.calls, c.receipt.ID)
							w.queue = slices.Delete(w.queue, index, index+1)
							continue
						}
						c.deferred = true
					}
					index++
					continue
				}
				w.queue = slices.Delete(w.queue, index, index+1)
				addNative(c, false)
			}
		}
		// Fresh execution-time shared ChooseBatch rechecks actual remaining SLO.
		slices.SortStableFunc(native.Requests, func(a, b firstfloor.RequestState) int {
			if (a.Phase == phaseDeferred) != (b.Phase == phaseDeferred) {
				if a.Phase == phaseDeferred {
					return 1
				}
				return -1
			}
			if a.Phase == phaseDeferred {
				return 0
			}
			x, y := firstfloor.RemainingSLOMS(a), firstfloor.RemainingSLOMS(b)
			if x < y {
				return -1
			}
			if x > y {
				return 1
			}
			return 0
		})
		wake := time.Duration(0)
		for range len(native.Requests) + 1 {
			choice := firstfloor.ChooseBatch(policy, native.Requests, native.LastServiceEndAgeNanos)
			if choice.Reason == firstfloor.OutcomeDeadlineDrop || choice.Reason == firstfloor.ReasonDeadlineDefer {
				index := slices.IndexFunc(native.Requests, func(q firstfloor.RequestState) bool { return q.Context.RequestID == choice.RequestID })
				if index < 0 {
					t.Fatal("native lifecycle lost identity")
				}
				q := native.Requests[index]
				native.Requests = slices.Delete(native.Requests, index, index+1)
				if choice.Reason == firstfloor.ReasonDeadlineDefer {
					q.Phase = phaseDeferred
					native.Requests = append(native.Requests, q)
				} else {
					complete(q.Context.RequestID, "rejected")
					removePhysical(q.Context.RequestID)
				}
				continue
			}
			if batchFinish > 0 || choice.Size <= 0 {
				if choice.WaitMS > 0 {
					wake = now + time.Duration(math.Ceil(choice.WaitMS*float64(time.Millisecond)))
				}
				break
			}
			b := admissionBatch(policy, choice.Size)
			if b == nil {
				t.Fatal("native batch outside measured support")
			}
			sample, _, ok := b.Distribution.Sample(native.LastServiceEndAgeNanos, rng)
			if !ok {
				t.Fatal("native service support missing")
			}
			batchID++
			batchFinish = now + time.Duration(sample.ServiceDurationNanos)
			deferred := false
			for _, q := range native.Requests {
				if q.Context.RequestID == choice.RequestID {
					deferred = q.Phase == phaseDeferred
					break
				}
			}
			selected := 0
			for j := range native.Requests {
				q := &native.Requests[j]
				if q.Phase != phaseRunning && selected < choice.Size && q.Context.WorkloadUID == choice.WorkloadUID && q.Context.Definition == choice.Definition && (q.Phase == phaseDeferred) == deferred {
					q.Phase = phaseRunning
					q.BatchID = batchID
					q.BatchSize = choice.Size
					q.PrecedingIdleNanos = copyNanos(native.LastServiceEndAgeNanos)
					zero := int64(0)
					q.ExchangeElapsedNanos = &zero
					selected++
					if cohort[q.Context.RequestID] != nil && !executed[q.Context.RequestID] {
						executed[q.Context.RequestID] = true
						counts.Executed++
					}
				}
			}
			break
		}
		if arrival == offered && len(outcomes) == offered && batchFinish == 0 {
			break
		}
		next := time.Duration(math.MaxInt64)
		if arrival < offered {
			next = time.Duration(int64(arrival) * int64(time.Second) / int64(rate))
		}
		if batchFinish > now {
			next = min(next, batchFinish)
		}
		if wake > now {
			next = min(next, wake)
		}
		for id, c := range cohort {
			if outcomes[id] == "" {
				next = min(next, c.receipt.Deadline.Sub(base))
			}
		}
		// Initial queued lifetimes also wake the native actor.
		for _, q := range native.Requests {
			if q.Phase != phaseRunning {
				remaining := time.Duration((2000 - q.Context.SenderElapsedMS - q.ElapsedMS) * float64(time.Millisecond))
				if remaining > 0 {
					next = min(next, now+remaining)
				}
			}
		}
		if next <= now || next == time.Duration(math.MaxInt64) {
			t.Fatalf("virtual actor stalled mode=%s now=%v next=%v offered=%d terminal=%d", mode, now, next, arrival, len(outcomes))
		}
		step := next - now
		admissionAdvance(native.Requests, int64(step))
		for j := range native.Requests {
			q := &native.Requests[j]
			if q.Phase == phaseRunning && q.ExchangeElapsedNanos != nil {
				*q.ExchangeElapsedNanos += int64(step)
			}
		}
		now = next
	}
	for _, outcome := range outcomes {
		switch outcome {
		case "onTime":
			counts.OnTime++
		case "late":
			counts.Late++
		case "rejected":
			counts.Rejected++
		case "timeout":
			counts.Timeout++
		}
	}
	if len(outcomes) != offered {
		t.Fatalf("mode=%s terminal count=%d", mode, len(outcomes))
	}
	return counts
}

func TestRuntimeAdmissionMeasuredOverload(t *testing.T) {
	fixture := measuredAdmissionFixture(t)
	// control378's 31/22.5 overload ratio is applied to this profile's actual
	// nearest-busy batch-16 service (502.828838ms, capacity 31.82/s): ceil(43.84)=44/s.
	// The requested 31/s trace is retained separately: this measured profile
	// serves it without sustained overload. No measured duration is rescaled.
	for _, rate := range []int{31, 44} {
		t.Run(fmt.Sprintf("%drps", rate), func(t *testing.T) {
			results := map[string]admissionOverloadCounts{}
			for _, mode := range []string{"legacy", "Reject", "Delay"} {
				results[mode] = runAdmissionOverload(t, fixture, mode, rate)
				t.Logf("%s: %+v", mode, results[mode])
			}
			offered := rate * 30
			legacy, reject, delay := results["legacy"], results["Reject"], results["Delay"]
			if rate == 44 && legacy.OnTime*5 > offered {
				t.Errorf("legacy on-time exceeds 20%% under calibrated overload: %+v", legacy)
			}
			p := &Profile{Batches: fixture.Batches}
			mean, ok := p.MeanNanos(16)
			if !ok {
				t.Fatal("measured batch-16 capacity missing")
			}
			lower := int(.6 * min(float64(offered), 16*float64(30*time.Second)/mean))
			if reject.OnTime < lower || rate == 44 && reject.OnTime < legacy.OnTime+offered/4 {
				t.Errorf("Reject goodput below independent measured-capacity bound %d or improvement margin: %+v", lower, reject)
			}
			if reject.ExecutedPastSLO*100 > 5*reject.Executed {
				t.Errorf("Reject executed-past-SLO exceeds 5%%: %+v", reject)
			}
			if delay.OnTime+delay.Late+delay.Rejected+delay.Timeout != offered || delay.Rejected != 0 {
				t.Errorf("Delay outcome partition: %+v", delay)
			}
			want := map[int]map[string]admissionOverloadCounts{
				31: {"legacy": {OnTime: 727, Late: 203, Executed: 930, ExecutedPastSLO: 203}, "Reject": {OnTime: 930, Executed: 930}, "Delay": {OnTime: 930, Executed: 930}},
				44: {"legacy": {Late: 84, Timeout: 1236, Executed: 1013, ExecutedPastSLO: 1013}, "Reject": {OnTime: 982, Rejected: 338, Executed: 982}, "Delay": {OnTime: 982, Late: 12, Timeout: 326, Executed: 995, ExecutedPastSLO: 13}},
			}
			for mode, got := range results {
				if got != want[rate][mode] {
					t.Errorf("frozen outcome counts %s got=%+v want=%+v (tolerance 0)", mode, got, want[rate][mode])
				}
			}
		})
	}
}
