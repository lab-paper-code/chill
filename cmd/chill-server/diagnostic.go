package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/trace"
	"strconv"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	ctrl "sigs.k8s.io/controller-runtime"
)

const diagnosticTraceDuration = 30 * time.Second
const diagnosticTraceBytes = 32 << 20

func (o options) validateDiagnostics() error {
	if o.diagnosticDirectory == "" {
		if o.diagnosticStartDelay != 0 || o.diagnosticTraceOffset != 0 {
			return errors.New("diagnostic timing options require a diagnostic directory")
		}
		return nil
	}
	if o.materializeProfile || !filepath.IsAbs(o.diagnosticDirectory) ||
		filepath.Clean(o.diagnosticDirectory) != o.diagnosticDirectory ||
		o.diagnosticStartDelay < 0 || o.diagnosticTraceOffset < 0 ||
		o.diagnosticTraceOffset > serving.DiagnosticWindow-diagnosticTraceDuration {
		return errors.New("diagnostics require a fresh absolute private directory, nonnegative delay " +
			"and trace offset in [0,390s], in serving mode")
	}
	return nil
}

// The runtime trace writer never writes to storage or calls trace.Stop. A
// separate owner stops tracing on the first cap notification or finite timer.
type cappedDiagnosticTrace struct {
	mu        sync.Mutex
	data      []byte
	used      int
	attempted int64
	truncated bool
	limit     chan struct{}
}

func (w *cappedDiagnosticTrace) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.attempted += int64(len(data))
	n := copy(w.data[w.used:], data)
	w.used += n
	if n < len(data) && !w.truncated {
		w.truncated = true
		close(w.limit)
	}
	// Accept discarded bytes so the runtime can finish stopping normally.
	return len(data), nil
}

type diagnosticSession struct {
	recorder    *serving.Diagnostics
	traceBuffer *cappedDiagnosticTrace
	files       [3]*os.File
	ready       chan time.Time
	done        chan struct{}
	cancel      context.CancelFunc
	err         error
}

type diagnosticTraceMetadata struct {
	CapacityBytes      int       `json:"capacityBytes"`
	DurationLimitNanos int64     `json:"durationLimitNanos"`
	Start              time.Time `json:"start"`
	StopRequested      time.Time `json:"stopRequested"`
	Stopped            time.Time `json:"stopped"`
	StopReason         string    `json:"stopReason"`
	StartError         string    `json:"startError,omitempty"`
	AttemptedBytes     int64     `json:"attemptedBytes"`
	CapturedBytes      int       `json:"capturedBytes"`
	WrittenBytes       int       `json:"writtenBytes"`
	DroppedBytes       int64     `json:"droppedBytes"`
	Truncated          bool      `json:"truncated"`
}

func newDiagnosticSession(ctx context.Context, o options) (*diagnosticSession, error) {
	if o.diagnosticDirectory == "" {
		return nil, nil
	}
	// Existing paths are rejected, including symlinks and previous captures.
	if err := os.Mkdir(o.diagnosticDirectory, 0700); err != nil {
		return nil, err
	}
	s := &diagnosticSession{ready: make(chan time.Time, 1), done: make(chan struct{})}
	for i, name := range []string{"events.bin", "runtime.trace", "metadata.json"} {
		file, err := os.OpenFile(filepath.Join(o.diagnosticDirectory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			for _, opened := range s.files {
				if opened != nil {
					err = errors.Join(err, opened.Close())
				}
			}
			return nil, err
		}
		s.files[i] = file
	}
	s.recorder = serving.NewDiagnostics()
	s.traceBuffer = &cappedDiagnosticTrace{data: make([]byte, diagnosticTraceBytes), limit: make(chan struct{})}
	captureCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.run(captureCtx, o)
	return s, nil
}

func (s *diagnosticSession) listenerBound() {
	if s != nil {
		s.ready <- time.Now()
	}
}
func (s *diagnosticSession) Close() error {
	if s == nil {
		return nil
	}
	s.cancel()
	<-s.done
	return s.err
}

func (s *diagnosticSession) run(ctx context.Context, o options) {
	defer close(s.done)
	var boundAt time.Time
	reason := "canceled_before_listener"
	traceMeta := diagnosticTraceMetadata{
		CapacityBytes:      diagnosticTraceBytes,
		DurationLimitNanos: int64(diagnosticTraceDuration),
		StopReason:         "not_started",
	}
	select {
	case boundAt = <-s.ready:
	case <-ctx.Done():
		s.persist(o, boundAt, reason, traceMeta)
		return
	}
	delay := time.NewTimer(time.Until(boundAt.Add(o.diagnosticStartDelay)))
	select {
	case <-delay.C:
	case <-ctx.Done():
		delay.Stop()
		s.persist(o, boundAt, "canceled_before_activation", traceMeta)
		return
	}
	s.recorder.Start()
	window := time.NewTimer(serving.DiagnosticWindow)
	defer window.Stop()
	traceOffset := time.NewTimer(o.diagnosticTraceOffset)
	defer traceOffset.Stop()
	var traceEnd *time.Timer
	var traceEndC <-chan time.Time
	var limitC <-chan struct{}
	traceOffsetC := traceOffset.C
	tracing := false
	stopTrace := func(why string) {
		if !tracing {
			return
		}
		traceMeta.StopRequested, traceMeta.StopReason = time.Now().UTC(), why
		trace.Stop()
		traceMeta.Stopped = time.Now().UTC()
		tracing = false
		if traceEnd != nil {
			traceEnd.Stop()
		}
		traceEndC, limitC = nil, nil
	}
loop:
	for {
		select {
		case <-ctx.Done():
			reason = "canceled"
			break loop
		case <-window.C:
			reason = "window_elapsed"
			break loop
		case <-traceOffsetC:
			traceOffsetC = nil
			traceBegan := time.Now()
			traceMeta.Start = traceBegan.UTC()
			if err := trace.Start(s.traceBuffer); err != nil {
				traceMeta.StartError, traceMeta.StopReason = err.Error(), "start_failed"
				continue
			}
			tracing = true
			traceEnd = time.NewTimer(time.Until(traceBegan.Add(diagnosticTraceDuration)))
			traceEndC, limitC = traceEnd.C, s.traceBuffer.limit
			anchor := time.Now().UnixNano()
			s.recorder.Mark(serving.DiagnosticTraceAnchor, serving.DiagnosticBegin, "", anchor)
			trace.Log(ctx, "chill.numeric-anchor", strconv.FormatInt(anchor, 10))
			s.recorder.Mark(serving.DiagnosticTraceAnchor, serving.DiagnosticEnd, "", anchor)
		case <-traceEndC:
			stopTrace("duration_limit")
		case <-limitC:
			stopTrace("byte_limit")
		}
	}
	s.recorder.Stop()
	stopTrace(reason)
	s.persist(o, boundAt, reason, traceMeta)
}

// Persistence begins after both recorders stop, outside Runtime/ObservationLog
// locks. These files are private diagnostic artifacts, not observation rows.
func (s *diagnosticSession) persist(o options, boundAt time.Time, reason string, traceMeta diagnosticTraceMetadata) {
	s.recorder.Stop()
	events := bufio.NewWriterSize(s.files[0], 64<<10)
	numeric, eventErr := s.recorder.WriteEvents(events)
	eventErr = errors.Join(eventErr, events.Flush())
	eventBytes := int64(-1)
	eventInfo, statErr := s.files[0].Stat()
	if statErr == nil {
		eventBytes = eventInfo.Size()
	}
	eventErr = errors.Join(eventErr, statErr, s.files[0].Sync(), s.files[0].Close())
	w := s.traceBuffer
	traceMeta.AttemptedBytes, traceMeta.CapturedBytes = w.attempted, w.used
	traceMeta.DroppedBytes, traceMeta.Truncated = w.attempted-int64(w.used), w.truncated
	n, traceErr := s.files[1].Write(w.data[:w.used])
	if n != w.used && traceErr == nil {
		traceErr = io.ErrShortWrite
	}
	traceMeta.WrittenBytes = n
	w.data = nil
	traceErr = errors.Join(traceErr, s.files[1].Sync(), s.files[1].Close())
	meta := struct {
		ListenerBound    time.Time                  `json:"listenerBound"`
		StartDelayNanos  int64                      `json:"startDelayNanos"`
		TraceOffsetNanos int64                      `json:"traceOffsetNanos"`
		StopReason       string                     `json:"stopReason"`
		Numeric          serving.DiagnosticMetadata `json:"numeric"`
		Trace            diagnosticTraceMetadata    `json:"trace"`
		EventFileBytes   int64                      `json:"eventFileBytes"`
		PersistenceError string                     `json:"persistenceError,omitempty"`
	}{
		ListenerBound:    boundAt.UTC(),
		StartDelayNanos:  int64(o.diagnosticStartDelay),
		TraceOffsetNanos: int64(o.diagnosticTraceOffset),
		StopReason:       reason,
		Numeric:          numeric,
		Trace:            traceMeta,
		EventFileBytes:   eventBytes,
	}
	if err := errors.Join(eventErr, traceErr); err != nil {
		meta.PersistenceError = err.Error()
	}
	data, metaErr := json.Marshal(meta)
	if metaErr == nil {
		n, metaErr = s.files[2].Write(append(data, '\n'))
		if n != len(data)+1 && metaErr == nil {
			metaErr = io.ErrShortWrite
		}
	}
	metaErr = errors.Join(metaErr, s.files[2].Sync(), s.files[2].Close())
	s.err = errors.Join(eventErr, traceErr, metaErr)
	if s.err != nil {
		ctrl.Log.Error(s.err, "persist finite control diagnostics")
	}
	ctrl.Log.Info("finite control diagnostics stopped", "directory", o.diagnosticDirectory, "reason", reason,
		"retainedEvents", numeric.Retained, "overwrittenEvents", numeric.Overwritten,
		"droppedEvents", numeric.DroppedContention, "traceTruncated", traceMeta.Truncated)
}
