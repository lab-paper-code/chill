// Package gateway owns the authenticated control/router channel, not inference.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

const Path = "/internal/gateway"
const HeartbeatInterval = 5 * time.Second
const FreshnessTimeout = 15 * time.Second

type message struct {
	Type       string                   `json:"type"`
	Seq        uint64                   `json:"seq"`
	At         time.Time                `json:"at"`
	Checkpoint uint64                   `json:"checkpoint"`
	RouterID   string                   `json:"routerID,omitempty"`
	Generation uint64                   `json:"generation,omitempty"`
	Plan       *serving.GatewayPlan     `json:"plan,omitempty"`
	Report     *serving.GatewayReport   `json:"report,omitempty"`
	Instance   string                   `json:"instance,omitempty"`
	Progress   *serving.BackendProgress `json:"progress,omitempty"`
	State      *serving.InstanceState   `json:"state,omitempty"`
	Owner      string                   `json:"owner,omitempty"`
	Recovery   []serving.GatewayReport  `json:"recovery,omitempty"`
	receivedAt time.Time
}

type channel struct {
	conn                      *websocket.Conn
	ctx                       context.Context
	cancel                    context.CancelFunc
	mu                        sync.Mutex
	seq, received, checkpoint uint64
	bytes                     int
	queue                     chan []byte
	lastRead                  atomic.Int64
}

func newChannel(ctx context.Context, conn *websocket.Conn) *channel {
	ctx, cancel := context.WithCancel(ctx)
	s := &channel{conn: conn, ctx: ctx, cancel: cancel, queue: make(chan []byte, live.QueueMessages)}
	s.lastRead.Store(time.Now().UnixNano())
	return s
}
func (s *channel) close() { s.cancel(); _ = s.conn.CloseNow() }

// send never waits on a socket or discards physical evidence. Overflow forces a
// fresh baseline; the coordinator retains the unresolved ownership record.
func (s *channel) send(kind string, payload message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	s.seq++
	data, err := live.Encode(kind, s.seq, time.Now(), struct {
		Checkpoint uint64                   `json:"checkpoint"`
		RouterID   string                   `json:"routerID,omitempty"`
		Generation uint64                   `json:"generation,omitempty"`
		Plan       *serving.GatewayPlan     `json:"plan,omitempty"`
		Report     *serving.GatewayReport   `json:"report,omitempty"`
		Instance   string                   `json:"instance,omitempty"`
		Progress   *serving.BackendProgress `json:"progress,omitempty"`
		State      *serving.InstanceState   `json:"state,omitempty"`
		Owner      string                   `json:"owner,omitempty"`
		Recovery   []serving.GatewayReport  `json:"recovery,omitempty"`
	}{s.received, payload.RouterID, payload.Generation, payload.Plan, payload.Report, payload.Instance, payload.Progress, payload.State, payload.Owner, payload.Recovery})
	if err != nil {
		s.cancel()
		return err
	}
	if s.bytes+len(data) > live.QueueBytes {
		s.cancel()
		return live.ErrClosed
	}
	select {
	case s.queue <- data:
		s.bytes += len(data)
		return nil
	default:
		s.cancel()
		return live.ErrClosed
	}
}
func (s *channel) writeLoop() {
	defer s.close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case data := <-s.queue:
			s.mu.Lock()
			s.bytes -= len(data)
			s.mu.Unlock()
			if live.Write(s.ctx, s.conn, data) != nil {
				return
			}
		}
	}
}
func (s *channel) read() (message, error) {
	ctx, cancel := context.WithTimeout(s.ctx, FreshnessTimeout)
	defer cancel()
	data, err := live.Read(ctx, s.conn, live.MaxFrameBytes)
	if err != nil {
		return message{}, err
	}
	receivedAt := time.Now()
	var m message
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, live.ErrProtocol
	}
	var extra any
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		return m, live.ErrProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Seq != s.received+1 || m.Seq > live.MaxSeq || m.Checkpoint > s.seq || m.Checkpoint < s.checkpoint || m.At.IsZero() {
		return m, live.ErrProtocol
	}
	for {
		previous := s.lastRead.Load()
		if previous < 0 || receivedAt.UnixNano()-previous >= int64(FreshnessTimeout) {
			s.cancel()
			return m, context.DeadlineExceeded
		}
		if s.lastRead.CompareAndSwap(previous, receivedAt.UnixNano()) {
			break
		}
	}
	m.receivedAt = receivedAt
	s.received = m.Seq
	s.checkpoint = m.Checkpoint
	return m, nil
}

// This loop never takes the apply/report mutex or Runtime.mu. The runtime's
// atomic receipt deadline closes admission even while a frame handler is stuck.
func (s *channel) watchFreshness(runtime *serving.Runtime) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			runtime.SetGatewayFreshUntil(time.Now())
			return
		case <-ticker.C:
			previous := s.lastRead.Load()
			now := time.Now()
			if previous > 0 && now.UnixNano()-previous >= int64(FreshnessTimeout) && s.lastRead.CompareAndSwap(previous, -1) {
				runtime.SetGatewayFreshUntil(now)
				s.close()
				return
			}
		}
	}
}
