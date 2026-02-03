package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"aprot"
)

func main() {
	// Create registry and register handlers
	registry := aprot.NewRegistry()
	handlers := NewHandlers()

	if err := registry.Register(handlers); err != nil {
		log.Fatalf("Failed to register handlers: %v", err)
	}

	// Create server
	server := aprot.NewServer(registry)
	handlers.SetServer(server)

	// Serve static files
	staticDir := "./static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		// Try relative to executable
		exe, _ := os.Executable()
		staticDir = filepath.Join(filepath.Dir(exe), "static")
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
