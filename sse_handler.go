package aprot

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

// decodeBody unmarshals a request body into v, enforcing the server's
// MaxMessageSize. It writes the error response itself and returns false
// when the body is invalid or too large.
func (h *sseHandler) decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body := r.Body
	if h.server.options.MaxMessageSize > 0 {
		body = http.MaxBytesReader(w, r.Body, h.server.options.MaxMessageSize)
	}
	if err := json.UnmarshalRead(body, v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

// sseHandler handles SSE+HTTP transport.
type sseHandler struct {
	server      *Server
	connections map[string]*Conn
	mu          sync.RWMutex
}

func newSSEHandler(s *Server) *sseHandler {
	return &sseHandler{
		server:      s,
		connections: make(map[string]*Conn),
	}
}

func (h *sseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet:
		h.handleSSE(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/rpc":
		h.handleRPC(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/cancel":
		h.handleCancel(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *sseHandler) handleSSE(w http.ResponseWriter, r *http.Request) {
	if h.server.stopping.Load() {
		http.Error(w, "server stopping", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Generate connection ID
	connectionID := generateConnectionID()

	// Create transport and connection
	sseT := newSSETransport(w, flusher)
	connID := atomic.AddUint64(&h.server.nextConnID, 1)
	conn := newConn(sseT, h.server, connID, r, r.Context())

	// Run connect hooks
	if err := h.server.runConnectHooks(r.Context(), conn); err != nil {
		// A hook may have called SetUserID before a later hook rejected the
		// connection; undo that association so a dead conn can't linger in
		// userConns and have a later PushToUser block on its send buffer.
		h.server.disassociateUser(conn)
		code := CodeConnectionRejected
		var message string
		var errData any
		if perr, ok := err.(*ProtocolError); ok {
			code = perr.Code
			message = perr.Message
			errData = perr.Data
		} else {
			message = err.Error()
		}
		errMsg := ErrorMessage{
			Type:    TypeError,
			Code:    code,
			Message: message,
			Data:    errData,
		}
		data, _ := json.Marshal(errMsg)
		sseT.sendEvent("error", data)
		return
	}

	// Send connected event with connection ID
	connMsg := ConnectedMessage{
		Type:         TypeConnected,
		ConnectionID: connectionID,
	}
	connData, _ := json.Marshal(connMsg)
	sseT.sendEvent("connected", connData)

	// Send config
	configMsg := ConfigMessage{
		Type:                 TypeConfig,
		ReconnectInterval:    h.server.options.ReconnectInterval,
		ReconnectMaxInterval: h.server.options.ReconnectMaxInterval,
		ReconnectMaxAttempts: h.server.options.ReconnectMaxAttempts,
	}
	configData, _ := json.Marshal(configMsg)
	sseT.sendEvent("config", configData)

	// Register connection
	h.mu.Lock()
	h.connections[connectionID] = conn
	h.mu.Unlock()

	// Register, but don't block forever if the server has already shut down
	// (run() has exited and will never read s.register).
	select {
	case h.server.register <- conn:
	case <-h.server.done:
		h.server.disassociateUser(conn)
		h.mu.Lock()
		delete(h.connections, connectionID)
		h.mu.Unlock()
		_ = sseT.Close()
		return
	}

	// Keep-alive loop, blocks until client disconnects
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			// Client disconnected — close transport first to drain in-flight writes
			// before the HTTP server finalizes the response writer.
			_ = sseT.Close()
			h.mu.Lock()
			delete(h.connections, connectionID)
			h.mu.Unlock()
			h.server.unregister <- conn
			return
		case <-sseT.done:
			// Transport was closed (e.g. by server shutdown)
			h.mu.Lock()
			delete(h.connections, connectionID)
			h.mu.Unlock()
			h.server.unregister <- conn
			return
		case <-keepAlive.C:
			sseT.sendComment("keep-alive")
		}
	}
}

// rpcRequest is the expected JSON body for POST /rpc.
type rpcRequest struct {
	Type         string         `json:"type,omitempty"`
	ConnectionID string         `json:"connectionId"`
	ID           string         `json:"id"`
	Method       string         `json:"method,omitempty"`
	Params       jsontext.Value `json:"params,omitempty"`
}

func (h *sseHandler) handleRPC(w http.ResponseWriter, r *http.Request) {
	if h.server.stopping.Load() {
		http.Error(w, "server stopping", http.StatusServiceUnavailable)
		return
	}

	var req rpcRequest
	if !h.decodeBody(w, r, &req) {
		return
	}

	h.mu.RLock()
	conn, ok := h.connections[req.ConnectionID]
	h.mu.RUnlock()

	if !ok {
		http.Error(w, "unknown connection ID", http.StatusBadRequest)
		return
	}

	// Dispatch based on message type. dispatchRequest/dispatchSubscribe reserve
	// an in-flight slot (rejecting with CodeTooManyRequests over the SSE stream
	// when a concurrency cap is exceeded) and manage requestsWg, so the acquire
	// stays paired with the release in the handler.
	switch req.Type {
	case "subscribe":
		conn.dispatchSubscribe(IncomingMessage{
			Type:   TypeSubscribe,
			ID:     req.ID,
			Method: req.Method,
			Params: req.Params,
		})
	case "unsubscribe":
		conn.handleUnsubscribe(req.ID)
	default:
		conn.dispatchRequest(IncomingMessage{
			Type:   TypeRequest,
			ID:     req.ID,
			Method: req.Method,
			Params: req.Params,
		})
	}

	w.WriteHeader(http.StatusAccepted)
}

// cancelRequest is the expected JSON body for POST /cancel.
type cancelRequestBody struct {
	ConnectionID string `json:"connectionId"`
	ID           string `json:"id"`
}

func (h *sseHandler) handleCancel(w http.ResponseWriter, r *http.Request) {
	var req cancelRequestBody
	if !h.decodeBody(w, r, &req) {
		return
	}

	h.mu.RLock()
	conn, ok := h.connections[req.ConnectionID]
	h.mu.RUnlock()

	if !ok {
		http.Error(w, "unknown connection ID", http.StatusBadRequest)
		return
	}

	conn.cancelRequest(req.ID)
	w.WriteHeader(http.StatusOK)
}

func generateConnectionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
