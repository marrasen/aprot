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

	// Create handlers with token store
	handlers := api.NewHandlers(tokenStore)

	// Create registry with handlers
	registry := api.NewRegistry(handlers)

	// Create server
	server := aprot.NewServer(registry)

	// Set up handlers with broadcaster and user pusher
	handlers.SetBroadcaster(server)
	handlers.SetUserPusher(server)

	// Add middleware (order matters: logging first, then auth)
	server.Use(
		api.LoggingMiddleware(),
		api.AuthMiddleware(tokenStore),
	)

	// Serve static files
	staticDir := "../client/static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		exe, _ := os.Executable()
		staticDir = filepath.Join(filepath.Dir(exe), "client", "static")
	}

	// Routes
	http.Handle("/ws", server)
	http.Handle("/", http.FileServer(http.Dir(staticDir)))

	addr := ":8080"
	fmt.Printf("Server starting on http://localhost%s\n", addr)
	fmt.Printf("WebSocket endpoint: ws://localhost%s/ws\n", addr)
	fmt.Println("\nOpen http://localhost:8080 in your browser")
	fmt.Println("\nMiddleware enabled: Logging, Authentication")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
