package aprot

import (
	"context"
	"sync"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
)

// Conn represents a single WebSocket connection.
type Conn struct {
	ws       *websocket.Conn
	server   *Server
	send     chan []byte
	requests map[string]context.CancelFunc
	mu       sync.Mutex
	closed   bool
}

func newConn(ws *websocket.Conn, server *Server) *Conn {
	return &Conn{
		ws:       ws,
		server:   server,
		send:     make(chan []byte, 256),
		requests: make(map[string]context.CancelFunc),
	}
}

// Push sends a push message to this connection.
func (c *Conn) Push(event string, data any) error {
	msg := PushMessage{
		Type:  TypePush,
		Event: event,
		Data:  data,
	}
	return c.sendJSON(msg)
}

func (c *Conn) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	select {
	case c.send <- data:
		return nil
	default:
		return nil // Drop message if buffer full
	}
}

func (c *Conn) sendResponse(id string, result any) {
	msg := ResponseMessage{
		Type:   TypeResponse,
		ID:     id,
		Result: result,
	}
	c.sendJSON(msg)
}

func (c *Conn) sendError(id string, code int, message string) {
	msg := ErrorMessage{
		Type:    TypeError,
		ID:      id,
		Code:    code,
		Message: message,
	}
	c.sendJSON(msg)
}

func (c *Conn) sendProgress(id string, current, total int, message string) {
	msg := ProgressMessage{
		Type:    TypeProgress,
		ID:      id,
		Current: current,
		Total:   total,
		Message: message,
	}
	c.sendJSON(msg)
}

func (c *Conn) registerRequest(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests[id] = cancel
}

func (c *Conn) unregisterRequest(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.requests, id)
}

func (c *Conn) cancelRequest(id string) {
	c.mu.Lock()
	cancel, ok := c.requests[id]
	c.mu.Unlock()
	if ok {
		cancel()
	}
}

func (c *Conn) readPump() {
	defer func() {
		c.server.unregister <- c
		c.ws.Close()
	}()

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}

		var msg IncomingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.sendError("", CodeParseError, "invalid JSON")
			continue
		}

		switch msg.Type {
		case TypeRequest:
			go c.handleRequest(msg)
		case TypeCancel:
			c.cancelRequest(msg.ID)
		default:
			c.sendError(msg.ID, CodeInvalidRequest, "unknown message type")
		}
	}
}

func (c *Conn) writePump() {
	defer c.ws.Close()

	for data := range c.send {
		if err := c.ws.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

func (c *Conn) handleRequest(msg IncomingMessage) {
	info, ok := c.server.registry.Get(msg.Method)
	if !ok {
		c.sendError(msg.ID, CodeMethodNotFound, "method not found: "+msg.Method)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.registerRequest(msg.ID, cancel)
	defer func() {
		c.unregisterRequest(msg.ID)
		cancel()
	}()

	// Add progress reporter and connection to context
	progress := newProgressReporter(c, msg.ID)
	ctx = withProgress(ctx, progress)
	ctx = withConnection(ctx, c)

	result, err := info.Call(ctx, msg.Params)

	// Check if context was canceled
	if ctx.Err() == context.Canceled {
		c.sendError(msg.ID, CodeCanceled, "request canceled")
		return
	}

	if err != nil {
		if perr, ok := err.(*ProtocolError); ok {
			c.sendError(msg.ID, perr.Code, perr.Message)
		} else {
			c.sendError(msg.ID, CodeInternalError, err.Error())
		}
		return
	}

	c.sendResponse(msg.ID, result)
}

func (c *Conn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.send)
	// Cancel all pending requests
	for _, cancel := range c.requests {
		cancel()
	}
	c.mu.Unlock()
}
