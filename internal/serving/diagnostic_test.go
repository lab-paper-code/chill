package serving

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
	"unsafe"
)

const diagnosticTestRequest = "0123456789abcdef0123456789abcdef"

func TestDiagnosticInactiveBoundariesDoNotAllocateOrEmit(t *testing.T) {
	for _, recorder := range []*Diagnostics{nil, NewDiagnostics()} {
		mutex := diagnosticMutex{recorder: recorder, id: 1}
		if n := testing.AllocsPerRun(1000, func() {
			mutex.LockRequest(diagnosticTestRequest)
			mutex.mark(DiagnosticDispatch, DiagnosticBegin)
			mutex.Unlock()
			recorder.Mark(DiagnosticWait, DiagnosticBegin, diagnosticTestRequest, 0)
		}); n != 0 {
			t.Fatalf("inactive diagnostic allocation: %g", n)
		}
		if recorder != nil && (recorder.written != 0 || recorder.sequence.Load() != 0) {
			t.Fatal("inactive boundaries emitted events or generated owners")
		}
	}
}

func TestDiagnosticMutexCorrelatesEveryOwnerAndRequest(t *testing.T) {
	d := NewDiagnostics()
	if unsafe.Sizeof(diagnosticEvent{}) != diagnosticEventBytes || len(d.events)*diagnosticEventBytes != DiagnosticBufferBytes {
		t.Fatal("numeric ring exceeds its fixed byte budget")
	}
	if !d.Start() || d.Start() { t.Fatal("capture activation is not one-shot") }
	runtimeLock := diagnosticMutex{recorder: d, id: 1}
	observationLock := diagnosticMutex{recorder: d, id: 2}
	runtimeLock.LockRequest(diagnosticTestRequest)
	observationLock.LockRequest(diagnosticTestRequest)
	observationLock.mark(DiagnosticMarshal, DiagnosticBegin)
	observationLock.mark(DiagnosticMarshal, DiagnosticEnd)
	observationLock.Unlock()
	runtimeLock.Unlock()
	var output bytes.Buffer
	meta, err := d.WriteEvents(&output)
	if err != nil || meta.Retained != 8 || output.Len() != 8*diagnosticEventBytes || len(d.events) != 0 {
		t.Fatalf("one-shot numeric export: %+v bytes=%d error=%v", meta, output.Len(), err)
	}
	owners := map[uint64]uint8{}
	request := diagnosticRequest(diagnosticTestRequest)
	for event := range int(meta.Retained) {
		raw := output.Bytes()[event*diagnosticEventBytes:(event+1)*diagnosticEventBytes]
		if !bytes.Equal(raw[32:48], request[:]) || binary.LittleEndian.Uint64(raw[8:16]) == 0 {
			t.Fatal("request or owner correlation was lost")
		}
		if DiagnosticKind(binary.LittleEndian.Uint16(raw[48:50])) == DiagnosticMutex {
			owner := binary.LittleEndian.Uint64(raw[8:16])
			owners[owner] |= 1 << DiagnosticPhase(raw[51])
		}
	}
	if len(owners) != 2 || len(meta.Callers) < 2 || meta.Locks[1] != "Runtime.mu" || meta.Locks[2] != "ObservationLog.mu" {
		t.Fatalf("holder inventory missing: %+v %v", meta, owners)
	}
	for _, phases := range owners {
		if phases != 1<<DiagnosticAttempt|1<<DiagnosticAcquired|1<<DiagnosticReleased {
			t.Fatalf("owner lacks attempt/acquire/release: %b", phases)
		}
	}
	if d.Start() || d.Active() { t.Fatal("export rearmed the capture") }
}

func TestDiagnosticLossAndDeadlineRemainExplicit(t *testing.T) {
	d := NewDiagnostics()
	d.events = d.events[:2]
	d.Start()
	for range 3 { d.Mark(DiagnosticReply, DiagnosticBegin, diagnosticTestRequest, 0) }
	d.mu.Lock()
	d.Mark(DiagnosticReply, DiagnosticEnd, diagnosticTestRequest, 0)
	d.mu.Unlock()
	meta, err := d.WriteEvents(io.Discard)
	if err != nil || meta.Written != 3 || meta.Retained != 2 || meta.Overwritten != 1 || meta.DroppedContention != 1 {
		t.Fatalf("loss was hidden: %+v %v", meta, err)
	}
	expired := NewDiagnostics()
	expired.Start()
	expired.origin = expired.origin.Add(-DiagnosticWindow)
	expired.Mark(DiagnosticReply, DiagnosticBegin, diagnosticTestRequest, 0)
	if expired.Active() || expired.written != 0 { t.Fatal("event escaped the fixed 420-second window") }
	expired.Stop()
	closed := NewDiagnostics()
	closed.Stop()
	if closed.Start() { t.Fatal("stopped recorder activated later") }
}

type shortDiagnosticWriter struct{}
func (shortDiagnosticWriter) Write([]byte) (int, error) { return 0, nil }
func TestDiagnosticShortExportReportsFailureAndReleasesBuffer(t *testing.T) {
	d := NewDiagnostics()
	d.Start()
	d.Mark(DiagnosticWait, DiagnosticBegin, diagnosticTestRequest, int64(time.Second))
	_, err := d.WriteEvents(shortDiagnosticWriter{})
	if !errors.Is(err, io.ErrShortWrite) || len(d.events) != 0 || d.Active() {
		t.Fatalf("partial export was not explicit and finite: %v", err)
	}
}
