package aprot

import (
	"time"

	"github.com/gorilla/websocket"
)

// wsTransport wraps a WebSocket connection as a transport.
type wsTransport struct {
	ws   *websocket.Conn
	send chan []byte
	done chan struct{} // closed once to signal shutdown; makes Send a no-op
}

func newWSTransport(ws *websocket.Conn) *wsTransport {
	return &wsTransport{
		ws:   ws,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}
}

func (t *wsTransport) Send(data []byte) error {
	select {
	case <-t.done:
		return nil // transport closed, discard
	case t.send <- data:
		return nil
	default:
		return nil // Drop message if buffer full
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
func (t *wsTransport) readPump(conn *Conn) {
	defer func() {
		conn.server.unregister <- conn
		t.ws.Close()
	}()

	for {
		_, data, err := t.ws.ReadMessage()
		if err != nil {
			return
		}
		conn.handleIncomingMessage(data)
	}
}

// writePump writes messages from the send channel to the WebSocket.
// It exits when done is closed; the send channel is never closed (only
// the sender should close a channel, but multiple goroutines may send).
func (t *wsTransport) writePump() {
	defer t.ws.Close()

	for {
		select {
		case <-t.done:
			return
		case data := <-t.send:
			if err := t.ws.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}
