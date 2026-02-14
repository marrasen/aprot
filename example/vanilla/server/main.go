package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/example/vanilla/api"
)

func main() {
	// Create token store for authentication
	tokenStore := api.NewTokenStore()

	// Create shared state for handlers
	state := api.NewSharedState(tokenStore)

	// Create auth middleware (applied only to protected handlers)
	authMiddleware := api.AuthMiddleware(tokenStore)

	// Create registry with handlers
	// - PublicHandlers: no middleware
	// - ProtectedHandlers: auth middleware
	registry := api.NewRegistry(state, authMiddleware)

	// Create server
	server := aprot.NewServer(registry)

	// Set up shared state with broadcaster and user pusher
	state.Broadcaster = server
	state.UserPusher = server

	// Optional: authenticate connections via session cookie at connect time.
	// This caches the authenticated user on the connection so AuthMiddleware
	// can read it without re-loading from the store on every request.
	// server.OnConnect(api.ConnectHookAuth(tokenStore))

	// Add server-level middleware (applies to all handlers)
	server.Use(
		api.LoggingMiddleware(),
	)

	// Serve static files
	staticDir := "../client/static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		exe, _ := os.Executable()
		staticDir = filepath.Join(filepath.Dir(exe), "client", "static")
	}

	// Routes
	http.Handle("/ws", server)                              // WebSocket transport
	http.Handle("/sse", server.HTTPTransport())             // SSE+HTTP transport (GET /sse, POST /sse/rpc, POST /sse/cancel)
	http.Handle("/sse/", server.HTTPTransport())            // SSE+HTTP sub-routes
	http.Handle("/", http.FileServer(http.Dir(staticDir)))

	addr := ":8080"
	fmt.Printf("Server starting on http://localhost%s\n", addr)
	fmt.Printf("WebSocket endpoint: ws://localhost%s/ws\n", addr)
	fmt.Printf("SSE endpoint:       http://localhost%s/sse\n", addr)
	fmt.Println("\nOpen http://localhost:8080 in your browser")
	fmt.Println("\nMiddleware: Logging (all), Authentication (protected handlers)")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
