package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/example/react/api"
)

func main() {
	// Create registry from shared API package
	registry := api.NewRegistry()

	// Create server
	server := aprot.NewServer(registry)

	// Create handlers with broadcaster for push events
	handlers := api.NewHandlers()
	handlers.SetBroadcaster(server)

	// Re-register with the actual handler instance
	registry.Register(handlers)

	// Serve static files (Vite build output)
	staticDir := "../client/dist"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		exe, _ := os.Executable()
		staticDir = filepath.Join(filepath.Dir(exe), "client", "dist")
	}

	// Routes
	http.Handle("/ws", server)
	http.Handle("/", http.FileServer(http.Dir(staticDir)))

	addr := ":8080"
	fmt.Printf("Server starting on http://localhost%s\n", addr)
	fmt.Printf("WebSocket endpoint: ws://localhost%s/ws\n", addr)
	fmt.Println("\nOpen http://localhost:8080 in your browser")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
