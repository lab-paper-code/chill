package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

const livePath = "/api/v1/live"

func (h *Handler) live(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.EscapedPath() != livePath || r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Sec-WebSocket-Protocol")) != 0 {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "live requires its exact path, no query and no subprotocol"})
		return
	}
	// This branch is deliberately outside ServeHTTP's generic request timeout
	// and ResponseController deadlines. Only review/handshake have this bound.
	authCtx, cancelAuth := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancelAuth()
	authRequest := r.WithContext(authCtx)
	if err := h.authorize(authRequest); err != nil {
		sendError(w, err)
		return
	}
	if authCtx.Err() != nil {
		sendError(w, servingkube.ErrAuthorizationUnavailable)
		return
	}
	if h.config.Live == nil {
		sendError(w, live.ErrClosed)
		return
	}
	controller := http.NewResponseController(w)
	deadline, _ := authCtx.Deadline()
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		sendError(w, err)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	cancelAuth()
	if err := controller.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		closeLive(conn, err)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	go func() {
		select {
		case <-h.config.Live.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	hello, err := h.config.Live.Hello(time.Now())
	if err != nil {
		closeLive(conn, err)
		return
	}
	if err := live.Write(ctx, conn, hello); err != nil {
		closeLive(conn, err)
		return
	}
	// No subscription and no data frames are created before the first resume.
	resume, err := readResume(ctx, conn)
	if err != nil {
		closeLive(conn, err)
		return
	}
	sub, err := h.config.Live.Subscribe(resume.From, resume.At)
	if err != nil {
		closeLive(conn, err)
		return
	}
	defer sub.Close()
	readDone := make(chan error, 1)
	go func() {
		for {
			resume, err := readResume(ctx, conn)
			if err == nil {
				err = sub.Resume(resume.From, resume.At)
			}
			if err != nil {
				readDone <- err
				cancel()
				return
			}
		}
	}()
	for {
		data, err := sub.Next(ctx)
		if err != nil {
			select {
			case readErr := <-readDone:
				err = readErr
			default:
			}
			closeLive(conn, err)
			return
		}
		if err := live.Write(ctx, conn, data); err != nil {
			closeLive(conn, err)
			return
		}
	}
}

type liveResume struct {
	From uint64
	At   string
}

func readResume(ctx context.Context, conn *websocket.Conn) (liveResume, error) {
	data, err := live.Read(ctx, conn, live.ClientMessageBytes)
	if err != nil {
		return liveResume{}, err
	}
	return decodeResume(data)
}

func decodeResume(data []byte) (liveResume, error) {
	var message struct {
		Type string          `json:"type"`
		From *uint64         `json:"from"`
		At   json.RawMessage `json:"at"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return liveResume{}, live.ErrProtocol
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return liveResume{}, live.ErrProtocol
	}
	if message.Type != "resume" || message.From == nil || *message.From > live.MaxSeq {
		return liveResume{}, live.ErrProtocol
	}
	if *message.From == 0 {
		if message.At != nil {
			return liveResume{}, live.ErrProtocol
		}
		return liveResume{}, nil
	}
	var at string
	if err := json.Unmarshal(message.At, &at); err != nil || !strings.HasSuffix(at, "Z") {
		return liveResume{}, live.ErrProtocol
	}
	if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
		return liveResume{}, live.ErrProtocol
	}
	return liveResume{From: *message.From, At: at}, nil
}

func closeLive(conn *websocket.Conn, err error) {
	switch {
	case errors.Is(err, live.ErrProtocol):
		_ = conn.Close(websocket.StatusProtocolError, "invalid live contract")
	case errors.Is(err, live.ErrFrameTooLarge):
		_ = conn.Close(websocket.StatusMessageTooBig, "live snapshot exceeds frame limit")
	case errors.Is(err, live.ErrClosed), errors.Is(err, context.Canceled):
		_ = conn.Close(websocket.StatusGoingAway, "live channel closed")
	default:
		_ = conn.Close(websocket.StatusTryAgainLater, "live delivery unavailable")
	}
}
