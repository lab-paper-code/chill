package serving

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"sync/atomic"
	"time"
)

type RuntimeConfig struct {
	QueueLimit     int
	ObservationLog *ObservationLog
	Diagnostics    *Diagnostics
}

type Receipt struct {
	ID, Name, UID        string
	ReceivedAt, Deadline time.Time
}
type callResult struct {
	result BackendResult
	err    error
}

type Call struct {
	runtime                                      *Runtime
	receipt                                      Receipt
	definition                                   *Definition
	identity                                     string
	input                                        *InferenceInput
	reply                                        chan callResult
	assignment                                   *Assignment
	instance                                     *runtimeInstance
	phase                                        string
	progress                                     *BackendProgress
	exchangeFinishedAt                           time.Time
	clientSpan                                   time.Duration
	backendContext                               context.Context
	cancelBackend                                context.CancelFunc
	transportDone, backendDone, responseRecorded bool
	closeNext                                    *Call
}

type runtimeInstance struct {
	ReadyInstance
	outstanding int
	bindings    map[string]bool
}
type routeEntry struct {
	instance *runtimeInstance
	path     *ModelPath
}

// inputRoute distributes a Workload's samples; the raw contract is common to
// every execution path, so one route spans all of them.
type inputRoute struct {
	evidence  bool
	entries   []routeEntry
	weights   []int32
	current   []int64
	available []bool
}
type runtimeWorkload struct {
	definition *Definition
	identity   string
	stopped    bool
	detached   bool
	reason     string
	route      inputRoute
	queue      []*Call
	calls      map[string]*Call
}

// The owner lock protects routing and physical ownership, never observation
// filesystem work. An assigned call reserves capacity while its row is stored.
type Runtime struct {
	mu                diagnosticMutex
	config            RuntimeConfig
	ctx               context.Context
	cancel            context.CancelFunc
	workloads         map[string]*runtimeWorkload
	names             map[string]string
	instances         map[string]*runtimeInstance
	profiles          []Profile
	measurement       []NodeReference
	routingControlled bool
	completionMargin  float64
	routeTargets      map[string]map[string]int32
	calls             map[string]*Call
	definitions       map[string]*Definition
	changed           chan struct{}
	observationError  error
	closing           *Call
	finalizing        int
	shuttingDown      bool
	overloaded        atomic.Bool
}

func NewRuntime(ctx context.Context, config RuntimeConfig) (*Runtime, error) {
	if config.QueueLimit <= 0 || config.ObservationLog == nil {
		return nil, errors.New("positive queue limit and observation log are required")
	}
	backendCtx, cancel := context.WithCancel(ctx)
	r := &Runtime{config: config, ctx: backendCtx, cancel: cancel, workloads: map[string]*runtimeWorkload{}, names: map[string]string{}, instances: map[string]*runtimeInstance{}, calls: map[string]*Call{}, definitions: map[string]*Definition{}, changed: make(chan struct{}, 1)}
	r.mu.recorder, r.mu.id = config.Diagnostics, 1
	definitions, pending := config.ObservationLog.runtimeFacts()
	r.definitions = definitions
	// Assigned rows without a final row are observed to completion, never
	// executed again; each closes with one incomplete final row.
	for _, row := range pending {
		d := definitions[row.DefinitionID]
		if d == nil || row.Node == nil || row.DispatchedAt == nil {
			cancel()
			return nil, errors.New("unfinished assignment has no retained definition")
		}
		w := r.workloads[d.UID]
		if w == nil {
			w = &runtimeWorkload{definition: d, identity: row.DefinitionID, stopped: true, reason: "RestartDrain", calls: map[string]*Call{}}
			r.workloads[d.UID] = w
			r.names[d.Name] = d.UID
		}
		assignment := &Assignment{Node: *row.Node, Instance: row.Instance, Profile: row.ProfileID, DispatchedAt: *row.DispatchedAt}
		c := &Call{runtime: r, receipt: Receipt{ID: row.RequestID, Name: d.Name, UID: d.UID, ReceivedAt: row.ReceivedAt}, definition: d, identity: row.DefinitionID, assignment: assignment, phase: phaseAssigned, transportDone: true}
		if row.LifetimeNanos > 0 {
			c.receipt.Deadline = c.receipt.ReceivedAt.Add(time.Duration(row.LifetimeNanos))
		}
		instance := r.instances[c.assignment.Instance]
		if instance == nil {
			instance = &runtimeInstance{ReadyInstance: ReadyInstance{State: InstanceState{ID: c.assignment.Instance, Node: c.assignment.Node, Profile: c.assignment.Profile, Reason: "RestartUnconfirmed"}}, bindings: map[string]bool{}}
			r.instances[c.assignment.Instance] = instance
		}
		instance.bindings[d.UID] = true
		instance.outstanding++
		c.instance = instance
		if err := r.event(c, eventResponseFinished, outcomeIncomplete, "EntrypointRestarted"); err != nil {
			cancel()
			return nil, err
		}
		c.responseRecorded = true
		r.calls[c.receipt.ID] = c
		w.calls[c.receipt.ID] = c
	}
	go r.watchHeap()
	return r, nil
}

func (r *Runtime) signal() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}
func (r *Runtime) Changed() <-chan struct{} { return r.changed }

// RetainedDefinition serves only the persisted applied snapshot needed at
// startup. It does not choose among versions or reconstruct it from new spec.
func (r *Runtime) RetainedDefinition(identity string) *Definition {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.definitions[identity]
}

func (r *Runtime) event(c *Call, kind, outcome, code string) error {
	return r.eventAt(c, kind, outcome, code, time.Now())
}
func (r *Runtime) eventAt(c *Call, kind, outcome, code string, now time.Time) error {
	if err := r.config.ObservationLog.RequestEvent(requestEvent(c, kind, outcome, code, now)); err != nil {
		r.observationError = err
		return err
	}
	return nil
}

func requestEvent(c *Call, kind, outcome, code string, now time.Time) RequestEvent {
	e := RequestEvent{RequestID: c.receipt.ID, WorkloadName: c.receipt.Name, WorkloadUID: c.receipt.UID, Definition: c.identity, Kind: kind, At: now.UTC(), ReceivedAt: c.receipt.ReceivedAt, ElapsedNanos: now.Sub(c.receipt.ReceivedAt).Nanoseconds(), Assignment: c.assignment, Outcome: outcome, Error: code}
	if !c.receipt.Deadline.IsZero() {
		e.LifetimeNanos = c.receipt.Deadline.Sub(c.receipt.ReceivedAt).Nanoseconds()
	}
	e.NativeProgress = c.progress
	e.ClientSpanNanos = int64(c.clientSpan)
	e.ExecutionComplete = c.backendDone
	if c.definition != nil {
		e.SLO = &c.definition.SLO
	}
	return e
}

func (r *Runtime) Receive(name string, at time.Time) (Receipt, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Receipt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	receipt := Receipt{ID: hex.EncodeToString(random[:]), Name: name, UID: r.names[name], ReceivedAt: at}
	c := &Call{receipt: receipt}
	if err := r.event(c, phaseReceived, "", ""); err != nil {
		return receipt, failure("ObservationUnavailable", "cannot persist request receipt")
	}
	if receipt.UID == "" {
		return receipt, failure("WorkloadNotFound", "workload %q is not registered", name)
	}
	return receipt, nil
}

func (r *Runtime) Accept(receipt Receipt, input *InferenceInput) (*Call, error) {
	r.mu.LockRequest(receipt.ID)
	if !receipt.Deadline.IsZero() && !time.Now().Before(receipt.Deadline) {
		r.mu.Unlock()
		return nil, failure("RequestTimeout", "request lifetime expired before acceptance")
	}
	w := r.workloads[receipt.UID]
	if w == nil || r.names[receipt.Name] != receipt.UID {
		r.mu.Unlock()
		return nil, failure("WorkloadChanged", "registration changed before acceptance")
	}
	d := w.definition
	r.mu.Unlock()
	if err := validateInput(input, d.Preprocessor.Input); err != nil {
		return nil, err
	}
	lifetime := int64(0)
	if !receipt.Deadline.IsZero() {
		lifetime = receipt.Deadline.Sub(receipt.ReceivedAt).Nanoseconds()
	}
	validatedAt := time.Now()
	if err := r.config.ObservationLog.RequestEvent(RequestEvent{RequestID: receipt.ID, WorkloadName: receipt.Name, WorkloadUID: receipt.UID, Definition: d.Identity(), Kind: "validated", At: validatedAt.UTC(), ReceivedAt: receipt.ReceivedAt, ElapsedNanos: validatedAt.Sub(receipt.ReceivedAt).Nanoseconds(), SLO: &d.SLO, LifetimeNanos: lifetime}); err != nil {
		return nil, failure("ObservationUnavailable", "cannot persist validated compute demand")
	}
	r.mu.LockRequest(receipt.ID)
	defer r.mu.Unlock()
	if r.shuttingDown {
		return nil, failure(reasonShuttingDown, "entry point no longer accepts requests")
	}
	w = r.workloads[receipt.UID]
	if w == nil || r.names[receipt.Name] != receipt.UID {
		return nil, failure("WorkloadChanged", "registration changed before acceptance")
	}
	if w.stopped {
		return nil, failure(w.reason, "new requests are stopped: %s", w.reason)
	}
	if r.observationError != nil {
		return nil, failure("ObservationUnavailable", "request observation log is unavailable")
	}
	if !w.definition.SameExecution(d) {
		return nil, failure("DefinitionChanged", "model or preprocessing changed before acceptance")
	}
	if w.definition.Dataset == nil {
		return nil, failure("MissingDataset", "workload names no dataset for its measurement")
	}
	if !w.route.evidence {
		return nil, failure("MissingEvidence", "no matching execution evidence for this input")
	}
	if len(w.queue) >= r.config.QueueLimit {
		return nil, failure("QueueFull", "workload ingress queue limit reached")
	}
	if r.overloaded.Load() {
		return nil, failure(reasonGatewayOverloaded, "gateway live heap is near its memory limit")
	}
	c := &Call{runtime: r, receipt: receipt, definition: w.definition, identity: w.identity, input: input, reply: make(chan callResult, 1)}
	if err := r.event(c, phaseAccepted, "", ""); err != nil {
		return nil, failure("ObservationUnavailable", "cannot persist request acceptance")
	}
	w.calls[receipt.ID] = c
	r.calls[receipt.ID] = c
	w.queue = append(w.queue, c)
	r.dispatchLocked()
	r.signal()
	return c, nil
}

func (c *Call) Wait(ctx context.Context) (BackendResult, error) {
	c.runtime.config.Diagnostics.Mark(DiagnosticWait, DiagnosticBegin, c.receipt.ID, 0)
	select {
	case result := <-c.reply:
		c.runtime.config.Diagnostics.Mark(DiagnosticWait, DiagnosticReceived, c.receipt.ID, 0)
		return result.result, result.err
	case <-ctx.Done():
		c.runtime.config.Diagnostics.Mark(DiagnosticWait, DiagnosticCanceled, c.receipt.ID, 0)
		c.runtime.mu.LockRequest(c.receipt.ID)
		if c.assignment == nil {
			c.runtime.removeQueuedLocked(c)
			c.backendDone = true
			c.transportDone = true
		}
		if c.cancelBackend != nil {
			c.cancelBackend()
		}
		c.runtime.mu.Unlock()
		c.runtime.signal()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return BackendResult{}, failure("RequestTimeout", "explicit request timeout reached")
		}
		return BackendResult{}, failure("RequestCanceled", "caller canceled the request")
	}
}

func (r *Runtime) Response(receipt Receipt, outcome, code string) (err error) {
	finishedAt := time.Now()
	r.config.Diagnostics.Mark(DiagnosticResponse, DiagnosticBegin, receipt.ID, 0)
	if r.config.Diagnostics.Active() {
		defer r.config.Diagnostics.Mark(DiagnosticResponse, DiagnosticEnd, receipt.ID, 0)
	}
	r.mu.LockRequest(receipt.ID)
	defer func() { err = errors.Join(err, r.unlock()) }()
	c := r.calls[receipt.ID]
	live := c != nil
	if !live {
		c = &Call{receipt: receipt}
	}
	if c.responseRecorded {
		return nil
	}
	if err := r.eventAt(c, eventResponseFinished, outcome, code, finishedAt); err != nil {
		return err
	}
	c.responseRecorded = true
	if !live {
		// A request rejected before acceptance closes with its response.
		r.closeLocked(c)
		return nil
	}
	r.releaseLocked(c)
	r.dispatchLocked()
	r.signal()
	return nil
}

func (r *Runtime) closeLocked(c *Call) {
	r.finalizing++
	c.closeNext = r.closing
	r.closing = c
}

// unlock finishes this transition's final rows in the same caller, outside the
// owner lock. Failed rows stay in ObservationLog for its existing Flush retry.
func (r *Runtime) unlock() error {
	closing := r.closing
	r.closing = nil
	r.mu.Unlock()
	var result error
	for c := closing; c != nil; {
		next := c.closeNext
		c.closeNext = nil
		err := r.config.ObservationLog.RequestClosed(c.receipt.ID)
		r.mu.Lock()
		r.finalizing--
		if err != nil {
			r.observationError = err
			result = errors.Join(result, err)
		}
		r.signal()
		r.mu.Unlock()
		c = next
	}
	return result
}

func (r *Runtime) removeQueuedLocked(c *Call) {
	w := r.workloads[c.receipt.UID]
	if w == nil {
		return
	}
	if index := slices.Index(w.queue, c); index >= 0 {
		w.queue = slices.Delete(w.queue, index, index+1)
	}
}

func (r *Runtime) chooseLocked(route inputRoute) *routeEntry {
	if r.mu.recorder.Active() {
		r.mu.mark(DiagnosticChoose, DiagnosticBegin)
		defer r.mu.mark(DiagnosticChoose, DiagnosticEnd)
	}
	for index, entry := range route.entries {
		route.available[index] = entry.instance.State.Ready && !entry.instance.State.Stopped && entry.instance.outstanding < entry.instance.Capacity
	}
	index := SmoothWeightedRoute(route.weights, route.current, route.available)
	if index < 0 {
		return nil
	}
	return &route.entries[index]
}

func (r *Runtime) dispatchLocked() {
	if r.mu.recorder.Active() {
		r.mu.mark(DiagnosticDispatch, DiagnosticBegin)
		defer r.mu.mark(DiagnosticDispatch, DiagnosticEnd)
	}
	// The ingress owner promptly distributes accepted samples to ready capacity.
	// The firstfloor actor owns actual compatible batching and additional wait.
	for _, w := range r.workloads {
		for len(w.queue) > 0 {
			c := w.queue[0]
			if !c.receipt.Deadline.IsZero() && !time.Now().Before(c.receipt.Deadline) {
				r.removeQueuedLocked(c)
				c.backendDone = true
				c.transportDone = true
				r.config.Diagnostics.Mark(DiagnosticReply, DiagnosticBegin, c.receipt.ID, 0)
				c.reply <- callResult{err: failure("RequestTimeout", "request lifetime expired in ingress queue")}
				r.config.Diagnostics.Mark(DiagnosticReply, DiagnosticEnd, c.receipt.ID, 0)
				continue
			}
			entry := r.chooseLocked(w.route)
			if entry == nil {
				break
			}
			i := entry.instance
			c.assignment = &Assignment{Node: i.State.Node, Instance: i.State.ID, Artifact: entry.path.Artifact, ExecutionPath: entry.path.Name, Profile: i.Profile.ID, DispatchedAt: time.Now().UTC()}
			c.assignment.ProfileBody = i.Profile
			// Reserve the physical owner before releasing the lock; the executor
			// stores its assignment before it can contact this native instance.
			r.removeQueuedLocked(c)
			c.instance = i
			c.phase = phaseAssigned
			i.outstanding++
			c.backendContext, c.cancelBackend = context.WithCancel(r.ctx)
			dispatch := Dispatch{RequestID: c.receipt.ID, Definition: c.definition, Input: c.input, Assignment: *c.assignment, ReceivedAt: c.receipt.ReceivedAt, Elapsed: time.Since(c.receipt.ReceivedAt), Deadline: c.receipt.Deadline, CompletionMarginMS: r.completionMargin}
			c.input = nil // Only this Dispatch reads the sample, and no path requeues the call.
			assigned := requestEvent(c, phaseAssigned, "", "", c.assignment.DispatchedAt)
			go r.execute(c, i.Execute, dispatch, assigned)
		}
	}
}

func (r *Runtime) execute(c *Call, execute func(context.Context, Dispatch) (BackendResult, error), dispatch Dispatch, assigned RequestEvent) {
	var result BackendResult
	persistErr := r.config.ObservationLog.RequestEvent(assigned)
	var err error
	if persistErr != nil {
		result.Finished = true // No native exchange began or can still execute.
		err = failure("ObservationUnavailable", "cannot persist assignment")
	} else if c.backendContext.Err() != nil {
		result.Finished = true
		err = failure("RequestCanceled", "caller canceled before native dispatch")
	} else if !dispatch.Deadline.IsZero() && !time.Now().Before(dispatch.Deadline) {
		result.Finished = true
		err = failure("RequestTimeout", "request lifetime expired before native dispatch")
	} else {
		dispatch.Elapsed = time.Since(dispatch.ReceivedAt)
		result, err = execute(c.backendContext, dispatch)
	}
	finishedAt := time.Now()
	r.config.Diagnostics.Mark(DiagnosticBackendReturn, DiagnosticEnd, c.receipt.ID, 0)
	outcome, code := outcomeCompleted, ""
	if err != nil {
		var domain *Error
		if !errors.As(err, &domain) {
			err = failure("BackendUnavailable", "native backend did not complete a valid exchange")
		}
		errors.As(err, &domain)
		outcome, code = "error", domain.Code
		switch domain.Code {
		case "SLODeadlineDrop":
			outcome = "timeout"
		case "PreprocessFailed":
			outcome = "preprocess_failed"
		}
	}
	r.mu.LockRequest(c.receipt.ID)
	defer func() {
		// unlock retains async final-write failures in observationError and signals its owner.
		_ = r.unlock()
	}()
	if persistErr != nil {
		r.observationError = persistErr
		c.assignment = nil
	}
	c.transportDone = true
	if c.cancelBackend != nil {
		c.cancelBackend()
		c.cancelBackend = nil
	}
	c.clientSpan = result.ClientSpan
	c.exchangeFinishedAt = finishedAt
	if result.Progress != nil {
		c.progress = result.Progress
	}
	if result.Finished && !c.backendDone {
		c.backendDone = true
		if eventErr := r.eventAt(c, "backend_finished", "", "", finishedAt); eventErr != nil {
			err = failure("ObservationUnavailable", "cannot persist native completion")
		}
	}
	if eventErr := r.eventAt(c, eventBackendExchangeFinished, outcome, code, finishedAt); eventErr != nil {
		err = failure("ObservationUnavailable", "cannot persist backend exchange outcome")
	}
	r.releaseLocked(c)
	if c.reply != nil {
		r.config.Diagnostics.Mark(DiagnosticReply, DiagnosticBegin, c.receipt.ID, 0)
		c.reply <- callResult{result: result, err: err}
		r.config.Diagnostics.Mark(DiagnosticReply, DiagnosticEnd, c.receipt.ID, 0)
	}
	r.dispatchLocked()
	r.signal()
}

func (r *Runtime) releaseLocked(c *Call) {
	if r.mu.recorder.Active() {
		r.mu.mark(DiagnosticRelease, DiagnosticBegin)
		defer r.mu.mark(DiagnosticRelease, DiagnosticEnd)
	}
	if c.backendDone {
		r.pruneInstanceBindingsLocked()
	}
	if !c.backendDone || !c.transportDone {
		return
	}
	if c.instance != nil {
		c.instance.outstanding--
		c.instance = nil
	}
	if !c.responseRecorded {
		return
	}
	delete(r.calls, c.receipt.ID)
	if w := r.workloads[c.receipt.UID]; w != nil {
		delete(w.calls, c.receipt.ID)
	}
	r.closeLocked(c)
}

// ProgressSince bounds a native log follow: progress older than since is
// already recorded unless an outstanding assignment was dispatched earlier,
// as restored assignments from a previous process are. Zero means everything.
func (r *Runtime) ProgressSince(instance string, since time.Time) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c.assignment != nil && c.assignment.Instance == instance && c.assignment.DispatchedAt.Before(since) {
			since = c.assignment.DispatchedAt
		}
	}
	return since
}

func (r *Runtime) ObserveBackend(instance string, progress BackendProgress) (err error) {
	r.mu.Lock()
	defer func() { err = errors.Join(err, r.unlock()) }()
	e := RequestEvent{RequestID: progress.RequestID, WorkloadUID: progress.WorkloadUID, Definition: progress.Definition, Kind: "backend_" + progress.Phase, At: progress.At, NativeInstance: instance, Outcome: progress.Outcome, Error: progress.Error}
	e.NativeProgress = &progress
	e.ExecutionComplete = progress.ExecutionComplete
	c := r.calls[progress.RequestID]
	if c == nil {
		// Preserve late direct progress even after the HTTP call settled.
		r.mu.Unlock()
		err := r.config.ObservationLog.RecordNativeSupplement(e)
		r.mu.Lock()
		return err
	}
	if d := r.definitions[progress.Definition]; d != nil {
		e.WorkloadName = d.Name
		e.SLO = &d.SLO
	}
	if c.assignment == nil || c.assignment.Instance != instance || c.receipt.UID != progress.WorkloadUID || c.identity != progress.Definition {
		return failure("BackendContextMismatch", "native progress does not match its assigned request")
	}
	e.Assignment = c.assignment
	e.ReceivedAt = c.receipt.ReceivedAt
	if !c.receipt.Deadline.IsZero() {
		e.LifetimeNanos = c.receipt.Deadline.Sub(c.receipt.ReceivedAt).Nanoseconds()
	}
	if !c.backendDone {
		c.progress = &progress
		if progress.Phase == phaseRunning || ((progress.Phase == phaseQueued || progress.Phase == phaseDeferred) && c.phase != phaseRunning) {
			c.phase = progress.Phase
		}
		if progress.Phase == phaseFinished && progress.ExecutionComplete {
			c.phase = phaseFinished
			c.backendDone = true
		}
	}
	if err := r.config.ObservationLog.RequestEvent(e); err != nil {
		r.observationError = err
		return err
	}
	r.releaseLocked(c)
	r.dispatchLocked()
	r.signal()
	return nil
}

// ConfirmInstanceStopped requires kubelet-reported container termination, not a
// failed readiness probe, deleted desired resource, or disconnected transport.
func (r *Runtime) ConfirmInstanceStopped(instance string) (err error) {
	r.mu.Lock()
	defer func() { err = errors.Join(err, r.unlock()) }()
	if i := r.instances[instance]; i != nil {
		i.State.Ready = false
		i.State.Reason = "InstanceStopped"
	}
	for _, c := range r.calls {
		if c.assignment != nil && c.assignment.Instance == instance && !c.backendDone {
			c.backendDone = true
			c.phase = phaseFinished
			if err := r.event(c, "backend_finished", "error", "InstanceStopped"); err != nil {
				return err
			}
			r.releaseLocked(c)
		}
	}
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return nil
}

func (r *Runtime) DisableInstance(id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := r.instances[id]; i != nil {
		i.State.Ready = false
		i.State.Reason = reason
	}
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
}

func (r *Runtime) DetachNode(uid string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c.assignment != nil && c.assignment.Node.UID == uid && (!c.backendDone || !c.transportDone || !c.responseRecorded) {
			return false, nil
		}
	}
	for _, i := range r.instances {
		if i.State.Node.UID == uid {
			i.State.Ready = false
			i.State.Reason = "NodeDrained"
			clear(i.bindings)
		}
	}
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return true, nil
}

func (r *Runtime) Publish(d *Definition) error { return r.publish(d, false, "") }
func (r *Runtime) PublishClosed(d *Definition, reason string) error {
	return r.publish(d, true, reason)
}

func (r *Runtime) publish(d *Definition, stopped bool, reason string) (err error) {
	r.mu.Lock()
	err = r.validatePublicationLocked(d)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err := r.config.ObservationLog.DefinitionRecord(d); err != nil {
		return err
	}
	identity := d.Identity()
	r.mu.Lock()
	defer func() { err = errors.Join(err, r.unlock()) }()
	if err := r.validatePublicationLocked(d); err != nil {
		return err
	}
	w := r.workloads[d.UID]
	if w == nil {
		w = &runtimeWorkload{calls: map[string]*Call{}}
		r.workloads[d.UID] = w
	}
	w.definition = d
	w.identity = identity
	w.stopped = stopped
	w.detached = false
	w.reason = reason
	r.names[d.Name] = d.UID
	r.definitions[w.identity] = d
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return nil
}

func (r *Runtime) validatePublicationLocked(d *Definition) error {
	if r.shuttingDown {
		return failure(reasonShuttingDown, "entry point is shutting down")
	}
	if other := r.names[d.Name]; other != "" && other != d.UID {
		return failure("RegistrationConflict", "old registration is still attached")
	}
	if w := r.workloads[d.UID]; w != nil && !w.definition.SameExecution(d) && !r.drainedLocked(w) {
		return failure("DrainIncomplete", "old model or preprocessing requests remain")
	}
	return nil
}

func (r *Runtime) OpenAdmission(uid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shuttingDown {
		return failure(reasonShuttingDown, "entry point is shutting down")
	}
	w := r.workloads[uid]
	if w == nil {
		return failure("WorkloadNotFound", "registration is not applied")
	}
	if r.observationError != nil {
		return failure("ObservationUnavailable", "request observation log is unavailable")
	}
	w.stopped = false
	w.reason = ""
	r.signal()
	return nil
}

func MatchesProfile(d *Definition, p *Profile) bool { return profilePath(d, p) != nil }

func (r *Runtime) StopAdmission(uid, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.workloads[uid]
	if w == nil {
		return failure("WorkloadNotFound", "registration is not applied")
	}
	w.stopped = true
	w.reason = reason
	r.signal()
	return nil
}
func (r *Runtime) drainedLocked(w *runtimeWorkload) bool {
	for _, c := range w.calls {
		if !c.backendDone || !c.transportDone || !c.responseRecorded {
			return false
		}
	}
	return len(w.queue) == 0
}
func (r *Runtime) Drained(uid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.workloads[uid]
	return w == nil || r.drainedLocked(w)
}
func (r *Runtime) Detach(uid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.workloads[uid]
	if w == nil {
		return nil
	}
	if !w.stopped || !r.drainedLocked(w) {
		return failure("DrainIncomplete", "stop admission and finish accepted requests before detaching")
	}
	for _, i := range r.instances {
		delete(i.bindings, uid)
	}
	w.route = inputRoute{}
	w.detached = true
	r.signal()
	return nil
}
func (r *Runtime) Forget(uid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.workloads[uid]
	if w == nil {
		return nil
	}
	if !w.stopped || !r.drainedLocked(w) {
		return failure("DrainIncomplete", "registration still owns requests")
	}
	for _, i := range r.instances {
		if _, attached := i.bindings[uid]; attached {
			return failure("StillAttached", "registration still has instance bindings")
		}
	}
	if r.names[w.definition.Name] == uid {
		delete(r.names, w.definition.Name)
	}
	delete(r.workloads, uid)
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return nil
}

func (r *Runtime) SetProfiles(profiles []Profile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles = profiles
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
}

// SetMeasurementNodes publishes the Nodes that carry the measurement POOL taint.
func (r *Runtime) SetMeasurementNodes(nodes []NodeReference) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.measurement = nodes
	r.signal()
}

// SetCompletionMargin applies the configured completion margin to later dispatches.
func (r *Runtime) SetCompletionMargin(ms float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completionMargin = ms
}

func (r *Runtime) InstallInstances(instances []ReadyInstance) (err error) {
	r.mu.Lock()
	defer func() { err = errors.Join(err, r.unlock()) }()
	seen := map[string]bool{}
	for _, candidate := range instances {
		if candidate.State.ID == "" || candidate.State.Node.UID == "" {
			return failure("InvalidDistribution", "actual instance and Node identities are required")
		}
		if candidate.State.Ready && (candidate.Profile == nil || candidate.Capacity <= 0 || candidate.Execute == nil) {
			return failure("InvalidDistribution", "ready instance needs verified profile, bounds and execution")
		}
		if seen[candidate.State.ID] {
			return failure("InvalidDistribution", "instance appears twice")
		}
		seen[candidate.State.ID] = true
	}
	for _, i := range r.instances {
		i.State.Ready = false
		i.State.Reason = "NoCurrentReadiness"
	}
	for _, candidate := range instances {
		i := r.instances[candidate.State.ID]
		if i == nil {
			i = &runtimeInstance{bindings: map[string]bool{}}
			r.instances[candidate.State.ID] = i
		}
		if i.outstanding > 0 && !candidate.State.Ready {
			i.State.Ready = false
			i.State.Reason = candidate.State.Reason
			continue
		}
		if i.outstanding > 0 && (i.State.Profile != candidate.Profile.ID || i.State.Node != candidate.State.Node) {
			i.State.Reason = "ConditionChangedBeforeDrain"
			continue
		}
		candidate.State.Stopped = i.State.Stopped
		if r.routingControlled && i.Routing != nil {
			candidate.Routing = r.mergeControlledRoutingLocked(i.Routing, candidate.Routing)
		}
		i.ReadyInstance = candidate
		for _, c := range r.calls {
			if c.assignment != nil && c.assignment.Instance == candidate.State.ID && c.instance == nil && !c.backendDone {
				c.instance = i
				i.outstanding++
			}
		}
		r.finishAbsentLocked(candidate.State)
	}
	for id, i := range r.instances {
		if !seen[id] && i.outstanding == 0 {
			delete(r.instances, id)
		}
	}
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return nil
}

// finishAbsentLocked finishes the calls whose native exchange ended without
// any native record (no progress, so the native never queued them) when a
// native state read after that exchange does not hold them: nothing will
// execute or report them. A held call waits for its native completion.
func (r *Runtime) finishAbsentLocked(state InstanceState) {
	if r.mu.recorder.Active() {
		r.mu.mark(DiagnosticAbsent, DiagnosticBegin)
		defer r.mu.mark(DiagnosticAbsent, DiagnosticEnd)
	}
	if state.Native == nil {
		return
	}
	held := map[string]bool{}
	for _, request := range state.Native.Requests {
		held[request.Context.RequestID] = true
	}
	for _, c := range r.calls {
		if c.assignment == nil || c.assignment.Instance != state.ID || c.backendDone || !c.transportDone || c.progress != nil ||
			held[c.receipt.ID] || !state.ObservedAt.After(c.exchangeFinishedAt) {
			continue
		}
		c.backendDone = true
		c.phase = phaseFinished
		if err := r.event(c, "backend_finished", "error", "NativeRequestAbsent"); err != nil {
			r.observationError = err
		}
		r.releaseLocked(c)
	}
}

func profilePath(d *Definition, p *Profile) *ModelPath {
	if p == nil {
		return nil
	}
	c := p.Condition
	pre := d.Preprocessor
	if c.LogicalModel != d.Model.Name || c.Preprocessor.Name != pre.Name || !slices.Equal(c.Preprocessor.Inputs, rawFieldNames(pre.Input)) || !slices.Equal(c.Preprocessor.Outputs, rawFieldNames(pre.Output)) || c.Preprocessor.MaxNewTokens != int(pre.MaxNewTokens) ||
		d.Dataset == nil || c.ActiveProtocol.Dataset == nil || d.Dataset.UID != c.ActiveProtocol.Dataset.UID {
		return nil
	}
	for n := range d.Model.Paths {
		path := &d.Model.Paths[n]
		if path.Name == c.Artifact.ExecutionPath && path.Artifact == c.Artifact.Name && slices.Equal(path.Files, c.Artifact.Files) && path.Image == c.Artifact.Image && path.RuntimeFamily == c.RuntimeFamily && path.Backend == c.Backend && path.Preprocessor == (PreprocessorArtifact{Image: c.Preprocessor.Image, Architecture: c.Preprocessor.Architecture, Path: c.Preprocessor.Path}) {
			if (path.Firstfloor == nil) != (c.Firstfloor == nil) {
				continue
			}
			if path.Firstfloor != nil && *path.Firstfloor != *c.Firstfloor {
				continue
			}
			return path
		}
	}
	return nil
}

func (r *Runtime) rebuildLocked() {
	r.pruneInstanceBindingsLocked()
	r.rebuildWorkloadRoutesLocked()
}

func (r *Runtime) pruneInstanceBindingsLocked() {
	if r.mu.recorder.Active() {
		r.mu.mark(DiagnosticPrune, DiagnosticBegin)
		defer r.mu.mark(DiagnosticPrune, DiagnosticEnd)
	}
	for _, i := range r.instances {
		for uid := range i.bindings {
			w := r.workloads[uid]
			live := false
			if w != nil {
				for _, c := range w.calls {
					if c.assignment != nil && c.assignment.Instance == i.State.ID && !c.backendDone {
						live = true
						break
					}
				}
			}
			if !live && (!i.State.Ready || w == nil || w.detached || profilePath(w.definition, i.Profile) == nil || (r.routingControlled && r.routeWeightLocked(uid, i) == 0)) {
				delete(i.bindings, uid)
			}
		}
	}
}

func (r *Runtime) rebuildWorkloadRoutesLocked() {
	if r.mu.recorder.Active() {
		r.mu.mark(DiagnosticRebuild, DiagnosticBegin)
		defer r.mu.mark(DiagnosticRebuild, DiagnosticEnd)
	}
	for uid, w := range r.workloads {
		previous := w.route
		w.route = inputRoute{}
		if w.detached {
			continue
		}
		route := &w.route
		for p := range r.profiles {
			if profilePath(w.definition, &r.profiles[p]) != nil {
				route.evidence = true
				break
			}
		}
		for _, i := range r.instances {
			actual := profilePath(w.definition, i.Profile)
			weight := r.routeWeightLocked(uid, i)
			if !i.State.Ready || i.State.Stopped || weight == 0 || actual == nil || (i.OwnerWorkloadUID != "" && i.OwnerWorkloadUID != uid) {
				continue
			}
			i.bindings[uid] = true
			route.entries = append(route.entries, routeEntry{instance: i, path: actual})
		}
		sort.Slice(route.entries, func(a, b int) bool { return route.entries[a].instance.State.ID < route.entries[b].instance.State.ID })
		route.weights = make([]int32, len(route.entries))
		route.current = make([]int64, len(route.entries))
		route.available = make([]bool, len(route.entries))
		for index, entry := range route.entries {
			route.weights[index] = r.routeWeightLocked(uid, entry.instance)
			for oldIndex, old := range previous.entries {
				if old.instance.State.ID == entry.instance.State.ID && previous.weights[oldIndex] == route.weights[index] {
					route.current[index] = previous.current[oldIndex]
					break
				}
			}
		}
	}
}

func (r *Runtime) State(name string) (WorkloadState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.workloads[r.names[name]]
	if w == nil {
		return WorkloadState{}, failure("WorkloadNotFound", "workload %q is not applied", name)
	}
	state := WorkloadState{Applied: w.definition, Reason: w.reason, Pending: len(w.queue), ObservedAt: time.Now().UTC()}
	for _, c := range w.calls {
		if c.assignment != nil && !c.backendDone {
			state.Assigned++
		}
	}
	evidence, ready := w.route.evidence, len(w.route.entries) > 0
	state.Admission = !w.stopped && w.definition.Dataset != nil && evidence && r.observationError == nil
	if !w.stopped {
		switch {
		case r.observationError != nil:
			state.Reason = "ObservationUnavailable"
		case w.definition.Dataset == nil:
			state.Reason = "MissingDataset"
		case !evidence:
			state.Reason = "MissingEvidence"
		case !ready:
			state.Reason = "Preparing"
		default:
			state.Reason = "Ready"
		}
	}
	return state, nil
}

// Instances lists every actual instance with its bindings and outstanding work.
func (r *Runtime) Instances() []InstanceState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instancesLocked()
}

func (r *Runtime) instancesLocked() []InstanceState {
	result := make([]InstanceState, 0, len(r.instances))
	for _, i := range r.instances {
		state := i.State
		state.OwnerWorkloadUID = i.OwnerWorkloadUID
		state.Workloads = nil
		state.Waiting = map[string]int{}
		state.Running = map[string]int{}
		state.AssignedUnknown = map[string]int{}
		for uid := range i.bindings {
			if w := r.workloads[uid]; w != nil {
				state.Workloads = append(state.Workloads, WorkloadReference{Name: w.definition.Name, UID: uid})
			}
		}
		for _, c := range r.calls {
			if c.assignment == nil || c.assignment.Instance != state.ID || c.backendDone {
				continue
			}
			switch c.phase {
			case phaseQueued, phaseDeferred:
				state.Waiting[c.receipt.UID]++
			case phaseRunning:
				state.Running[c.receipt.UID]++
			default:
				state.AssignedUnknown[c.receipt.UID]++
			}
		}
		sort.Slice(state.Workloads, func(a, b int) bool { return state.Workloads[a].UID < state.Workloads[b].UID })
		result = append(result, state)
	}
	sort.Slice(result, func(a, b int) bool { return result[a].ID < result[b].ID })
	return result
}

// Pools derives each Workload's active POOL from the instances it owns or is
// bound to, the measurement POOL from the taint, and the remaining Nodes of the
// entire POOL from the latest scheduler snapshot.
func (r *Runtime) Pools(nodes []SchedulerNode) PoolsState {
	r.mu.Lock()
	instances := r.instancesLocked()
	measurement := slices.Clone(r.measurement)
	names := map[string]string{}
	for uid, w := range r.workloads {
		names[uid] = w.definition.Name
	}
	r.mu.Unlock()
	return BuildPools(instances, measurement, nodes, names)
}

// BuildPools is the pure POOL derivation behind Runtime.Pools.
func BuildPools(instances []InstanceState, measurement []NodeReference, nodes []SchedulerNode, names map[string]string) PoolsState {
	result := PoolsState{Workloads: []WorkloadPool{}, Measurement: []MeasurementPool{}, Available: []PoolNode{}, Other: []PoolNode{}, ObservedAt: time.Now().UTC()}
	pools := map[string]*WorkloadPool{}
	measured := map[NodeReference]*MeasurementPool{}
	for _, node := range measurement {
		measured[node] = &MeasurementPool{Node: node, Instances: []InstanceState{}}
	}
	served := map[NodeReference]bool{}
	active := map[NodeReference]bool{}
	for _, instance := range instances {
		served[instance.Node] = true
		if pool := measured[instance.Node]; pool != nil {
			pool.Instances = append(pool.Instances, instance)
		}
		uids := []string{}
		if instance.OwnerWorkloadUID != "" {
			uids = append(uids, instance.OwnerWorkloadUID)
		}
		for _, w := range instance.Workloads {
			uids = appendUnique(uids, w.UID)
		}
		for _, uid := range uids {
			pool := pools[uid]
			if pool == nil {
				pool = &WorkloadPool{Workload: WorkloadReference{Name: names[uid], UID: uid}, Nodes: []NodeReference{}, Instances: []InstanceState{}}
				pools[uid] = pool
			}
			if !slices.Contains(pool.Nodes, instance.Node) {
				pool.Nodes = append(pool.Nodes, instance.Node)
			}
			pool.Instances = append(pool.Instances, instance)
			active[instance.Node] = true
		}
	}
	for _, pool := range pools {
		sort.Slice(pool.Nodes, func(a, b int) bool { return pool.Nodes[a].Name < pool.Nodes[b].Name })
		result.Workloads = append(result.Workloads, *pool)
	}
	sort.Slice(result.Workloads, func(a, b int) bool { return result.Workloads[a].Workload.UID < result.Workloads[b].Workload.UID })
	for _, node := range measurement {
		result.Measurement = append(result.Measurement, *measured[node])
	}
	for _, n := range nodes {
		if measured[n.Node] != nil {
			continue
		}
		node := PoolNode{Node: n.Node, Ready: n.Ready, PowerMode: n.PowerMode, Reasons: n.Reasons}
		switch {
		case n.Ready && !served[n.Node]:
			result.Available = append(result.Available, node)
		case !active[n.Node]:
			result.Other = append(result.Other, node)
		}
	}
	return result
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.shuttingDown = true
	for _, w := range r.workloads {
		w.stopped = true
		w.reason = reasonShuttingDown
	}
	r.mu.Unlock()
	for {
		r.mu.Lock()
		done := r.finalizing == 0
		for _, w := range r.workloads {
			done = done && r.drainedLocked(w)
		}
		r.mu.Unlock()
		if done {
			r.cancel()
			return nil
		}
		select {
		case <-r.changed:
		case <-ctx.Done():
			r.cancel()
			return ctx.Err()
		}
	}
}

// Diagnostics exposes the optional control-only capture to the HTTP boundary.
func (r *Runtime) Diagnostics() *Diagnostics { return r.config.Diagnostics }

// mergeControlledRoutingLocked keeps the Runtime's declaration for controlled
// UIDs while Pod patches lag and follows the current Pod seed for the rest,
// including removed seed entries.
func (r *Runtime) mergeControlledRoutingLocked(current, seed map[string]int32) map[string]int32 {
	for uid := range current {
		if _, controlled := r.routeTargets[uid]; !controlled {
			if _, present := seed[uid]; !present {
				delete(current, uid)
			}
		}
	}
	for uid, weight := range seed {
		if _, controlled := r.routeTargets[uid]; !controlled {
			current[uid] = weight
		}
	}
	return current
}
