package live

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/coder/websocket"
)

const (
	ClientMessageBytes int64 = 16 << 10
	WriteTimeout             = 5 * time.Second
)

var ErrProtocol = errors.New("invalid live protocol message")

// Write bounds a single socket write, not the lifetime of a live connection.
func Write(ctx context.Context, conn *websocket.Conn, data []byte) error {
	if len(data) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	writeCtx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

// Read is shared by browser-feed and trusted internal-channel handlers. Each
// boundary chooses its message limit; neither permits binary JSON frames.
func Read(ctx context.Context, conn *websocket.Conn, limit int64) ([]byte, error) {
	if limit <= 0 || limit > MaxFrameBytes {
		return nil, ErrProtocol
	}
	conn.SetReadLimit(MaxFrameBytes + 1)
	kind, reader, err := conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageText {
		return nil, ErrProtocol
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		if limit == MaxFrameBytes {
			return nil, ErrFrameTooLarge
		}
		return nil, ErrProtocol
	}
	return data, nil
}
