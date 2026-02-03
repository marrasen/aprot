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

// Server manages WebSocket connections and handler dispatch.
type Server struct {
	registry   *Registry
	upgrader   websocket.Upgrader
	conns      map[*Conn]struct{}
	userConns  map[string]map[*Conn]struct{} // userID -> connections
	mu         sync.RWMutex
	register   chan *Conn
	unregister chan *Conn
	middleware []Middleware
	nextConnID uint64 // atomic counter for connection IDs
}

// NewServer creates a new WebSocket server with the given registry.
func NewServer(registry *Registry) *Server {
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
	}
	go s.run()
	return s
}

// Use adds middleware to the chain.
// Middleware is executed in the order it is added.
func (s *Server) Use(mw ...Middleware) {
	s.middleware = append(s.middleware, mw...)
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
	conn := newConn(ws, s, connID, r)
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
			s.disassociateUser(conn)
			s.mu.Lock()
			if _, ok := s.conns[conn]; ok {
				delete(s.conns, conn)
				conn.close()
			}
			s.mu.Unlock()
		}
	}
}

// Registry returns the server's handler registry.
func (s *Server) Registry() *Registry {
	return s.registry
}
