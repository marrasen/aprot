//go:build ignore

package main

import (
	"fmt"
	"os"

	"aprot"
)

// This file is run via go generate to produce the TypeScript client.
// Run: go generate ./...

func main() {
	// Create registry and register handlers
	registry := aprot.NewRegistry()
	handlers := &Handlers{}

	if err := registry.Register(handlers); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register handlers: %v\n", err)
		os.Exit(1)
	}

	gen := aprot.NewGenerator(registry)

	// Register push events
	gen.RegisterPushEvent("UserCreated", UserCreatedEvent{})
	gen.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})
	gen.RegisterPushEvent("SystemNotification", SystemNotification{})

	// Ensure static directory exists
	if err := os.MkdirAll("./static", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create static dir: %v\n", err)
		os.Exit(1)
	}

	// Generate to file
	f, err := os.Create("./static/client.ts")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	if err := gen.Generate(f); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Generated ./static/client.ts")
}
