package aprot

import (
	"time"

	"github.com/gorilla/websocket"
)

// wsTransport wraps a WebSocket connection as a transport.
type wsTransport struct {
	ws   *websocket.Conn
	send chan []byte
}

func newWSTransport(ws *websocket.Conn) *wsTransport {
	return &wsTransport{
		ws:   ws,
		send: make(chan []byte, 256),
	}
}

func (t *wsTransport) Send(data []byte) error {
	select {
	case t.send <- data:
		return nil
	default:
		return nil // Drop message if buffer full
	}
}

func (t *wsTransport) Close() error {
	close(t.send)
	return nil
}

func (t *wsTransport) CloseGracefully() error {
	// Send a WebSocket close frame to notify the client
	_ = t.ws.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
		time.Now().Add(5*time.Second),
	)
	close(t.send)
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
func (t *wsTransport) writePump() {
	defer t.ws.Close()

	for data := range t.send {
		if err := t.ws.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}
