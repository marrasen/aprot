package aprot

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// Broadcaster is an interface for broadcasting push events to all clients.
type Broadcaster interface {
	Broadcast(event string, data any)
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
func (s *Server) PushToUser(userID string, event string, data any) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if conns, ok := s.userConns[userID]; ok {
		for conn := range conns {
			conn.Push(event, data)
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

// ServeHTTP implements http.Handler for WebSocket upgrades.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	connID := atomic.AddUint64(&s.nextConnID, 1)
	ctx := r.Context()
	conn := newConn(ws, s, connID, r, ctx)

	// Run connect hooks before starting message processing
	if err := s.runConnectHooks(ctx, conn); err != nil {
		conn.sendConnectionRejected(err)
		ws.Close()
		return
	}

	// Send client configuration (writes directly to ws before pumps start)
	conn.sendConfig(s.options)

	s.register <- conn

	go conn.writePump()
	conn.readPump()
}

// Broadcast sends a push message to all connected clients.
func (s *Server) Broadcast(event string, data any) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for conn := range s.conns {
		conn.Push(event, data)
	}
}

// ConnectionCount returns the number of active connections.
func (s *Server) ConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.conns)
}

func (s *Server) run() {
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
			s.mu.Unlock()

			if existed {
				s.runDisconnectHooks(conn) // Before disassociate so UserID() works
				s.disassociateUser(conn)
				conn.close()
			}
		}
	}
}

// Registry returns the server's handler registry.
func (s *Server) Registry() *Registry {
	return s.registry
}
