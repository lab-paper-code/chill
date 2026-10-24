package serving

import (
	"encoding/binary"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DiagnosticWindow = 420 * time.Second
	DiagnosticBufferBytes = 16 << 20
	diagnosticEventBytes = 64
)

// DiagnosticKind identifies bounded source spans, never request payloads.
type DiagnosticKind uint16
const (
	DiagnosticMutex DiagnosticKind = iota + 1
	DiagnosticBackendReturn
	DiagnosticReply
	DiagnosticWait
	DiagnosticResponse
	DiagnosticWrite
	DiagnosticFlush
	DiagnosticMarshal
	DiagnosticFileWrite
	DiagnosticFileOpen
	DiagnosticFileStat
	DiagnosticFileSync
	DiagnosticFileClose
	DiagnosticRemove
	DiagnosticArchive
	DiagnosticQuery
	DiagnosticDispatch
	DiagnosticRebuild
	DiagnosticRelease
	DiagnosticClientMarshal
	DiagnosticClientExchange
	DiagnosticClientDecode
	DiagnosticChoose
	DiagnosticAbsent
	DiagnosticPrune
	DiagnosticWriteHeader
	DiagnosticResponseEncode
	DiagnosticFileTruncate
	DiagnosticCompress
	DiagnosticUpload
	DiagnosticCapture
	DiagnosticTraceAnchor
)

type DiagnosticPhase uint8
const (
	DiagnosticAttempt DiagnosticPhase = iota + 1
	DiagnosticAcquired
	DiagnosticReleased
	DiagnosticBegin
	DiagnosticEnd
	DiagnosticReceived
	DiagnosticCanceled
)

type diagnosticEvent struct {
	offset int64
	owner uint64
	caller uint64
	value int64
	request [16]byte
	kind DiagnosticKind
	lock uint8
	phase DiagnosticPhase
	_ [12]byte
}

// Diagnostics is a one-shot control-process recorder. A nil recorder leaves only
// a branch at existing boundaries. Its buffers and clocks are never created or
// sampled by an inference request outside the explicitly activated window.
type Diagnostics struct {
	active atomic.Bool
	started atomic.Bool
	sequence atomic.Uint64
	dropped atomic.Uint64
	mu sync.Mutex
	origin time.Time
	ended time.Time
	events []diagnosticEvent
	written uint64
}

func NewDiagnostics() *Diagnostics {
	return &Diagnostics{events: make([]diagnosticEvent, DiagnosticBufferBytes/diagnosticEventBytes)}
}

func (d *Diagnostics) Active() bool { return d != nil && d.active.Load() }

// Start activates at most once. The command owner schedules Stop at 420 seconds;
// the emitter also rejects any timestamp beyond that fixed window.
func (d *Diagnostics) Start() bool {
	if d == nil { return false }
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started.Load() || !d.ended.IsZero() { return false }
	d.origin = time.Now()
	d.started.Store(true)
	d.active.Store(true)
	return true
}

func (d *Diagnostics) Stop() {
	if d == nil { return }
	d.mu.Lock()
	d.active.Store(false)
	if d.ended.IsZero() { d.ended = time.Now() }
	d.mu.Unlock()
}

func diagnosticRequest(id string) (out [16]byte) {
	if len(id) != 32 { return out }
	for i := range out {
		var value byte
		for j := range 2 {
			c := id[2*i+j]
			switch {
			case c >= '0' && c <= '9': c -= '0'
			case c >= 'a' && c <= 'f': c = c-'a'+10
			default: return [16]byte{}
			}
			value = value*16+c
		}
		out[i] = value
	}
	return out
}

func (d *Diagnostics) record(kind DiagnosticKind, phase DiagnosticPhase, lock uint8, owner, caller uint64, request [16]byte, value int64) {
	if !d.Active() { return }
	now := time.Since(d.origin)
	if now >= DiagnosticWindow {
		d.active.Store(false)
		return
	}
	// Instrumentation never queues a measured holder behind another producer.
	if !d.mu.TryLock() {
		d.dropped.Add(1)
		return
	}
	if d.active.Load() {
		d.events[d.written%uint64(len(d.events))] = diagnosticEvent{
			offset: int64(now), owner: owner, caller: caller, request: request,
			kind: kind, phase: phase, lock: lock, value: value,
		}
		d.written++
	}
	d.mu.Unlock()
}

func (d *Diagnostics) Mark(kind DiagnosticKind, phase DiagnosticPhase, requestID string, value int64) {
	if !d.Active() { return }
	pc, _, _, _ := runtime.Caller(1)
	d.record(kind, phase, 0, 0, uint64(pc), diagnosticRequest(requestID), value)
}

// diagnosticMutex retains the original lock scope. Owner and request fields are
// accessed only while that same mutex is held. Caller PCs distinguish every
// holder, including query, management and scheduler paths without request IDs.
type diagnosticMutex struct {
	mutex sync.Mutex
	recorder *Diagnostics
	id uint8
	owner uint64
	caller uint64
	request [16]byte
}

func (m *diagnosticMutex) Lock() { m.lock("") }
func (m *diagnosticMutex) LockRequest(id string) { m.lock(id) }
func (m *diagnosticMutex) lock(id string) {
	d := m.recorder
	if !d.Active() {
		m.mutex.Lock()
		return
	}
	pc, _, _, _ := runtime.Caller(2)
	owner, request := d.sequence.Add(1), diagnosticRequest(id)
	d.record(DiagnosticMutex, DiagnosticAttempt, m.id, owner, uint64(pc), request, 0)
	m.mutex.Lock()
	m.owner, m.caller, m.request = owner, uint64(pc), request
	d.record(DiagnosticMutex, DiagnosticAcquired, m.id, owner, uint64(pc), request, 0)
}
func (m *diagnosticMutex) Unlock() {
	if m.owner != 0 {
		m.recorder.record(DiagnosticMutex, DiagnosticReleased, m.id, m.owner, m.caller, m.request, 0)
		m.owner = 0
	}
	m.mutex.Unlock()
}
func (m *diagnosticMutex) mark(kind DiagnosticKind, phase DiagnosticPhase) {
	if !m.recorder.Active() { return }
	m.recorder.record(kind, phase, m.id, m.owner, m.caller, m.request, 0)
}

type DiagnosticCaller struct {
	Function string `json:"function"`
	File string `json:"file"`
	Line int `json:"line"`
}
type DiagnosticMetadata struct {
	Activated bool `json:"activated"`
	Start time.Time `json:"start"`
	End time.Time `json:"end"`
	WindowNanos int64 `json:"windowNanos"`
	CapacityBytes int `json:"capacityBytes"`
	EventBytes int `json:"eventBytes"`
	Written uint64 `json:"written"`
	Retained uint64 `json:"retained"`
	Overwritten uint64 `json:"overwritten"`
	DroppedContention uint64 `json:"droppedContention"`
	FirstOffsetNanos int64 `json:"firstOffsetNanos"`
	LastOffsetNanos int64 `json:"lastOffsetNanos"`
	Callers map[uint64]DiagnosticCaller `json:"callers"`
	Kinds []string `json:"kinds"`
	Phases []string `json:"phases"`
	Locks map[uint8]string `json:"locks"`
}

// WriteEvents exports once after Stop and releases the ring. The little-endian
// 64-byte records contain offset, owner, caller PC, value, 16-byte request ID,
// kind, lock, phase and twelve zero bytes. Missing edges remain inconclusive.
func (d *Diagnostics) WriteEvents(w io.Writer) (DiagnosticMetadata, error) {
	d.Stop()
	d.mu.Lock()
	defer func() { d.events = nil; d.mu.Unlock() }()
	retained := min(d.written, uint64(len(d.events)))
	meta := DiagnosticMetadata{Activated: d.started.Load(), Start: d.origin.UTC(), End: d.ended.UTC(),
		WindowNanos: int64(DiagnosticWindow), CapacityBytes: DiagnosticBufferBytes, EventBytes: diagnosticEventBytes,
		Written: d.written, Retained: retained, Overwritten: d.written-retained,
		DroppedContention: d.dropped.Load(), Callers: map[uint64]DiagnosticCaller{},
		Kinds: []string{"", "mutex", "backend_return", "reply", "wait", "response", "write", "flush",
			"marshal", "file_write", "file_open", "file_stat", "file_sync", "file_close", "remove",
			"archive", "query", "dispatch", "rebuild", "release", "client_marshal", "client_exchange",
			"client_decode", "choose", "absent_completion", "prune_bindings", "write_header", "response_encode_envelope",
			"file_truncate", "compress", "upload", "capture", "trace_anchor"},
		Phases: []string{"", "attempt", "acquired", "released", "begin", "end", "received", "canceled"},
		Locks: map[uint8]string{1: "Runtime.mu", 2: "ObservationLog.mu"},
	}
	var raw [diagnosticEventBytes]byte
	for i := d.written-retained; i < d.written; i++ {
		e := d.events[i%uint64(len(d.events))]
		if i == d.written-retained { meta.FirstOffsetNanos = e.offset }
		meta.FirstOffsetNanos = min(meta.FirstOffsetNanos, e.offset)
		meta.LastOffsetNanos = max(meta.LastOffsetNanos, e.offset)
		binary.LittleEndian.PutUint64(raw[0:8], uint64(e.offset))
		binary.LittleEndian.PutUint64(raw[8:16], e.owner)
		binary.LittleEndian.PutUint64(raw[16:24], e.caller)
		binary.LittleEndian.PutUint64(raw[24:32], uint64(e.value))
		copy(raw[32:48], e.request[:])
		binary.LittleEndian.PutUint16(raw[48:50], uint16(e.kind))
		raw[50], raw[51] = e.lock, byte(e.phase)
		if _, ok := meta.Callers[e.caller]; !ok && e.caller != 0 {
			if f := runtime.FuncForPC(uintptr(e.caller)); f != nil {
				file, line := f.FileLine(uintptr(e.caller))
				meta.Callers[e.caller] = DiagnosticCaller{Function: f.Name(), File: file, Line: line}
			}
		}
		n, err := w.Write(raw[:])
		if err != nil { return meta, err }
		if n != len(raw) { return meta, io.ErrShortWrite }
	}
	return meta, nil
}
