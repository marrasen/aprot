package aprot

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Server manages WebSocket connections and handler dispatch.
type Server struct {
	registry   *Registry
	upgrader   websocket.Upgrader
	conns      map[*Conn]struct{}
	mu         sync.RWMutex
	register   chan *Conn
	unregister chan *Conn
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
		register:   make(chan *Conn),
		unregister: make(chan *Conn),
	}
	go s.run()
	return s
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

	conn := newConn(ws, s)
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
