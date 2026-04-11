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
	registry, handlers, todos := api.NewRegistry()

	// Create WebSocket server (all handlers)
	server := aprot.NewServer(registry)

	// Set broadcaster for push events
	handlers.SetBroadcaster(server)

	// Create REST adapter (only Todos handler exposed via REST)
	rest := aprot.NewRESTAdapter(registry, aprot.WithHandlers(todos))

	// Serve static files (Vite build output)
	staticDir := "../client/dist"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		exe, _ := os.Executable()
		staticDir = filepath.Join(filepath.Dir(exe), "client", "dist")
	}

	// Routes
	http.Handle("/ws", server)
	http.Handle("/api/", http.StripPrefix("/api", rest))
	http.Handle("/", http.FileServer(http.Dir(staticDir)))

	addr := ":8080"
	fmt.Printf("Server starting on http://localhost%s\n", addr)
	fmt.Printf("WebSocket endpoint: ws://localhost%s/ws\n", addr)
	fmt.Printf("REST API: http://localhost%s/api/\n", addr)
	fmt.Println("\nREST routes:")
	for _, r := range rest.Routes() {
		fmt.Printf("  %s %s/api%s\n", r.HTTPMethod, "http://localhost"+addr, r.Path)
	}
	fmt.Println("\nOpen http://localhost:8080 in your browser")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
