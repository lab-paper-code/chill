package firstfloor

// The bounded channel/actor and one persistent serial runner are adopted from
// firstfloor. There is no second batching or runner queue in CHILL.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var ErrQueueFull = errors.New("request queue is full")
var ErrUnavailable = errors.New("runner is unavailable")
var ErrDeadlineDrop = fmt.Errorf("request cannot complete within its SLO: %w", context.DeadlineExceeded)

type request struct {
	ctx      context.Context
	context  RequestContext
	received time.Time
	data     [][]byte
	bytes    int
	result   chan result
	profile  *ProfileRequest
	lifetime time.Time
}
type result struct {
	response Response
	profile  ProfileResponse
	err      error
	complete bool
	output   runnerOutput
}
type configChange struct {
	config  *Config
	applied chan struct{}
}
type stateQuery struct{ reply chan State }
type Scheduler struct {
	config    atomic.Pointer[Config]
	ready     atomic.Bool
	requests  chan *request
	changes   chan configChange
	completed chan result
	wake      chan struct{}
	states    chan stateQuery
	done      chan struct{}
	runner    *Runner
	appliedAt time.Time
}

func NewScheduler(ctx context.Context, cfg *Config, runner *Runner) *Scheduler {
	s := &Scheduler{requests: make(chan *request), changes: make(chan configChange), completed: make(chan result, 1), wake: make(chan struct{}, 1), states: make(chan stateQuery), done: make(chan struct{}), runner: runner, appliedAt: time.Now().UTC()}
	s.config.Store(cfg)
	s.ready.Store(true)
	go s.run(ctx)
	return s
}
func (s *Scheduler) Ready() bool           { return s.ready.Load() }
func (s *Scheduler) Done() <-chan struct{} { return s.done }
func (s *Scheduler) submit(ctx context.Context, r *request) result {
	r.ctx = ctx
	r.result = make(chan result, 1)
	select {
	case s.requests <- r:
	case <-ctx.Done():
		return result{err: ctx.Err(), complete: true}
	case <-s.done:
		return result{err: ErrUnavailable, complete: true}
	}
	select {
	case v := <-r.result:
		return v
	case <-ctx.Done():
		select {
		case s.wake <- struct{}{}:
		default:
		}
		return result{err: ctx.Err(), complete: false}
	case <-s.done:
		return <-r.result
	}
}
func (s *Scheduler) Update(ctx context.Context, cfg *Config) error {
	current := s.config.Load()
	if EffectiveDeadlinePolicy(cfg.Policy.DeadlinePolicy) != EffectiveDeadlinePolicy(current.Policy.DeadlinePolicy) {
		return errors.New("deadline policy changes require restart")
	}
	copyCfg := *cfg
	copyCfg.Policy = current.Policy
	// Compare all startup facts; incoming policy alone is mutable.
	comparableCurrent := *current
	comparableCurrent.Policy = copyCfg.Policy
	if !reflect.DeepEqual(copyCfg, comparableCurrent) {
		return errors.New("execution, mode, capacity and immutable profile changes require restart")
	}
	next := *current
	next.Policy = cfg.Policy
	next.Policy.DeadlinePolicy = EffectiveDeadlinePolicy(next.Policy.DeadlinePolicy)
	next.Policy.Batches = current.Policy.Batches
	if next.Policy.ProfileID != current.Policy.ProfileID {
		return errors.New("profile changes require restart")
	}
	// Samples are already sorted and immutable; do not mutate them on updates.
	if !durationOK(next.Policy.CompletionOverheadMS) || !durationOK(next.Policy.MaxBatchWaitMS) || !identity(next.Policy.DecisionID) || next.Policy.EnvelopeSource == "" || len(next.Policy.EnvelopeSource) > 256 {
		return errors.New("invalid mutable policy")
	}
	if err := validateBatchCap(next.Policy, current.BatchSizes); err != nil {
		return err
	}
	change := configChange{&next, make(chan struct{})}
	select {
	case s.changes <- change:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrUnavailable
	}
	select {
	case <-change.applied:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrUnavailable
	}
}
func (s *Scheduler) State(ctx context.Context) (State, error) {
	q := stateQuery{make(chan State, 1)}
	select {
	case s.states <- q:
	case <-ctx.Done():
		return State{}, ctx.Err()
	case <-s.done:
		return State{}, ErrUnavailable
	}
	select {
	case v := <-q.reply:
		return v, nil
	case <-ctx.Done():
		return State{}, ctx.Err()
	case <-s.done:
		return State{}, ErrUnavailable
	}
}
func (s *Scheduler) metadata() Metadata {
	c := s.config.Load()
	return Metadata{Model: c.Model, Mode: c.Mode, Runtime: s.runner.metadata, ProfileID: c.Policy.ProfileID, DecisionID: c.Policy.DecisionID, AppliedAt: s.appliedAt, Policy: c.Policy, MaxQueueRequests: c.MaxQueueRequests, MaxQueueBytes: c.MaxQueueBytes, MaxRequestBytes: c.MaxRequestBytes, MaxHeaderBytes: MaxHeaderBytes, MaxFrameBytes: MaxFrameBytes, MaxProgressBytes: MaxProgressBytes, MaxErrorBytes: MaxErrorBytes, MaxBatchSize: c.MaxBatchSize, ModelInstanceCount: 1}
}

var progressMu sync.Mutex

func emitProgress(p Progress) {
	b, e := json.Marshal(p)
	if e == nil && len(b)+len("CHILL_PROGRESS ") <= MaxProgressBytes {
		progressMu.Lock()
		_, err := fmt.Fprintf(os.Stdout, "CHILL_PROGRESS %s\n", b)
		progressMu.Unlock()
		if err != nil {
			slog.Warn("write native progress", "error", err)
		}
	}
}
func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

type arrivalRate struct {
	last, previous time.Time
	intervalMS     float64
}

// schedulerActor is owned exclusively by run; execution goroutines only send results.
type schedulerActor struct {
	s                               *Scheduler
	cfg                             *Config
	queue, deferred, active         []*request
	queuedBytes                     int
	batchID                         uint64
	started                         time.Time
	activePolicy                    Policy
	accepted, completed, executions uint64
	rates                           map[string]arrivalRate
}

func (s *Scheduler) run(ctx context.Context) {
	cfg := s.config.Load()
	a := schedulerActor{s: s, cfg: cfg, queue: make([]*request, 0, cfg.MaxQueueRequests), rates: map[string]arrivalRate{}}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	defer close(s.done)
	defer s.ready.Store(false)
	defer s.runner.Close()
	for {
		now := time.Now()
		a.cfg = s.config.Load()
		next := a.expireQueued(now)
		next = a.startBatch(ctx, now, next)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		var tick <-chan time.Time
		if !next.IsZero() {
			timer.Reset(time.Until(next))
			tick = timer.C
		}
		select {
		case r := <-s.requests:
			a.admit(r)
		case q := <-s.states:
			q.reply <- a.state(time.Now())
		case change := <-s.changes:
			s.config.Store(change.config)
			a.cfg = change.config
			s.appliedAt = time.Now().UTC()
			close(change.applied)
		case done := <-s.completed:
			if done.err != nil {
				a.fail(fmt.Errorf("%w: %v", ErrUnavailable, done.err))
				return
			}
			if err := a.finishBatch(done); err != nil {
				a.fail(err)
				return
			}
		case <-s.runner.exited:
			a.fail(ErrUnavailable)
			return
		case <-ctx.Done():
			a.fail(ErrUnavailable)
			return
		case <-s.wake:
		case <-tick:
		}
	}
}

func (a *schedulerActor) progress(r *request, phase string, now time.Time, o runnerOutput, complete bool) Progress {
	p := Progress{RequestID: r.context.RequestID, WorkloadUID: r.context.WorkloadUID, Definition: r.context.Definition, Phase: phase, At: now.UTC(), ExecutionComplete: complete, NativeElapsedMS: milliseconds(now.Sub(r.received))}
	inActive := false
	for _, active := range a.active {
		if active == r {
			inActive = true
			break
		}
	}
	if phase == phaseRunning || phase == phaseFinished && inActive {
		p.BatchID = a.batchID
		p.BatchSize = len(a.active)
		if r.profile != nil {
			p.BatchSize = r.profile.BatchSize
		}
		p.ProfileID = a.activePolicy.ProfileID
		p.DecisionID = a.activePolicy.DecisionID
		p.QueueMS = milliseconds(a.started.Sub(r.received))
		p.ServiceMS = o.serviceMS
		p.BackendMS = o.backendMS
	} else {
		p.QueueMS = p.NativeElapsedMS
		p.ProfileID = a.cfg.Policy.ProfileID
		p.DecisionID = a.cfg.Policy.DecisionID
	}
	return p
}
func (a *schedulerActor) fail(e error) {
	a.s.runner.Close()
	now := time.Now()
	for _, pending := range [][]*request{a.queue, a.deferred, a.active} {
		for _, r := range pending {
			p := a.progress(r, phaseFinished, now, runnerOutput{}, true)
			p.Outcome = "runner_terminated"
			emitProgress(p)
			r.result <- result{err: e, complete: true}
			a.completed++
		}
	}
}
func (a *schedulerActor) requestStates(now time.Time) []RequestState {
	out := make([]RequestState, 0, len(a.queue)+len(a.deferred)+len(a.active))
	for index, list := range [][]*request{a.queue, a.deferred, a.active} {
		for _, r := range list {
			phase := phaseQueued
			if index == 1 {
				phase = phaseDeferred
			}
			if index == 2 {
				phase = phaseRunning
			}
			elapsed := milliseconds(now.Sub(r.received))
			q := elapsed
			service := 0.0
			id := uint64(0)
			size := 0
			policy := a.cfg.Policy
			if phase == phaseRunning {
				q = milliseconds(a.started.Sub(r.received))
				service = milliseconds(now.Sub(a.started))
				id = a.batchID
				size = len(a.active)
				if r.profile != nil {
					size = r.profile.BatchSize
				}
				policy = a.activePolicy
			}
			rate := 0.0
			if rr := a.rates[r.context.WorkloadUID]; rr.intervalMS > 0 {
				rate = 1000 / rr.intervalMS
			}
			out = append(out, RequestState{Context: r.context, QueueMS: q, ElapsedMS: elapsed, ArrivalRate: rate, Phase: phase, BatchID: id, BatchSize: size, ServiceElapsedMS: service, ProfileID: policy.ProfileID, DecisionID: policy.DecisionID})
			if !r.lifetime.IsZero() {
				remaining := milliseconds(r.lifetime.Sub(now))
				out[len(out)-1].RemainingLifetimeMS = &remaining
			}
		}
	}
	return out
}
func (a *schedulerActor) state(now time.Time) State {
	ages := make(map[string][]float64, len(a.rates))
	for uid, rr := range a.rates {
		last := milliseconds(now.Sub(rr.last))
		ages[uid] = []float64{last}
		if rr.intervalMS > 0 {
			ages[uid] = append(ages[uid], last+rr.intervalMS)
		}
	}
	return State{At: now.UTC(), Ready: a.s.Ready(), Metadata: a.s.metadata(), Requests: a.requestStates(now), AcceptedRequests: a.accepted, CompletedRequests: a.completed, Executions: a.executions, ArrivalAgesMS: ages}
}
func requestError(r *request, now time.Time) error {
	e := r.ctx.Err()
	if e == nil && !r.lifetime.IsZero() && !now.Before(r.lifetime) {
		e = context.DeadlineExceeded
	}
	return e
}
func (a *schedulerActor) finishQueued(r *request, now time.Time, e error) {
	a.queuedBytes -= r.bytes
	p := a.progress(r, phaseFinished, now, runnerOutput{}, true)
	p.Outcome = e.Error()
	if errors.Is(e, ErrDeadlineDrop) {
		p.Outcome = OutcomeDeadlineDrop
	}
	emitProgress(p)
	r.result <- result{response: Response{ModelName: a.cfg.Model, ID: r.context.RequestID, Progress: p, ExecutionComplete: true}, err: e, complete: true}
	a.completed++
}

func (a *schedulerActor) expireQueued(now time.Time) time.Time {
	var next time.Time
	for index, pending := range []*[]*request{&a.queue, &a.deferred} {
		kept := (*pending)[:0]
		for _, r := range *pending {
			e := requestError(r, now)
			state := RequestState{Context: r.context, ElapsedMS: milliseconds(now.Sub(r.received))}
			drop := a.cfg.Mode == modeServing && index == 0 && EffectiveDeadlinePolicy(a.cfg.Policy.DeadlinePolicy) == DeadlineDrop
			if e == nil && drop && SLOExpired(state) {
				e = ErrDeadlineDrop
			}
			if e != nil {
				a.finishQueued(r, now, e)
				continue
			}
			kept = append(kept, r)
			if !r.lifetime.IsZero() && (next.IsZero() || r.lifetime.Before(next)) {
				next = r.lifetime
			}
			if drop {
				deadline := now.Add(time.Duration(RemainingSLOMS(state) * float64(time.Millisecond)))
				if next.IsZero() || deadline.Before(next) {
					next = deadline
				}
			}
		}
		clear((*pending)[len(kept):])
		*pending = kept
	}
	return next
}
func (a *schedulerActor) startBatch(ctx context.Context, now, next time.Time) time.Time {
	if len(a.active) != 0 {
		return next
	}
	a.started = time.Time{}
	for len(a.queue)+len(a.deferred) > 0 {
		choice := BatchChoice{}
		if a.cfg.Mode == modeProfiling {
			choice = BatchChoice{Size: 1, WorkloadUID: a.queue[0].context.WorkloadUID, Definition: a.queue[0].context.Definition}
		} else {
			choice = ChooseBatch(a.cfg.Policy, a.requestStates(now))
		}
		if choice.Reason == OutcomeDeadlineDrop || choice.Reason == ReasonDeadlineDefer {
			for i, r := range a.queue {
				if r.context.RequestID != choice.RequestID {
					continue
				}
				copy(a.queue[i:], a.queue[i+1:])
				a.queue[len(a.queue)-1] = nil
				a.queue = a.queue[:len(a.queue)-1]
				if choice.Reason == ReasonDeadlineDefer {
					a.deferred = append(a.deferred, r)
					emitProgress(a.progress(r, phaseDeferred, now, runnerOutput{}, false))
				} else {
					a.finishQueued(r, now, ErrDeadlineDrop)
				}
				break
			}
			continue
		}
		if choice.WaitMS > 0 {
			wait := now.Add(time.Duration(choice.WaitMS * float64(time.Millisecond)))
			if next.IsZero() || wait.Before(next) {
				next = wait
			}
		}
		if choice.Size <= 0 {
			return next
		}
		pending := &a.queue
		if len(a.queue) == 0 {
			pending = &a.deferred
		}
		a.active = make([]*request, 0, choice.Size)
		kept := (*pending)[:0]
		for _, r := range *pending {
			if len(a.active) < choice.Size && r.context.WorkloadUID == choice.WorkloadUID && r.context.Definition == choice.Definition {
				a.active = append(a.active, r)
				a.queuedBytes -= r.bytes
			} else {
				kept = append(kept, r)
			}
		}
		clear((*pending)[len(kept):])
		*pending = kept
		a.started = now
		a.activePolicy = a.cfg.Policy
		a.batchID++
		for _, r := range a.active {
			emitProgress(a.progress(r, phaseRunning, now, runnerOutput{}, false))
		}
		go a.s.executeBatch(ctx, a.active, a.batchID, a.cfg)
		break
	}
	return next
}
func (s *Scheduler) executeBatch(ctx context.Context, batch []*request, id uint64, snapshot *Config) {
	if batch[0].profile != nil {
		s.completed <- s.executeProfile(ctx, batch, id, snapshot)
		return
	}
	tensors, data := packBatch(snapshot.Inputs, batch, len(batch))
	o, e := s.runner.infer(ctx, id, tensors, data)
	s.completed <- result{output: o, err: e, complete: true}
}
func (s *Scheduler) executeProfile(ctx context.Context, batch []*request, id uint64, snapshot *Config) result {
	r := batch[0]
	pr := r.profile
	reply := ProfileResponse{BatchSize: pr.BatchSize}
	begin := time.Now()
	tensors, data := packBatch(snapshot.Inputs, batch, pr.BatchSize)
	for range pr.Executions {
		span := time.Now()
		o, e := s.runner.infer(ctx, id, tensors, data)
		if e != nil {
			return result{err: e}
		}
		reply.Outputs, e = fullOutputs(o)
		if e != nil {
			return result{err: e}
		}
		reply.BatchIDs = append(reply.BatchIDs, id)
		reply.NativeSamplesMS = append(reply.NativeSamplesMS, milliseconds(time.Since(span)))
		reply.ServiceSamplesMS = append(reply.ServiceSamplesMS, o.serviceMS)
		reply.BackendSamplesMS = append(reply.BackendSamplesMS, o.backendMS)
		reply.Executions++
		reply.Items += pr.BatchSize
		id++
		if r.ctx.Err() != nil {
			break
		}
	}
	reply.NativeElapsedMS = milliseconds(time.Since(begin))
	reply.ExecutionComplete = true
	return result{profile: reply, complete: true}
}
func (a *schedulerActor) admit(r *request) {
	if r.ctx.Err() != nil {
		r.result <- result{err: r.ctx.Err(), complete: true}
		return
	}
	if len(a.queue)+len(a.deferred) >= a.cfg.MaxQueueRequests || r.bytes > a.cfg.MaxQueueBytes-a.queuedBytes {
		r.result <- result{err: ErrQueueFull, complete: true}
		return
	}
	a.observeArrival(r)
	if a.cfg.Mode == modeProfiling {
		a.queue = append(a.queue, r)
	} else {
		now := time.Now()
		slack := RemainingSLOMS(RequestState{Context: r.context, ElapsedMS: milliseconds(now.Sub(r.received))})
		index := sort.Search(len(a.queue), func(i int) bool {
			q := a.queue[i]
			return RemainingSLOMS(RequestState{Context: q.context, ElapsedMS: milliseconds(now.Sub(q.received))}) > slack
		})
		a.queue = append(a.queue, nil)
		copy(a.queue[index+1:], a.queue[index:])
		a.queue[index] = r
	}
	a.queuedBytes += r.bytes
	a.accepted++
	emitProgress(a.progress(r, phaseQueued, time.Now(), runnerOutput{}, false))
}
func (a *schedulerActor) observeArrival(r *request) {
	if _, known := a.rates[r.context.WorkloadUID]; !known && len(a.rates) >= a.cfg.MaxQueueRequests {
		oldest := ""
		var at time.Time
		for uid, rr := range a.rates {
			if at.IsZero() || rr.last.Before(at) {
				oldest = uid
				at = rr.last
			}
		}
		delete(a.rates, oldest)
	}
	rr := a.rates[r.context.WorkloadUID]
	if rr.last.IsZero() || !r.received.Before(rr.last) {
		rr.previous = rr.last
		rr.last = r.received
	} else if rr.previous.IsZero() || r.received.After(rr.previous) {
		rr.previous = r.received
	}
	if !rr.previous.IsZero() {
		rr.intervalMS = milliseconds(rr.last.Sub(rr.previous))
	}
	a.rates[r.context.WorkloadUID] = rr
}
func (a *schedulerActor) finishBatch(done result) error {
	now := time.Now()
	a.completed += uint64(len(a.active))
	if a.active[0].profile != nil {
		a.executions += uint64(done.profile.Executions)
		r := a.active[0]
		done.profile.NativeElapsedMS = milliseconds(now.Sub(r.received))
		a.batchID += uint64(done.profile.Executions - 1)
		r.result <- done
		span := runnerOutput{}
		for _, v := range done.profile.ServiceSamplesMS {
			span.serviceMS += v
		}
		for _, v := range done.profile.BackendSamplesMS {
			span.backendMS += v
		}
		emitProgress(a.progress(r, phaseFinished, now, span, true))
	} else {
		a.executions++
		outputs, e := splitOutputs(done.output, len(a.active))
		if e != nil {
			return e
		}
		now = time.Now()
		for i, r := range a.active {
			p := a.progress(r, phaseFinished, now, done.output, true)
			p.Outcome = outcomeCompleted
			e := requestError(r, now)
			if e != nil {
				p.Outcome = e.Error()
			}
			emitProgress(p)
			r.result <- result{response: Response{ModelName: a.cfg.Model, ID: r.context.RequestID, Outputs: outputs[i], Progress: p, ExecutionComplete: true}, err: e, complete: true}
		}
	}
	clear(a.active)
	a.active = nil
	a.started = time.Time{}
	return nil
}
func packBatch(specs []InputSpec, requests []*request, size int) ([]wireTensor, [][]byte) {
	tensors := make([]wireTensor, len(specs))
	var chunks [][]byte
	if len(requests) == 1 {
		chunks = requests[0].data
	} else {
		chunks = make([][]byte, 0, len(specs)*len(requests))
	}
	for i, s := range specs {
		count := 0
		for _, r := range requests {
			count += len(r.data[i])
			if len(requests) > 1 {
				chunks = append(chunks, r.data[i])
			}
		}
		shape := make([]int, len(s.Shape)+1)
		shape[0] = size
		copy(shape[1:], s.Shape)
		tensors[i] = wireTensor{InputSpec: InputSpec{s.Name, s.Datatype, shape}, Bytes: count}
	}
	return tensors, chunks
}
func splitOutputs(o runnerOutput, size int) ([][]Tensor, error) {
	out := make([][]Tensor, size)
	offset := 0
	for _, t := range o.tensors {
		per := t.Bytes / size
		for i := range size {
			shape := append([]int{1}, t.Shape[1:]...)
			v, e := decodeTensor(InputSpec{t.Name, t.Datatype, shape}, o.data[offset+i*per:offset+(i+1)*per])
			if e != nil {
				return nil, e
			}
			out[i] = append(out[i], v)
		}
		offset += t.Bytes
	}
	return out, nil
}
func initialized(sizes []int, k int) bool {
	i := sort.SearchInts(sizes, k)
	return i < len(sizes) && sizes[i] == k
}
func fullOutputs(o runnerOutput) ([]Tensor, error) {
	out := make([]Tensor, 0, len(o.tensors))
	offset := 0
	for _, t := range o.tensors {
		v, e := decodeTensor(t.InputSpec, o.data[offset:offset+t.Bytes])
		if e != nil {
			return nil, e
		}
		out = append(out, v)
		offset += t.Bytes
	}
	return out, nil
}
