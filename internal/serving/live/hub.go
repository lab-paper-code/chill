package live

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"
)

const messageSnapshot = "snapshot"

var (
	ErrClosed        = errors.New("live hub closed")
	ErrFrameTooLarge = errors.New("live frame exceeds 1 MiB")
	ErrSeqExhausted  = errors.New("live sequence exhausted")
)

type frame struct {
	kind string
	seq  uint64
	at   string
	data []byte
}

type Hub struct {
	mu                  sync.Mutex
	state               Snapshot
	head                uint64
	headAt              string
	ring                [RetainedMessages]frame
	baseline            frame
	start, count, bytes int
	subs                map[*Subscription]struct{}
	closed              bool
	done                chan struct{}
}

func New(now time.Time) *Hub {
	return &Hub{
		state: Snapshot{Workloads: []Workload{}, Nodes: []Node{}, Instances: []Instance{},
			Routing: Routing{}, Actions: []Action{}, Reservations: []Reservation{}},
		headAt: timestamp(now), subs: make(map[*Subscription]struct{}), done: make(chan struct{}),
	}
}

func timestamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

func encode(kind string, seq uint64, at string, payload any) (frame, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return frame{}, err
	}
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return frame{}, ErrProtocol
	}
	// All payloads are explicit JSON objects. Merge their fields into the envelope.
	envelope, err := json.Marshal(struct {
		Type string `json:"type"`
		Seq  uint64 `json:"seq"`
		At   string `json:"at"`
	}{kind, seq, at})
	if err != nil {
		return frame{}, err
	}
	data := make([]byte, 0, len(envelope)+len(body))
	data = append(data, envelope[:len(envelope)-1]...)
	if len(body) > 2 {
		data = append(data, ',')
		data = append(data, body[1:len(body)-1]...)
	}
	data = append(data, '}')
	if len(data) > MaxFrameBytes {
		return frame{}, ErrFrameTooLarge
	}
	return frame{kind: kind, seq: seq, at: at, data: data}, nil
}

// Hello is a checkpoint, not a new publication. A consumer must send it before
// processing resume, and must not subscribe until that resume arrives.
func (h *Hub) Hello(now time.Time) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	retainFrom := h.head + 1
	if h.count > 0 {
		retainFrom = h.ring[h.start].seq
	}
	at := timestamp(now)
	f, err := encode("hello", h.head, at, struct {
		Protocol   string `json:"protocol"`
		ServerAt   string `json:"serverAt"`
		RetainFrom uint64 `json:"retainFrom"`
	}{Protocol, at, retainFrom})
	return f.data, err
}

// Publish applies state and queues the delta before its related event under one
// lock. Inputs are copied; producers retain ownership and never wait for sockets.
func (h *Hub) Publish(at time.Time, delta Delta, event *Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.publishStateLocked(at, delta, event)
}

func (h *Hub) publishStateLocked(at time.Time, delta Delta, event *Event) error {
	if h.closed {
		return ErrClosed
	}
	if event != nil &&
		(event.Kind != "decision" || event.Decision == nil || event.Action != nil || event.Cluster != nil) &&
		(event.Kind != "action" || event.Action == nil || event.Decision != nil || event.Cluster != nil) &&
		(event.Kind != "cluster" || event.Cluster == nil || event.Decision != nil || event.Action != nil) {
		return errors.New("live event requires exactly its declared payload")
	}
	changed := h.changedLocked(delta)
	if !changed && (event == nil || event.Kind != "cluster") {
		return nil
	}
	n := uint64(0)
	if changed {
		n++
	}
	if event != nil {
		n++
	}
	if h.head > MaxSeq-n {
		return ErrSeqExhausted
	}
	atText := timestamp(at)
	var d, e frame
	var err error
	if changed {
		d, err = encode("delta", h.head+1, atText, delta)
		if err != nil {
			return err
		}
	}
	if event != nil {
		e, err = encode("event", h.head+n, atText, event)
		if err != nil {
			return err
		}
	}
	if changed {
		h.applyLocked(delta)
		h.appendLocked(d)
	}
	if event != nil {
		h.appendLocked(e)
	}
	return nil
}

func (h *Hub) changedLocked(d Delta) bool {
	// Runtime's readiness timestamp is the capture time, not a state change.
	if d.Workloads != nil && !slices.EqualFunc(h.state.Workloads, *d.Workloads, func(a, b Workload) bool {
		b.Readiness.ObservedAt = a.Readiness.ObservedAt
		return reflect.DeepEqual(a, b)
	}) ||
		d.Nodes != nil && !reflect.DeepEqual(h.state.Nodes, *d.Nodes) ||
		d.Routing != nil && !reflect.DeepEqual(h.state.Routing, *d.Routing) ||
		d.Decision != nil && !reflect.DeepEqual(h.state.Decision, *d.Decision) ||
		d.Reservations != nil && !reflect.DeepEqual(h.state.Reservations, *d.Reservations) {
		return true
	}
	if d.Instances != nil {
		for _, row := range *d.Instances {
			i := slices.IndexFunc(h.state.Instances, func(old Instance) bool { return old.ID == row.ID })
			if i < 0 || !reflect.DeepEqual(h.state.Instances[i], row) {
				return true
			}
		}
	}
	if d.Actions != nil {
		for _, row := range *d.Actions {
			key := row.Key()
			i := slices.IndexFunc(h.state.Actions, func(old Action) bool { return old.Key() == key })
			if i < 0 || !reflect.DeepEqual(h.state.Actions[i], row) {
				return true
			}
		}
	}
	if d.Removed != nil {
		for _, row := range h.state.Instances {
			if slices.Contains(d.Removed.Instances, row.ID) {
				return true
			}
		}
		for _, row := range h.state.Actions {
			if slices.Contains(d.Removed.Actions, row.Key()) {
				return true
			}
		}
	}
	return false
}

func (h *Hub) PublishMetrics(at time.Time, metrics MetricsFrame) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.publishLocked("metrics", at, metrics)
}

func (h *Hub) Heartbeat(at time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.publishLocked("heartbeat", at, struct {
		ServerAt string `json:"serverAt"`
	}{timestamp(at)})
}

func (h *Hub) publishLocked(kind string, at time.Time, payload any) error {
	if h.closed {
		return ErrClosed
	}
	if h.head == MaxSeq {
		return ErrSeqExhausted
	}
	f, err := encode(kind, h.head+1, timestamp(at), payload)
	if err != nil {
		return err
	}
	h.appendLocked(f)
	return nil
}

func (h *Hub) appendLocked(f frame) {
	h.head, h.headAt = f.seq, f.at
	for h.count > 0 && (h.count == RetainedMessages || h.bytes+len(f.data) > RetainedBytes) {
		h.bytes -= len(h.ring[h.start].data)
		h.ring[h.start] = frame{}
		h.start = (h.start + 1) % RetainedMessages
		h.count--
	}
	h.ring[(h.start+h.count)%RetainedMessages] = f
	h.count++
	h.bytes += len(f.data)
	for sub := range h.subs {
		sub.enqueueLocked(f)
	}
}

func (h *Hub) snapshotLocked() (frame, error) {
	if h.baseline.kind == messageSnapshot && h.baseline.seq == h.head {
		return h.baseline, nil
	}
	f, err := encode(messageSnapshot, h.head, h.headAt, h.state)
	if err == nil {
		h.baseline = f
	}
	return f, err
}

// Subscribe atomically chooses a baseline/replay and registers future delivery.
// from=0 always requests a baseline; nonzero checkpoints match both seq and at.
func (h *Hub) Subscribe(from uint64, at string) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	s := &Subscription{hub: h, wake: make(chan struct{}, 1)}
	if err := s.resumeLocked(from, at); err != nil {
		return nil, err
	}
	h.subs[s] = struct{}{}
	return s, nil
}

type Subscription struct {
	hub     *Hub
	wake    chan struct{}
	pending []frame
	bytes   int
	err     error
}

// Resume replaces pending delivery atomically with a new replay or baseline.
func (s *Subscription) Resume(from uint64, at string) error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return s.resumeLocked(from, at)
}

func (s *Subscription) resumeLocked(from uint64, at string) error {
	h := s.hub
	s.pending, s.bytes = nil, 0
	matched := false
	if from > 0 && from <= MaxSeq {
		for i := range h.count {
			f := h.ring[(h.start+i)%RetainedMessages]
			if f.seq == from && f.at == at {
				matched = true
				break
			}
		}
	}
	if matched {
		for i := range h.count {
			f := h.ring[(h.start+i)%RetainedMessages]
			if f.seq > from && s.enqueueLocked(f) {
				break
			}
		}
	} else {
		f, err := h.snapshotLocked()
		if err != nil {
			s.failLocked(err)
			return err
		}
		s.pending, s.bytes = []frame{f}, len(f.data)
		s.signalLocked()
	}
	return s.err
}

func (s *Subscription) enqueueLocked(f frame) bool {
	if s.err != nil {
		return true
	}
	if len(s.pending)+1 <= QueueMessages && s.bytes+len(f.data) <= QueueBytes {
		s.pending = append(s.pending, f)
		s.bytes += len(f.data)
		s.signalLocked()
		return false
	}
	barrier, err := s.hub.snapshotLocked()
	if err != nil {
		s.failLocked(err)
		return true
	}
	// Drop old metrics first, and everything after that first missing seq. The
	// checkpoint barrier precedes all future state/events; clients preserve the
	// skipped checkpoint interval as a gap rather than inventing observations.
	cut := 0
	for i, pending := range s.pending {
		if pending.kind == "metrics" {
			cut = i
			break
		}
	}
	prefixBytes := 0
	for _, pending := range s.pending[:cut] {
		prefixBytes += len(pending.data)
	}
	if cut+1 > QueueMessages || prefixBytes+len(barrier.data) > QueueBytes {
		cut, prefixBytes = 0, 0
	}
	kept := make([]frame, cut, cut+1)
	copy(kept, s.pending[:cut])
	s.pending = append(kept, barrier)
	s.bytes = prefixBytes + len(barrier.data)
	s.signalLocked()
	return true
}

func (s *Subscription) signalLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscription) failLocked(err error) {
	s.err, s.pending, s.bytes = err, nil, 0
	delete(s.hub.subs, s)
	s.signalLocked()
}

// Next transfers an immutable frame to an internal consumer. It must not modify
// the returned bytes, which are also retained by the shared hub ring.
func (s *Subscription) Next(ctx context.Context) ([]byte, error) {
	for {
		s.hub.mu.Lock()
		if s.err != nil {
			err := s.err
			s.hub.mu.Unlock()
			return nil, err
		}
		if len(s.pending) > 0 {
			f := s.pending[0]
			s.pending[0] = frame{}
			s.pending = s.pending[1:]
			s.bytes -= len(f.data)
			s.hub.mu.Unlock()
			return f.data, nil
		}
		s.hub.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.wake:
		}
	}
}

func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.failLocked(ErrClosed)
}

func (h *Hub) Done() <-chan struct{} { return h.done }

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	close(h.done)
	h.closed = true
	for s := range h.subs {
		s.failLocked(ErrClosed)
	}
}

// Run is the only live timer loop; source callbacks do memory updates only.
func (h *Hub) Run(ctx context.Context, metrics *Metrics) error {
	tick := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	defer heartbeat.Stop()
	defer h.Close()
	for {
		select {
		case <-ctx.Done():
			return nil
		case at := <-tick.C:
			if metrics != nil {
				if err := metrics.Flush(at); err != nil {
					return err
				}
			}
		case at := <-heartbeat.C:
			if err := h.Heartbeat(at); err != nil {
				return err
			}
		}
	}
}

func copied[T any](value *T) *T {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}

func cloneAction(value Action) Action {
	value.CompletedAt = copied(value.CompletedAt)
	value.ExpectedSeconds = copied(value.ExpectedSeconds)
	value.ExpectedSource = copied(value.ExpectedSource)
	if value.ExpectedSource != nil {
		value.ExpectedSource.Reference = slices.Clone(value.ExpectedSource.Reference)
	}
	return value
}

func (h *Hub) applyLocked(delta Delta) {
	if delta.Workloads != nil {
		h.state.Workloads = make([]Workload, len(*delta.Workloads))
		for i, row := range *delta.Workloads {
			row.DefinitionID = copied(row.DefinitionID)
			h.state.Workloads[i] = row
		}
	}
	if delta.Nodes != nil {
		h.state.Nodes = make([]Node, len(*delta.Nodes))
		for i, row := range *delta.Nodes {
			row.ObservedAt = copied(row.ObservedAt)
			row.Reasons = slices.Clone(row.Reasons)
			h.state.Nodes[i] = row
		}
	}
	if delta.Reservations != nil {
		h.state.Reservations = make([]Reservation, len(*delta.Reservations))
		for i, row := range *delta.Reservations {
			row.Nodes = slices.Clone(row.Nodes)
			row.WorkloadUIDs = slices.Clone(row.WorkloadUIDs)
			row.PodUIDs = slices.Clone(row.PodUIDs)
			row.Instances = slices.Clone(row.Instances)
			h.state.Reservations[i] = row
		}
	}
	if delta.Routing != nil {
		h.state.Routing = make(Routing, len(*delta.Routing))
		for uid, routes := range *delta.Routing {
			h.state.Routing[uid] = maps.Clone(routes)
		}
	}
	if delta.Decision != nil {
		h.state.Decision = copied(*delta.Decision)
	}
	if delta.Instances != nil {
		for _, row := range *delta.Instances {
			row.Routable, row.Running, row.Waiting = maps.Clone(row.Routable), maps.Clone(row.Running), maps.Clone(row.Waiting)
			index := slices.IndexFunc(h.state.Instances, func(old Instance) bool { return old.ID == row.ID })
			if index < 0 {
				h.state.Instances = append(h.state.Instances, row)
			} else {
				h.state.Instances[index] = row
			}
		}
	}
	if delta.Actions != nil {
		for _, row := range *delta.Actions {
			row = cloneAction(row)
			key := row.Key()
			index := slices.IndexFunc(h.state.Actions, func(old Action) bool { return old.Key() == key })
			if index < 0 {
				h.state.Actions = append(h.state.Actions, row)
			} else {
				h.state.Actions[index] = row
			}
		}
	}
	if delta.Removed != nil {
		h.state.Instances = slices.DeleteFunc(h.state.Instances, func(row Instance) bool { return slices.Contains(delta.Removed.Instances, row.ID) })
		h.state.Actions = slices.DeleteFunc(h.state.Actions, func(row Action) bool { return slices.Contains(delta.Removed.Actions, row.Key()) })
	}
}
