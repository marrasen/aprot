package aprot

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

// wsTransport wraps a WebSocket connection as a transport.
type wsTransport struct {
	ws   *websocket.Conn
	send chan outboundFrame
	done chan struct{} // closed once to signal shutdown; makes Send a no-op
	opts ServerOptions // read limit, write timeout, and keepalive settings
	// conn is a back-reference used only to report send-buffer pressure and
	// write timeouts to the server's observer. Set after construction, before
	// the pumps start; nil in transports created without a connection.
	conn *Conn
}

type outboundFrame struct {
	messageType int
	data        []byte
}

// observer returns the server's observer via the connection back-reference, or
// nil when there is no connection or no observer registered.
func (t *wsTransport) observer() Observer {
	if t.conn == nil || t.conn.server == nil {
		return nil
	}
	return t.conn.server.observer
}

func newWSTransport(ws *websocket.Conn, opts ServerOptions) *wsTransport {
	return &wsTransport{
		ws:   ws,
		send: make(chan outboundFrame, 256),
		done: make(chan struct{}),
		opts: opts,
	}
}

func (t *wsTransport) Send(data []byte) error {
	return t.sendFrame(context.Background(), outboundFrame{messageType: websocket.TextMessage, data: data})
}

func (t *wsTransport) SendCtx(ctx context.Context, data []byte) error {
	return t.sendFrame(ctx, outboundFrame{messageType: websocket.TextMessage, data: data})
}

func (t *wsTransport) SendBinary(data []byte) error {
	return t.sendFrame(context.Background(), outboundFrame{messageType: websocket.BinaryMessage, data: data})
}

func (t *wsTransport) SendBinaryCtx(ctx context.Context, data []byte) error {
	return t.sendFrame(ctx, outboundFrame{messageType: websocket.BinaryMessage, data: data})
}

func (t *wsTransport) sendFrame(ctx context.Context, frame outboundFrame) error {
	// Blocking send — waits for room in the 256-slot buffer. Unblocks
	// immediately if the transport is closed. Previously this method dropped
	// on a full buffer, which silently lost responses/progress for slow
	// clients; streams need guaranteed delivery and all other message types
	// benefit too. The wait is bounded: writePump enforces WriteTimeout, so
	// a peer that stops reading is closed instead of stalling senders forever.
	select {
	case <-t.done:
		return ErrConnectionClosed
	case t.send <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		t.reportBufferFull()
	}
	select {
	case <-t.done:
		return ErrConnectionClosed
	case t.send <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reportBufferFull signals send-buffer backpressure to the observer. Called on
// the enqueue path when the buffer has no free slot; the frame is not dropped,
// the caller falls back to a blocking send.
func (t *wsTransport) reportBufferFull() {
	if o := t.observer(); o != nil {
		o.SendBufferFull(t.conn)
	}
}

func (t *wsTransport) Close() error {
	close(t.done)
	return nil
}

func (t *wsTransport) CloseGracefully() error {
	_ = t.ws.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
		time.Now().Add(5*time.Second),
	)
	close(t.done)
	return nil
}

// readPump reads messages from the WebSocket and dispatches them to the connection.
// It enforces MaxMessageSize and, when keepalive pings are enabled, a read
// deadline that drops half-open connections whose peer no longer responds.
func (t *wsTransport) readPump(conn *Conn) {
	defer func() {
		conn.server.unregister <- conn
		_ = t.ws.Close()
	}()

	if t.opts.MaxMessageSize > 0 {
		t.ws.SetReadLimit(t.opts.MaxMessageSize)
	}
	keepalive := t.opts.PingInterval > 0 && t.opts.PongTimeout > 0
	if keepalive {
		_ = t.ws.SetReadDeadline(time.Now().Add(t.opts.PongTimeout))
		t.ws.SetPongHandler(func(string) error {
			return t.ws.SetReadDeadline(time.Now().Add(t.opts.PongTimeout))
		})
	}

	for {
		_, data, err := t.ws.ReadMessage()
		if err != nil {
			return
		}
		if keepalive {
			// Any inbound traffic proves liveness, not just pongs.
			_ = t.ws.SetReadDeadline(time.Now().Add(t.opts.PongTimeout))
		}
		conn.handleIncomingMessage(data)
	}
}

// writePump writes messages from the send channel to the WebSocket and sends
// keepalive pings. It exits when done is closed; the send channel is never
// closed (only the sender should close a channel, but multiple goroutines may
// send).
func (t *wsTransport) writePump() {
	defer t.ws.Close()

	var pingC <-chan time.Time
	if t.opts.PingInterval > 0 {
		ticker := time.NewTicker(t.opts.PingInterval)
		defer ticker.Stop()
		pingC = ticker.C
	}

	for {
		select {
		case <-t.done:
			return
		case <-pingC:
			if err := t.writeMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case frame := <-t.send:
			if err := t.writeMessage(frame.messageType, frame.data); err != nil {
				return
			}
		}
	}
}

func encodeBinaryFrame(header binaryFrameHeader, payload []byte) ([]byte, error) {
	header.Version = binaryFrameVersion
	headerData, err := marshalJSON(header)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 4+len(headerData)+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(headerData)))
	copy(frame[4:], headerData)
	copy(frame[4+len(headerData):], payload)
	return frame, nil
}

// writeMessage writes one frame, bounded by WriteTimeout so a peer that
// stops reading cannot stall the pump (and with it every Send caller).
func (t *wsTransport) writeMessage(messageType int, data []byte) error {
	if t.opts.WriteTimeout > 0 {
		_ = t.ws.SetWriteDeadline(time.Now().Add(t.opts.WriteTimeout))
	}
	err := t.ws.WriteMessage(messageType, data)
	if err != nil && isTimeoutError(err) {
		if o := t.observer(); o != nil {
			o.WriteTimedOut(t.conn)
		}
	}
	return err
}

// isTimeoutError reports whether err is (or wraps) an I/O deadline timeout,
// which for a write means the peer stopped reading and WriteTimeout elapsed.
func isTimeoutError(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
