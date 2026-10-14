package serving

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

type RuntimeConfig struct {
	QueueLimit int
	Journal    *Journal
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
	input                                        validatedInput
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
	routes     []inputRoute
	queue      []*Call
	calls      map[string]*Call
}

// ponytail: one owner and one lock for the research cluster. Use per-Workload
// locks only if measured contention, not a distributed queue or lock service.
type Runtime struct {
	mu                sync.Mutex
	config            RuntimeConfig
	ctx               context.Context
	cancel            context.CancelFunc
	workloads         map[string]*runtimeWorkload
	names             map[string]string
	instances         map[string]*runtimeInstance
	profiles          []Profile
	nodes             map[string][]NodeReference
	routingControlled bool
	routeTargets      map[string]map[string]int32
	calls             map[string]*Call
	definitions       map[string]*Definition
	changed           chan struct{}
	observationError  error
	shuttingDown      bool
}

func NewRuntime(ctx context.Context, config RuntimeConfig) (*Runtime, error) {
	if config.QueueLimit <= 0 || config.Journal == nil {
		return nil, errors.New("positive queue limit and journal are required")
	}
	backendCtx, cancel := context.WithCancel(ctx)
	r := &Runtime{config: config, ctx: backendCtx, cancel: cancel, workloads: map[string]*runtimeWorkload{}, names: map[string]string{}, instances: map[string]*runtimeInstance{}, nodes: map[string][]NodeReference{}, calls: map[string]*Call{}, definitions: map[string]*Definition{}, changed: make(chan struct{}, 1)}
	definitions, pending, err := config.Journal.runtimeFacts()
	if err != nil {
		cancel()
		return nil, err
	}
	r.definitions = definitions
	// Existing assignments are observed to completion, never executed again.
	for _, fact := range pending {
		d := definitions[fact.event.Definition]
		if d == nil {
			cancel()
			return nil, errors.New("unfinished assignment has no retained definition")
		}
		w := r.workloads[d.UID]
		if w == nil {
			w = &runtimeWorkload{definition: d, identity: fact.event.Definition, stopped: true, reason: "RestartDrain", calls: map[string]*Call{}}
			r.workloads[d.UID] = w
			r.names[d.Name] = d.UID
		}
		c := &Call{runtime: r, receipt: Receipt{ID: fact.event.RequestID, Name: d.Name, UID: d.UID, ReceivedAt: fact.event.ReceivedAt}, definition: d, identity: fact.event.Definition, assignment: fact.event.Assignment, phase: fact.phase, transportDone: true, responseRecorded: fact.responseRecorded}
		if fact.event.LifetimeNanos > 0 {
			c.receipt.Deadline = c.receipt.ReceivedAt.Add(time.Duration(fact.event.LifetimeNanos))
		}
		if fact.event.InputPath != nil {
			c.input.path = *fact.event.InputPath
		}
		instance := r.instances[c.assignment.Instance]
		if instance == nil {
			instance = &runtimeInstance{ReadyInstance: ReadyInstance{State: InstanceState{ID: c.assignment.Instance, Node: c.assignment.Node, Pool: c.assignment.Pool, Profile: c.assignment.Profile, Reason: "RestartUnconfirmed"}}, bindings: map[string]bool{}}
			r.instances[c.assignment.Instance] = instance
		}
		instance.bindings[d.UID] = true
		instance.outstanding++
		c.instance = instance
		if !c.responseRecorded {
			if err := r.event(c, eventResponseFinished, outcomeIncomplete, "EntrypointRestarted"); err != nil {
				cancel()
				return nil, err
			}
			c.responseRecorded = true
		}
		r.calls[c.receipt.ID] = c
		w.calls[c.receipt.ID] = c
	}
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
	e := RequestEvent{RequestID: c.receipt.ID, WorkloadName: c.receipt.Name, WorkloadUID: c.receipt.UID, Definition: c.identity, Kind: kind, At: now.UTC(), ReceivedAt: c.receipt.ReceivedAt, ElapsedNanos: now.Sub(c.receipt.ReceivedAt).Nanoseconds(), Assignment: c.assignment, Outcome: outcome, Error: code}
	if !c.receipt.Deadline.IsZero() {
		e.LifetimeNanos = c.receipt.Deadline.Sub(c.receipt.ReceivedAt).Nanoseconds()
	}
	e.NativeProgress = c.progress
	e.ClientSpanNanos = int64(c.clientSpan)
	e.ExecutionComplete = c.backendDone
	if c.definition != nil {
		e.SLO = &c.definition.SLO
		e.InputPath = &c.input.path
	}
	if err := r.config.Journal.RequestEvent(e); err != nil {
		r.observationError = err
		return err
	}
	return nil
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
	r.mu.Lock()
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
	validated, err := validateInput(input, d.Model.Paths)
	if err != nil {
		return nil, err
	}
	lifetime := int64(0)
	if !receipt.Deadline.IsZero() {
		lifetime = receipt.Deadline.Sub(receipt.ReceivedAt).Nanoseconds()
	}
	if err := r.config.Journal.RequestEvent(RequestEvent{RequestID: receipt.ID, WorkloadName: receipt.Name, WorkloadUID: receipt.UID, Definition: d.Identity(), Kind: "validated", At: time.Now().UTC(), ReceivedAt: receipt.ReceivedAt, SLO: &d.SLO, InputPath: &validated.path, LifetimeNanos: lifetime}); err != nil {
		return nil, failure("ObservationUnavailable", "cannot persist validated compute demand")
	}
	r.mu.Lock()
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
		return nil, failure("ObservationUnavailable", "request journal is unavailable")
	}
	if w.definition.Model.UID != d.Model.UID || w.definition.Model.Generation != d.Model.Generation {
		return nil, failure("DefinitionChanged", "model changed before acceptance")
	}
	if !w.routes[validated.path].evidence {
		return nil, failure("MissingEvidence", "no matching execution evidence for this input")
	}
	if len(w.queue) >= r.config.QueueLimit {
		return nil, failure("QueueFull", "workload ingress queue limit reached")
	}
	c := &Call{runtime: r, receipt: receipt, definition: w.definition, identity: w.identity, input: validated, reply: make(chan callResult, 1)}
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
	select {
	case result := <-c.reply:
		return result.result, result.err
	case <-ctx.Done():
		c.runtime.mu.Lock()
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

func (r *Runtime) Response(receipt Receipt, outcome, code string) error {
	finishedAt := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls[receipt.ID]
	if c == nil {
		c = &Call{receipt: receipt}
	}
	if c.responseRecorded {
		return nil
	}
	if err := r.eventAt(c, eventResponseFinished, outcome, code, finishedAt); err != nil {
		return err
	}
	c.responseRecorded = true
	r.releaseLocked(c)
	r.dispatchLocked()
	r.signal()
	return nil
}

func (r *Runtime) removeQueuedLocked(c *Call) {
	w := r.workloads[c.receipt.UID]
	if w == nil {
		return
	}
	for i, queued := range w.queue {
		if queued == c {
			copy(w.queue[i:], w.queue[i+1:])
			w.queue[len(w.queue)-1] = nil
			w.queue = w.queue[:len(w.queue)-1]
			return
		}
	}
}

func (r *Runtime) chooseLocked(route inputRoute) *routeEntry {
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
	// The ingress owner promptly distributes accepted samples to ready capacity.
	// The firstfloor actor owns actual compatible batching and additional wait.
	for _, w := range r.workloads {
		for len(w.queue) > 0 {
			c := w.queue[0]
			if !c.receipt.Deadline.IsZero() && !time.Now().Before(c.receipt.Deadline) {
				r.removeQueuedLocked(c)
				c.backendDone = true
				c.transportDone = true
				c.reply <- callResult{err: failure("RequestTimeout", "request lifetime expired in ingress queue")}
				continue
			}
			entry := r.chooseLocked(w.routes[c.input.path])
			if entry == nil {
				break
			}
			i := entry.instance
			c.assignment = &Assignment{Pool: i.State.Pool, Node: i.State.Node, Instance: i.State.ID, Artifact: entry.path.Artifact, ExecutionPath: entry.path.Name, Profile: i.Profile.ID, DispatchedAt: time.Now().UTC()}
			c.assignment.ProfileReference = i.Profile.Reference
			c.assignment.ProfileEvidence = i.Profile.Evidence
			c.assignment.ProfileConfidenceLevel = i.Profile.ConfidenceLevel
			if err := r.event(c, phaseAssigned, "", ""); err != nil {
				c.assignment = nil
				r.removeQueuedLocked(c)
				c.backendDone = true
				c.transportDone = true
				c.reply <- callResult{err: failure("ObservationUnavailable", "cannot persist assignment")}
				continue
			}
			r.removeQueuedLocked(c)
			c.instance = i
			c.phase = phaseAssigned
			i.outstanding++
			c.backendContext, c.cancelBackend = context.WithCancel(r.ctx)
			dispatch := Dispatch{RequestID: c.receipt.ID, Definition: c.definition, Input: c.input.input, Assignment: *c.assignment, ReceivedAt: c.receipt.ReceivedAt, Elapsed: time.Since(c.receipt.ReceivedAt), Deadline: c.receipt.Deadline}
			go r.execute(c, i.Execute, dispatch)
		}
	}
}

func (r *Runtime) execute(c *Call, execute func(context.Context, Dispatch) (BackendResult, error), dispatch Dispatch) {
	result, err := execute(c.backendContext, dispatch)
	finishedAt := time.Now()
	outcome, code := outcomeCompleted, ""
	if err != nil {
		var domain *Error
		if !errors.As(err, &domain) {
			err = failure("BackendUnavailable", "native backend did not complete a valid exchange")
		}
		errors.As(err, &domain)
		outcome, code = "error", domain.Code
		if domain.Code == "SLODeadlineDrop" {
			outcome = "timeout"
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
	if eventErr := r.eventAt(c, "backend_exchange_finished", outcome, code, finishedAt); eventErr != nil {
		err = failure("ObservationUnavailable", "cannot persist backend exchange outcome")
	}
	if c.reply != nil {
		c.reply <- callResult{result: result, err: err}
	}
	r.releaseLocked(c)
	r.dispatchLocked()
	r.signal()
}

func (r *Runtime) releaseLocked(c *Call) {
	if !c.backendDone || !c.transportDone {
		return
	}
	if c.instance != nil {
		c.instance.outstanding--
		c.instance = nil
	}
	c.input.input = nil
	if !c.responseRecorded {
		return
	}
	delete(r.calls, c.receipt.ID)
	if w := r.workloads[c.receipt.UID]; w != nil {
		delete(w.calls, c.receipt.ID)
	}
}

func (r *Runtime) ObserveBackend(instance string, progress BackendProgress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls[progress.RequestID]
	e := RequestEvent{RequestID: progress.RequestID, WorkloadUID: progress.WorkloadUID, Definition: progress.Definition, Kind: "backend_" + progress.Phase, At: progress.At, NativeInstance: instance, Outcome: progress.Outcome, Error: progress.Error}
	e.NativeProgress = &progress
	e.ExecutionComplete = progress.ExecutionComplete
	if d := r.definitions[progress.Definition]; d != nil {
		e.WorkloadName = d.Name
		e.SLO = &d.SLO
	}
	if c != nil {
		if c.assignment == nil || c.assignment.Instance != instance || c.receipt.UID != progress.WorkloadUID || c.identity != progress.Definition {
			return failure("BackendContextMismatch", "native progress does not match its assigned request")
		}
		e.Assignment = c.assignment
		e.ReceivedAt = c.receipt.ReceivedAt
		if !c.receipt.Deadline.IsZero() {
			e.LifetimeNanos = c.receipt.Deadline.Sub(c.receipt.ReceivedAt).Nanoseconds()
		}
		e.InputPath = &c.input.path
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
	}
	if err := r.config.Journal.RequestEvent(e); err != nil {
		r.observationError = err
		return err
	}
	if c != nil {
		r.releaseLocked(c)
	}
	r.dispatchLocked()
	r.signal()
	return nil
}

// ConfirmInstanceStopped requires kubelet-reported container termination, not a
// failed readiness probe, deleted desired resource, or disconnected transport.
func (r *Runtime) ConfirmInstanceStopped(instance string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *Runtime) publish(d *Definition, stopped bool, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shuttingDown {
		return failure(reasonShuttingDown, "entry point is shutting down")
	}
	w := r.workloads[d.UID]
	if other := r.names[d.Name]; other != "" && other != d.UID {
		return failure("RegistrationConflict", "old registration is still attached")
	}
	if w != nil && (w.definition.Model.UID != d.Model.UID || w.definition.Model.Generation != d.Model.Generation) && !r.drainedLocked(w) {
		return failure("DrainIncomplete", "old model requests remain")
	}
	if err := r.config.Journal.DefinitionRecord(d); err != nil {
		return err
	}
	if w == nil {
		w = &runtimeWorkload{calls: map[string]*Call{}}
		r.workloads[d.UID] = w
	}
	w.definition = d
	w.identity = d.Identity()
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
		return failure("ObservationUnavailable", "request journal is unavailable")
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
	w.routes = nil
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
func (r *Runtime) SetNodes(nodes map[string][]NodeReference) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes = nodes
	r.signal()
}

func (r *Runtime) InstallInstances(instances []ReadyInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, candidate := range instances {
		if candidate.State.ID == "" || candidate.State.Node.UID == "" {
			return failure("InvalidDistribution", "actual instance and Node identities are required")
		}
		if candidate.State.Ready && (candidate.Profile == nil || candidate.Weight < 0 || candidate.Capacity <= 0 || candidate.Execute == nil) {
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
		if i.outstanding > 0 && (i.State.Profile != candidate.Profile.ID || i.State.Node != candidate.State.Node || i.State.Pool != candidate.State.Pool) {
			i.State.Reason = "ConditionChangedBeforeDrain"
			continue
		}
		candidate.State.Stopped = i.State.Stopped
		i.ReadyInstance = candidate
		for _, c := range r.calls {
			if c.assignment != nil && c.assignment.Instance == candidate.State.ID && c.instance == nil && !c.backendDone {
				c.instance = i
				i.outstanding++
			}
		}
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

func sameInputs(a, b []modelcompat.InputTensor) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if x.Name != y.Name || x.Datatype != y.Datatype || len(x.SampleShape) != len(y.SampleShape) {
				continue
			}
			equal := true
			for n := range x.SampleShape {
				if x.SampleShape[n] != y.SampleShape[n] {
					equal = false
					break
				}
			}
			if equal {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func profilePath(d *Definition, p *Profile) *ModelPath {
	if p == nil {
		return nil
	}
	c := p.Condition
	if c.LogicalModel != d.Model.Name {
		return nil
	}
	for n := range d.Model.Paths {
		path := &d.Model.Paths[n]
		if path.Name == c.Artifact.ExecutionPath && path.Artifact == c.Artifact.Name && path.Path == c.Artifact.Path && path.Image == c.Artifact.Image && path.RuntimeFamily == c.RuntimeFamily && path.Backend == c.Backend && sameInputs(path.Tensors, c.Tensor.Tensors) {
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
			if !live && (!i.State.Ready || w == nil || w.detached || profilePath(w.definition, i.Profile) == nil) {
				delete(i.bindings, uid)
			}
		}
	}
}

func (r *Runtime) rebuildWorkloadRoutesLocked() {
	for uid, w := range r.workloads {
		if w.detached {
			w.routes = nil
			continue
		}
		previous := w.routes
		w.routes = make([]inputRoute, len(w.definition.Model.Paths))
		for n, path := range w.definition.Model.Paths {
			route := &w.routes[n]
			for p := range r.profiles {
				if profilePath(w.definition, &r.profiles[p]) != nil && sameInputs(path.Tensors, r.profiles[p].Condition.Tensor.Tensors) {
					route.evidence = true
					break
				}
			}
			for _, i := range r.instances {
				actual := profilePath(w.definition, i.Profile)
				weight := r.routeWeightLocked(uid, i)
				if !i.State.Ready || i.State.Stopped || weight == 0 || actual == nil || (i.OwnerWorkloadUID != "" && i.OwnerWorkloadUID != uid) || !sameInputs(path.Tensors, actual.Tensors) {
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
				if n < len(previous) {
					for oldIndex, old := range previous[n].entries {
						if old.instance.State.ID == entry.instance.State.ID && oldIndex < len(previous[n].weights) && previous[n].weights[oldIndex] == route.weights[index] {
							route.current[index] = previous[n].current[oldIndex]
							break
						}
					}
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
	evidence, ready := false, false
	for _, route := range w.routes {
		evidence = evidence || route.evidence
		ready = ready || len(route.entries) > 0
	}
	state.Admission = !w.stopped && evidence && r.observationError == nil
	if !w.stopped {
		switch {
		case r.observationError != nil:
			state.Reason = "ObservationUnavailable"
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

func (r *Runtime) Pools() []PoolState {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	pools := map[string]*PoolState{}
	get := func(id string) *PoolState {
		if pools[id] == nil {
			pools[id] = &PoolState{ID: id, ObservedAt: now}
		}
		return pools[id]
	}
	for id, nodes := range r.nodes {
		get(id).CurrentNodes = append([]NodeReference(nil), nodes...)
	}
	for _, i := range r.instances {
		state := i.State
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
		pool := get(state.Pool)
		pool.Instances = append(pool.Instances, state)
	}
	result := make([]PoolState, 0, len(pools))
	for _, pool := range pools {
		seen := map[string]bool{}
		for _, i := range pool.Instances {
			for _, w := range i.Workloads {
				if !seen[w.UID] {
					pool.ServingWorkloads = append(pool.ServingWorkloads, w)
					seen[w.UID] = true
				}
			}
		}
		sort.Slice(pool.Instances, func(a, b int) bool { return pool.Instances[a].ID < pool.Instances[b].ID })
		sort.Slice(pool.ServingWorkloads, func(a, b int) bool { return pool.ServingWorkloads[a].UID < pool.ServingWorkloads[b].UID })
		result = append(result, *pool)
	}
	sort.Slice(result, func(a, b int) bool { return result[a].ID < result[b].ID })
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
		done := true
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
