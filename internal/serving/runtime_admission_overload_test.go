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

const admissionOverloadLegacy = "legacy"

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
// The legacy arm preserves real SLO feasibility/fill and implements the former
// keep fallback: if no normal batch is feasible, execute maximum throughput.
type admissionOverloadRun struct {
	base                   time.Time
	now                    time.Duration
	mode                   string
	rate, offered, arrival int
	runtime                *Runtime
	instance               *runtimeInstance
	workload               *runtimeWorkload
	native                 *firstfloor.State
	policy                 firstfloor.Policy
	rng                    *rand.Rand
	batchID                uint64
	batchFinish            time.Duration
	lastEnd                *time.Duration
	arrivals               admissionArrival
	outcomes               map[string]string
	executed               map[string]bool
	cohort                 map[string]*Call
	counts                 admissionOverloadCounts
}

func runAdmissionOverload(t *testing.T, fixture admissionOverloadFixture, mode string, rate int) admissionOverloadCounts {
	t.Helper()
	s := newAdmissionOverloadRun(t, fixture, mode, rate)
	for range 20000 {
		s.finishNative()
		s.expireRequests()
		s.admitArrival()
		s.observeNative()
		s.routeGateway()
		wake := s.startNative(t)
		if s.arrival == s.offered && len(s.outcomes) == s.offered && s.batchFinish == 0 {
			break
		}
		s.advance(t, wake)
	}
	return s.result(t)
}

func newAdmissionOverloadRun(t *testing.T, fixture admissionOverloadFixture, mode string, rate int) *admissionOverloadRun {
	t.Helper()
	s := &admissionOverloadRun{base: time.Unix(2000000000, 0).UTC(), mode: mode, rate: rate, offered: rate * 30, rng: rand.New(rand.NewPCG(23, 47)), batchID: 1, outcomes: map[string]string{}, executed: map[string]bool{}, cohort: map[string]*Call{}}
	d := &Definition{Name: "measured", UID: "measured", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}, Admission: Admission{Policy: AdmissionPolicyReject, MaxQueuedRequests: 1024}}
	if mode == string(AdmissionPolicyDelay) {
		d.Admission.Policy = AdmissionPolicyDelay
	}
	p := &Profile{ID: fixture.ProfileID, Batches: fixture.Batches}
	s.policy = firstfloor.Policy{ProfileID: p.ID, MaxBatchSize: fixture.MaxBatchSize, MaxBatchWaitMS: 1000, Batches: p.NativeBundle().Batches}
	s.instance = &runtimeInstance{ReadyInstance: ReadyInstance{State: InstanceState{ID: "jetsono", Ready: true, ObservedAt: s.base, Native: &firstfloor.State{Ready: true, Metadata: firstfloor.Metadata{Policy: s.policy}}}, Profile: p, Capacity: 256}, admissionBatches: s.policy.Batches}
	s.workload = &runtimeWorkload{definition: d, identity: d.Identity(), calls: map[string]*Call{}}
	s.workload.route = inputRoute{entries: []routeEntry{{instance: s.instance}}, weights: []int32{1}, current: []int64{0}, available: []bool{false}}
	s.runtime = &Runtime{workloads: map[string]*runtimeWorkload{d.UID: s.workload}, instances: map[string]*runtimeInstance{s.instance.State.ID: s.instance}, calls: map[string]*Call{}}
	s.native = s.instance.State.Native
	for _, b := range fixture.Batches {
		if b.Batch == 16 {
			s.batchFinish = time.Duration(b.ServiceSamples[0].ServiceDurationNanos)
		}
	}
	if s.batchFinish <= 0 {
		t.Fatal("measured initial batch missing")
	}
	for j := range 32 {
		received := s.base.Add(-500 * time.Millisecond)
		c := &Call{receipt: Receipt{ID: fmt.Sprintf("initial-%02d", j), UID: d.UID, ReceivedAt: received, Deadline: received.Add(2 * time.Second)}, definition: d, identity: s.workload.identity}
		s.addNative(c, j < 16)
	}
	return s
}

func (s *admissionOverloadRun) refreshArrival() {
	s.native.ArrivalAgesMS = map[string][]float64{}
	if !s.arrivals.last.IsZero() {
		ages := []float64{float64(s.base.Add(s.now).Sub(s.arrivals.last)) / float64(time.Millisecond)}
		if !s.arrivals.previous.IsZero() {
			ages = append(ages, float64(s.base.Add(s.now).Sub(s.arrivals.previous))/float64(time.Millisecond))
		}
		s.native.ArrivalAgesMS[s.workload.definition.UID] = ages
	}
	for j := range s.native.Requests {
		s.native.Requests[j].ArrivalRate = s.arrivals.rate()
	}
}

func (s *admissionOverloadRun) addNative(c *Call, running bool) {
	q := admissionCallState(c, s.base.Add(s.now))
	q.Context.LifetimeMS, q.Phase = 2000, phaseQueued
	at := s.base.Add(s.now)
	if s.cohort[c.receipt.ID] == nil {
		at = c.receipt.ReceivedAt
		q.Context.SenderElapsedMS = 0
		q.ElapsedMS = float64(s.base.Add(s.now).Sub(at)) / float64(time.Millisecond)
		q.QueueMS = q.ElapsedMS
	}
	s.arrivals = s.arrivals.accepted(at)
	if s.mode == admissionOverloadLegacy {
		q.Context.AdmissionPolicy = string(AdmissionPolicyDelay)
	}
	if running {
		q.Phase, q.BatchID, q.BatchSize = phaseRunning, 1, 16
		zero := int64(0)
		q.ExchangeElapsedNanos = &zero
	}
	s.native.Requests = append(s.native.Requests, q)
	s.refreshArrival()
	c.instance = s.instance
	s.runtime.calls[c.receipt.ID] = c
	s.instance.outstanding++
}

func (s *admissionOverloadRun) complete(id, outcome string) {
	if s.cohort[id] != nil && s.outcomes[id] == "" {
		s.outcomes[id] = outcome
	}
}

func (s *admissionOverloadRun) removePhysical(id string) {
	delete(s.runtime.calls, id)
	s.instance.outstanding--
}

// Physical completion releases occupancy even after a final HTTP timeout.
func (s *admissionOverloadRun) finishNative() {
	if s.batchFinish <= 0 || s.now < s.batchFinish {
		return
	}
	rest := s.native.Requests[:0]
	for _, q := range s.native.Requests {
		if q.Phase != phaseRunning {
			rest = append(rest, q)
			continue
		}
		if c := s.cohort[q.Context.RequestID]; c != nil {
			late := s.base.Add(s.now).Sub(c.receipt.ReceivedAt) > time.Second
			if late {
				s.counts.ExecutedPastSLO++
			}
			if s.outcomes[q.Context.RequestID] == "" {
				if late {
					s.complete(q.Context.RequestID, schedulerOutcomeLate)
				} else {
					s.complete(q.Context.RequestID, schedulerOutcomeOnTime)
				}
			}
		}
		s.removePhysical(q.Context.RequestID)
	}
	s.native.Requests = rest
	s.batchFinish = 0
	end := s.now
	s.lastEnd = &end
}

func (s *admissionOverloadRun) expireRequests() {
	for id, c := range s.cohort {
		if s.outcomes[id] == "" && !s.base.Add(s.now).Before(c.receipt.Deadline) {
			s.complete(id, schedulerOutcomeTimeout)
		}
	}
	s.workload.queue = slices.DeleteFunc(s.workload.queue, func(c *Call) bool {
		if s.outcomes[c.receipt.ID] == "" {
			return false
		}
		delete(s.runtime.calls, c.receipt.ID)
		return true
	})
	rest := s.native.Requests[:0]
	for _, q := range s.native.Requests {
		if q.Phase != phaseRunning && q.Context.SenderElapsedMS+q.ElapsedMS >= 2000 {
			s.complete(q.Context.RequestID, schedulerOutcomeTimeout)
			s.removePhysical(q.Context.RequestID)
		} else {
			rest = append(rest, q)
		}
	}
	s.native.Requests = rest
}

func (s *admissionOverloadRun) nextArrival() time.Duration {
	return time.Duration(int64(s.arrival) * int64(time.Second) / int64(s.rate))
}

func (s *admissionOverloadRun) admitArrival() {
	if s.arrival >= s.offered || s.now < s.nextArrival() {
		return
	}
	at, id := s.base.Add(s.nextArrival()), fmt.Sprintf("offered-%03d", s.arrival)
	c := &Call{receipt: Receipt{ID: id, UID: s.workload.definition.UID, ReceivedAt: at, Deadline: at.Add(2 * time.Second)}, definition: s.workload.definition, identity: s.workload.identity}
	s.cohort[id], s.runtime.calls[id] = c, c
	s.workload.queue = append(s.workload.queue, c)
	s.arrival++
}

func (s *admissionOverloadRun) observeNative() {
	s.instance.State.ObservedAt = s.base.Add(s.now)
	s.refreshArrival()
	if s.lastEnd != nil {
		idle := int64(s.now - *s.lastEnd)
		s.native.LastServiceEndAgeNanos = &idle
	}
}

func (s *admissionOverloadRun) routeGateway() {
	w := s.workload
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
			if s.mode != admissionOverloadLegacy {
				choice = s.runtime.chooseLocked(w.route, c, s.base.Add(s.now))
			}
			if choice.entry == nil {
				if choice.state == CodeDeadlineInfeasible && !c.deferred {
					if w.definition.Admission.Policy == AdmissionPolicyReject {
						s.complete(c.receipt.ID, schedulerOutcomeRejected)
						delete(s.runtime.calls, c.receipt.ID)
						w.queue = slices.Delete(w.queue, index, index+1)
						continue
					}
					c.deferred = true
				}
				index++
				continue
			}
			w.queue = slices.Delete(w.queue, index, index+1)
			s.addNative(c, false)
		}
	}
}

func (s *admissionOverloadRun) transitionNative(t *testing.T, choice firstfloor.BatchChoice) {
	t.Helper()
	index := slices.IndexFunc(s.native.Requests, func(q firstfloor.RequestState) bool { return q.Context.RequestID == choice.RequestID })
	if index < 0 {
		t.Fatal("native lifecycle lost identity")
	}
	q := s.native.Requests[index]
	s.native.Requests = slices.Delete(s.native.Requests, index, index+1)
	if choice.Reason == firstfloor.ReasonDeadlineDefer {
		q.Phase = phaseDeferred
		s.native.Requests = append(s.native.Requests, q)
	} else {
		s.complete(q.Context.RequestID, schedulerOutcomeRejected)
		s.removePhysical(q.Context.RequestID)
	}
}

func (s *admissionOverloadRun) startNative(t *testing.T) time.Duration {
	t.Helper()
	slices.SortStableFunc(s.native.Requests, compareAdmissionQueue)
	for range len(s.native.Requests) + 1 {
		choice := firstfloor.ChooseBatch(s.policy, s.native.Requests, s.native.LastServiceEndAgeNanos)
		if s.mode == admissionOverloadLegacy {
			choice = admissionLegacyKeepChoice(s.policy, s.native.Requests, s.native.LastServiceEndAgeNanos)
		}
		if choice.Reason == firstfloor.OutcomeDeadlineDrop || choice.Reason == firstfloor.ReasonDeadlineDefer {
			s.transitionNative(t, choice)
			continue
		}
		if s.batchFinish > 0 || choice.Size <= 0 {
			if choice.WaitMS > 0 {
				return s.now + time.Duration(math.Ceil(choice.WaitMS*float64(time.Millisecond)))
			}
			return 0
		}
		s.startNativeBatch(t, choice)
		return 0
	}
	return 0
}

func (s *admissionOverloadRun) startNativeBatch(t *testing.T, choice firstfloor.BatchChoice) {
	t.Helper()
	b := admissionBatch(s.policy, choice.Size)
	if b == nil {
		t.Fatal("native batch outside measured support")
	}
	service, _, ok := b.Distribution.Sample(s.native.LastServiceEndAgeNanos, 95, s.rng)
	if !ok {
		t.Fatal("native service support missing")
	}
	s.batchID++
	s.batchFinish = s.now + time.Duration(service)
	deferred := false
	for _, q := range s.native.Requests {
		if q.Context.RequestID == choice.RequestID {
			deferred = q.Phase == phaseDeferred
			break
		}
	}
	selected := 0
	for j := range s.native.Requests {
		q := &s.native.Requests[j]
		if q.Phase != phaseRunning && selected < choice.Size && q.Context.WorkloadUID == choice.WorkloadUID && q.Context.Definition == choice.Definition && (q.Phase == phaseDeferred) == deferred {
			q.Phase, q.BatchID, q.BatchSize = phaseRunning, s.batchID, choice.Size
			q.PrecedingIdleNanos = copyNanos(s.native.LastServiceEndAgeNanos)
			zero := int64(0)
			q.ExchangeElapsedNanos = &zero
			selected++
			if s.cohort[q.Context.RequestID] != nil && !s.executed[q.Context.RequestID] {
				s.executed[q.Context.RequestID] = true
				s.counts.Executed++
			}
		}
	}
}

func (s *admissionOverloadRun) advance(t *testing.T, wake time.Duration) {
	t.Helper()
	next := time.Duration(math.MaxInt64)
	if s.arrival < s.offered {
		next = s.nextArrival()
	}
	if s.batchFinish > s.now {
		next = min(next, s.batchFinish)
	}
	if wake > s.now {
		next = min(next, wake)
	}
	for id, c := range s.cohort {
		if s.outcomes[id] == "" {
			next = min(next, c.receipt.Deadline.Sub(s.base))
		}
	}
	// Initial queued lifetimes also wake the native actor.
	for _, q := range s.native.Requests {
		if q.Phase == phaseRunning {
			continue
		}
		remaining := time.Duration((2000 - q.Context.SenderElapsedMS - q.ElapsedMS) * float64(time.Millisecond))
		if remaining > 0 {
			next = min(next, s.now+remaining)
		}
	}
	if next <= s.now || next == time.Duration(math.MaxInt64) {
		t.Fatalf("virtual actor stalled mode=%s now=%v next=%v offered=%d terminal=%d", s.mode, s.now, next, s.arrival, len(s.outcomes))
	}
	step := next - s.now
	admissionAdvance(s.native.Requests, int64(step))
	for j := range s.native.Requests {
		q := &s.native.Requests[j]
		if q.Phase == phaseRunning && q.ExchangeElapsedNanos != nil {
			*q.ExchangeElapsedNanos += int64(step)
		}
	}
	s.now = next
}

func (s *admissionOverloadRun) result(t *testing.T) admissionOverloadCounts {
	t.Helper()
	for _, outcome := range s.outcomes {
		switch outcome {
		case schedulerOutcomeOnTime:
			s.counts.OnTime++
		case schedulerOutcomeLate:
			s.counts.Late++
		case schedulerOutcomeRejected:
			s.counts.Rejected++
		case schedulerOutcomeTimeout:
			s.counts.Timeout++
		}
	}
	if len(s.outcomes) != s.offered {
		t.Fatalf("mode=%s terminal count=%d", s.mode, len(s.outcomes))
	}
	return s.counts
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
			for _, mode := range []string{admissionOverloadLegacy, string(AdmissionPolicyReject), string(AdmissionPolicyDelay)} {
				results[mode] = runAdmissionOverload(t, fixture, mode, rate)
				t.Logf("%s: %+v", mode, results[mode])
			}
			offered := rate * 30
			legacy, reject, delay := results[admissionOverloadLegacy], results[string(AdmissionPolicyReject)], results[string(AdmissionPolicyDelay)]
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
				31: {admissionOverloadLegacy: {OnTime: 808, Late: 122, Executed: 930, ExecutedPastSLO: 122}, string(AdmissionPolicyReject): {OnTime: 930, Executed: 930}, string(AdmissionPolicyDelay): {OnTime: 930, Executed: 930}},
				44: {admissionOverloadLegacy: {Late: 84, Timeout: 1236, Executed: 1013, ExecutedPastSLO: 1013}, string(AdmissionPolicyReject): {OnTime: 982, Rejected: 338, Executed: 982}, string(AdmissionPolicyDelay): {OnTime: 924, Late: 13, Timeout: 383, Executed: 937, ExecutedPastSLO: 13}},
			}
			for mode, got := range results {
				if got != want[rate][mode] {
					t.Errorf("frozen outcome counts %s got=%+v want=%+v (tolerance 0)", mode, got, want[rate][mode])
				}
			}
		})
	}
}

// This is the old keep branch from 767e470a: shared feasibility/fill remains
// unchanged, and only the no-feasible-batch lifecycle choice is replaced.
func admissionLegacyKeepChoice(p firstfloor.Policy, states []firstfloor.RequestState, gap *int64) firstfloor.BatchChoice {
	choice := firstfloor.ChooseBatch(p, states, gap)
	if choice.Reason != firstfloor.ReasonDeadlineDefer {
		return choice
	}
	head := slices.IndexFunc(states, func(q firstfloor.RequestState) bool { return q.Context.RequestID == choice.RequestID })
	if head < 0 {
		return choice
	}
	r := states[head]
	count := 0
	for _, q := range states {
		if q.Phase == phaseQueued && q.Context.WorkloadUID == r.Context.WorkloadUID && q.Context.Definition == r.Context.Definition {
			count++
		}
	}
	best := math.Inf(1)
	for _, b := range p.Batches {
		if b.Size < 1 || b.Size > count || p.MaxBatchSize > 0 && b.Size > p.MaxBatchSize {
			continue
		}
		service, _, ok := b.Distribution.QuantileNanos(gap, r.Context.SLO.Percentile)
		if !ok {
			continue
		}
		cost := float64(service) / float64(b.Size)
		if cost < best || cost == best && b.Size > choice.Size {
			choice.Size, best = b.Size, cost
		}
	}
	choice.Reason = "maximum_throughput"
	return choice
}

func TestRuntimeAdmissionLegacyKeepPreservesRealSLO(t *testing.T) {
	p := admissionTestPolicy()
	p.MaxBatchWaitMS = 20
	q := admissionTestRequest(admissionOverloadLegacy)
	q.Context.AdmissionPolicy = string(AdmissionPolicyDelay)
	q.Context.SLO.TargetMS = 20
	q.ArrivalRate = 100
	choice := admissionLegacyKeepChoice(p, []firstfloor.RequestState{q}, nil)
	if choice.Size != 1 || choice.WaitMS != 0 {
		t.Fatalf("real SLO no longer constrains legacy fill: %+v", choice)
	}
	q.Context.SLO.TargetMS = 1
	choice = admissionLegacyKeepChoice(p, []firstfloor.RequestState{q}, nil)
	if choice.Size != 1 || choice.Reason != "maximum_throughput" || q.Context.SLO.TargetMS != 1 {
		t.Fatalf("legacy keep did not execute real-SLO-infeasible batch: %+v", choice)
	}
}
