package aprot

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
)

// Broadcaster is an interface for broadcasting push events to all clients.
// The event name is derived from the Go type of data, which must have been
// registered via RegisterPushEvent or RegisterPushEventFor.
type Broadcaster interface {
	Broadcast(data any)
}

// ConnectHook is called when a new connection is established.
// Return an error to reject the connection.
type ConnectHook func(ctx context.Context, conn *Conn) error

// DisconnectHook is called when a connection is closed.
type DisconnectHook func(ctx context.Context, conn *Conn)

// ServerOptions configures the server behavior.
type ServerOptions struct {
	// ReconnectInterval is the initial reconnect delay in milliseconds. Default: 1000
	ReconnectInterval int
	// ReconnectMaxInterval is the maximum reconnect delay in milliseconds. Default: 30000
	ReconnectMaxInterval int
	// ReconnectMaxAttempts is the maximum number of reconnect attempts. 0 = unlimited. Default: 0
	ReconnectMaxAttempts int
	// HeartbeatInterval is the heartbeat interval in milliseconds. Default: 30000
	HeartbeatInterval int
	// HeartbeatTimeout is the heartbeat timeout in milliseconds. Default: 5000
	HeartbeatTimeout int
}

func defaultServerOptions() ServerOptions {
	return ServerOptions{
		ReconnectInterval:    1000,
		ReconnectMaxInterval: 30000,
		ReconnectMaxAttempts: 0,
		HeartbeatInterval:    30000,
		HeartbeatTimeout:     5000,
	}
}

// Server manages WebSocket connections and handler dispatch.
type Server struct {
	registry        *Registry
	upgrader        websocket.Upgrader
	conns           map[*Conn]struct{}
	userConns       map[string]map[*Conn]struct{} // userID -> connections
	mu              sync.RWMutex
	register        chan *Conn
	unregister      chan *Conn
	middleware      []Middleware
	nextConnID      uint64 // atomic counter for connection IDs
	options         ServerOptions
	connectHooks    []ConnectHook
	disconnectHooks []DisconnectHook
	stopping        atomic.Bool   // reject new connections when set
	stopCh          chan struct{} // closed by Stop() to signal run() to drain and exit
	stopOnce        sync.Once    // ensures stopCh is closed only once
	done            chan struct{} // closed by run() when it finishes
	requestsWg      sync.WaitGroup
}

// NewServer creates a new WebSocket server with the given registry.
// An optional ServerOptions can be passed to configure server behavior.
func NewServer(registry *Registry, opts ...ServerOptions) *Server {
	options := defaultServerOptions()
	if len(opts) > 0 {
		// Merge provided options with defaults
		opt := opts[0]
		if opt.ReconnectInterval > 0 {
			options.ReconnectInterval = opt.ReconnectInterval
		}
		if opt.ReconnectMaxInterval > 0 {
			options.ReconnectMaxInterval = opt.ReconnectMaxInterval
		}
		if opt.ReconnectMaxAttempts > 0 {
			options.ReconnectMaxAttempts = opt.ReconnectMaxAttempts
		}
		if opt.HeartbeatInterval > 0 {
			options.HeartbeatInterval = opt.HeartbeatInterval
		}
		if opt.HeartbeatTimeout > 0 {
			options.HeartbeatTimeout = opt.HeartbeatTimeout
		}
	}

	s := &Server{
		registry: registry,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins by default
			},
		},
		conns:      make(map[*Conn]struct{}),
		userConns:  make(map[string]map[*Conn]struct{}),
		register:   make(chan *Conn),
		unregister: make(chan *Conn),
		middleware: []Middleware{},
		options:    options,
		stopCh:     make(chan struct{}),
		done:       make(chan struct{}),
	}
	go s.run()
	return s
}

// Use adds middleware to the chain.
// Middleware is executed in the order it is added.
func (s *Server) Use(mw ...Middleware) {
	s.middleware = append(s.middleware, mw...)
}

// OnConnect registers a hook to be called when a new connection is established.
// Hooks are called in the order they are registered.
// If a hook returns an error, the connection is rejected and subsequent hooks are not called.
func (s *Server) OnConnect(hook ConnectHook) {
	s.connectHooks = append(s.connectHooks, hook)
}

// OnDisconnect registers a hook to be called when a connection is closed.
// Hooks are called in the order they are registered.
// The connection's UserID is still available when the hook is called.
func (s *Server) OnDisconnect(hook DisconnectHook) {
	s.disconnectHooks = append(s.disconnectHooks, hook)
}

// runConnectHooks executes all connect hooks in order.
// Returns the first error encountered, or nil if all hooks succeed.
func (s *Server) runConnectHooks(ctx context.Context, conn *Conn) error {
	for _, hook := range s.connectHooks {
		if err := hook(ctx, conn); err != nil {
			return err
		}
	}
	return nil
}

// runDisconnectHooks executes all disconnect hooks in order.
func (s *Server) runDisconnectHooks(conn *Conn) {
	ctx := conn.ctx
	for _, hook := range s.disconnectHooks {
		hook(ctx, conn)
	}
}

// buildHandler creates the middleware chain for a handler.
// The chain is: server middleware -> handler middleware -> actual handler
// Server middleware is outermost (executed first on request, last on response).
func (s *Server) buildHandler(info *HandlerInfo) Handler {
	// The final handler that calls the actual method
	final := func(ctx context.Context, req *Request) (any, error) {
		return info.Call(ctx, req.Params)
	}

	handler := final

	// Apply handler-specific middleware (inner layer)
	handlerMW := s.registry.GetMiddleware(info.Name)
	for i := len(handlerMW) - 1; i >= 0; i-- {
		handler = handlerMW[i](handler)
	}

	// Apply server middleware (outer layer)
	for i := len(s.middleware) - 1; i >= 0; i-- {
		handler = s.middleware[i](handler)
	}

	return handler
}

// PushToUser sends a push message to all connections for a specific user.
// The event name is derived from the Go type of data, which must have been
// registered via RegisterPushEvent or RegisterPushEventFor.
func (s *Server) PushToUser(userID string, data any) {
	event := s.registry.eventName(data)
	s.mu.RLock()
	defer s.mu.RUnlock()

	if conns, ok := s.userConns[userID]; ok {
		for conn := range conns {
			conn.push(event, data)
		}
	}
}

// associateUser registers a connection with a user ID.
func (s *Server) associateUser(conn *Conn, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.userConns[userID] == nil {
		s.userConns[userID] = make(map[*Conn]struct{})
	}
	s.userConns[userID][conn] = struct{}{}
}

// disassociateUser removes a connection from user tracking.
func (s *Server) disassociateUser(conn *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if conn.userID != "" {
		if conns, ok := s.userConns[conn.userID]; ok {
			delete(conns, conn)
			if len(conns) == 0 {
				delete(s.userConns, conn.userID)
			}
		}
	}
}

// SetCheckOrigin sets the origin check function for the WebSocket upgrader.
func (s *Server) SetCheckOrigin(f func(r *http.Request) bool) {
	s.upgrader.CheckOrigin = f
}

// WebSocket returns an http.Handler for WebSocket upgrades.
func (s *Server) WebSocket() http.Handler {
	return http.HandlerFunc(s.ServeHTTP)
}

// HTTPTransport returns an http.Handler for SSE+HTTP transport.
// Routes:
//   - GET  / — SSE event stream
//   - POST /rpc — RPC calls
//   - POST /cancel — Request cancellation
func (s *Server) HTTPTransport() http.Handler {
	return newSSEHandler(s)
}

// ServeHTTP implements http.Handler for WebSocket upgrades.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.stopping.Load() {
		http.Error(w, "server stopping", http.StatusServiceUnavailable)
		return
	}

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	connID := atomic.AddUint64(&s.nextConnID, 1)
	ctx := r.Context()
	wst := newWSTransport(ws)
	conn := newConn(wst, s, connID, r, ctx)

	// Run connect hooks before starting message processing
	if err := s.runConnectHooks(ctx, conn); err != nil {
		// Send rejection directly before pumps start
		sendConnectionRejectedWS(ws, err)
		ws.Close()
		return
	}

	// Send config directly before pumps start
	sendConfigWS(ws, s.options)

	s.register <- conn

	go wst.writePump()
	wst.readPump(conn)
}

// Broadcast sends a push message to all connected clients.
// The event name is derived from the Go type of data, which must have been
// registered via RegisterPushEvent or RegisterPushEventFor.
func (s *Server) Broadcast(data any) {
	event := s.registry.eventName(data)
	s.mu.RLock()
	defer s.mu.RUnlock()

	for conn := range s.conns {
		conn.push(event, data)
	}
}

// ConnectionCount returns the number of active connections.
func (s *Server) ConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.conns)
}

func (s *Server) run() {
	defer close(s.done)

	stopCh := s.stopCh
	for {
		select {
		case conn := <-s.register:
			s.mu.Lock()
			s.conns[conn] = struct{}{}
			s.mu.Unlock()
		case conn := <-s.unregister:
			s.mu.Lock()
			_, existed := s.conns[conn]
			if existed {
				delete(s.conns, conn)
			}
			empty := len(s.conns) == 0
			s.mu.Unlock()

			if existed {
				s.runDisconnectHooks(conn) // Before disassociate so UserID() works
				s.disassociateUser(conn)
				conn.close()
			}

			if s.stopping.Load() && empty {
				return
			}
		case <-stopCh:
			stopCh = nil // prevent re-firing
			s.mu.RLock()
			empty := len(s.conns) == 0
			s.mu.RUnlock()
			if empty {
				return
			}
		}
	}
}

// Stop gracefully shuts down the server. It rejects new connections,
// closes existing connections with a close frame, waits for in-flight
// requests to complete, and waits for disconnect hooks to finish.
// Returns nil on clean shutdown, or ctx.Err() if the context expires.
func (s *Server) Stop(ctx context.Context) error {
	s.stopping.Store(true)

	// Close all current connections gracefully
	s.mu.RLock()
	conns := make([]*Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.RUnlock()

	for _, conn := range conns {
		conn.closeGracefully()
	}

	// Wait for in-flight requests to complete
	requestsDone := make(chan struct{})
	go func() {
		s.requestsWg.Wait()
		close(requestsDone)
	}()

	select {
	case <-requestsDone:
		// All requests finished
	case <-ctx.Done():
		return ctx.Err()
	}

	// Signal run() to exit
	s.stopOnce.Do(func() { close(s.stopCh) })
	// Wait for run() to finish processing remaining unregister events
	<-s.done

	return nil
}

// Registry returns the server's handler registry.
func (s *Server) Registry() *Registry {
	return s.registry
}

// sendConnectionRejectedWS sends an error message directly to a WebSocket connection.
// Used when a connect hook rejects the connection before pumps are started.
func sendConnectionRejectedWS(ws *websocket.Conn, err error) {
	code := CodeConnectionRejected
	message := "connection rejected"
	if perr, ok := err.(*ProtocolError); ok {
		code = perr.Code
		message = perr.Message
	} else if err != nil {
		message = err.Error()
	}

	msg := ErrorMessage{
		Type:    TypeError,
		ID:      "",
		Code:    code,
		Message: message,
	}
	data, _ := json.Marshal(msg)
	ws.WriteMessage(websocket.TextMessage, data)
}

// sendConfigWS sends the server configuration directly to a WebSocket connection.
// Called before the pumps are started.
func sendConfigWS(ws *websocket.Conn, opts ServerOptions) {
	msg := ConfigMessage{
		Type:                 TypeConfig,
		ReconnectInterval:    opts.ReconnectInterval,
		ReconnectMaxInterval: opts.ReconnectMaxInterval,
		ReconnectMaxAttempts: opts.ReconnectMaxAttempts,
		HeartbeatInterval:    opts.HeartbeatInterval,
		HeartbeatTimeout:     opts.HeartbeatTimeout,
	}
	data, _ := json.Marshal(msg)
	ws.WriteMessage(websocket.TextMessage, data)
}
