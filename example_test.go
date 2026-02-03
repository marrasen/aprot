package aprot_test

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/marrasen/aprot"
)

// Request and response types
type CreateUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type CreateUserResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ProcessItemsRequest struct {
	Items []string `json:"items"`
}

type ProcessItemsResponse struct {
	Processed int `json:"processed"`
}

// Push event type
type UserUpdatedEvent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Handlers struct
type MyHandlers struct {
	server *aprot.Server
}

// CreateUser handles user creation
func (h *MyHandlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	// Access progress reporter
	progress := aprot.Progress(ctx)
	progress.Update(1, 2, "Validating...")

	// Simulate validation
	if req.Name == "" {
		return nil, aprot.ErrInvalidParams("name is required")
	}

	progress.Update(2, 2, "Creating user...")

	// Push notification to the requesting client
	conn := aprot.Connection(ctx)
	if conn != nil {
		conn.Push("UserUpdated", &UserUpdatedEvent{ID: "123", Name: req.Name})
	}

	return &CreateUserResponse{ID: "123", Name: req.Name}, nil
}

// ProcessItems demonstrates progress reporting with cancellation
func (h *MyHandlers) ProcessItems(ctx context.Context, req *ProcessItemsRequest) (*ProcessItemsResponse, error) {
	progress := aprot.Progress(ctx)
	total := len(req.Items)

	for i, item := range req.Items {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return nil, aprot.ErrCanceled()
		default:
		}

		progress.Update(i+1, total, fmt.Sprintf("Processing %s...", item))
		// Simulate work
	}

	// Broadcast to all clients
	h.server.Broadcast("ProcessingComplete", map[string]int{"count": total})

	return &ProcessItemsResponse{Processed: total}, nil
}

func Example() {
	// Create registry and register handlers
	registry := aprot.NewRegistry()
	handlers := &MyHandlers{}
	registry.Register(handlers)

	// Create server
	server := aprot.NewServer(registry)
	handlers.server = server

	// Start HTTP server with WebSocket endpoint
	http.Handle("/ws", server)
	// http.ListenAndServe(":8080", nil)

	fmt.Println("Server ready")
	// Output: Server ready
}

func Example_generate() {
	// Create registry with handlers
	registry := aprot.NewRegistry()
	registry.Register(&MyHandlers{})

	// Register push events on the registry
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	// Create generator and generate TypeScript to stdout (or file)
	gen := aprot.NewGenerator(registry)
	gen.GenerateTo(os.Stdout)
}
