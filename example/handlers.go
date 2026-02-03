package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"aprot"
)

// Handlers implements the example API
type Handlers struct {
	server *aprot.Server
	users  map[string]*User
	mu     sync.RWMutex
	nextID int
}

func NewHandlers() *Handlers {
	return &Handlers{
		users:  make(map[string]*User),
		nextID: 1,
	}
}

func (h *Handlers) SetServer(s *aprot.Server) {
	h.server = s
}

// CreateUser creates a new user
func (h *Handlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	if req.Name == "" {
		return nil, aprot.ErrInvalidParams("name is required")
	}
	if req.Email == "" {
		return nil, aprot.ErrInvalidParams("email is required")
	}

	h.mu.Lock()
	id := fmt.Sprintf("user_%d", h.nextID)
	h.nextID++
	user := &User{
		ID:    id,
		Name:  req.Name,
		Email: req.Email,
	}
	h.users[id] = user
	h.mu.Unlock()

	// Broadcast to all clients that a user was created
	if h.server != nil {
		h.server.Broadcast("UserCreated", &UserCreatedEvent{
			ID:    user.ID,
			Name:  user.Name,
			Email: user.Email,
		})
	}

	return &CreateUserResponse{
		ID:    user.ID,
		Name:  user.Name,
		Email: user.Email,
	}, nil
}

// GetUser retrieves a user by ID
func (h *Handlers) GetUser(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	if req.ID == "" {
		return nil, aprot.ErrInvalidParams("id is required")
	}

	h.mu.RLock()
	user, ok := h.users[req.ID]
	h.mu.RUnlock()

	if !ok {
		return nil, aprot.NewError(404, "user not found")
	}

	return &GetUserResponse{
		ID:    user.ID,
		Name:  user.Name,
		Email: user.Email,
	}, nil
}

// ListUsers returns all users
func (h *Handlers) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	users := make([]User, 0, len(h.users))
	for _, u := range h.users {
		users = append(users, *u)
	}

	return &ListUsersResponse{Users: users}, nil
}

// ProcessBatch processes items with progress reporting
func (h *Handlers) ProcessBatch(ctx context.Context, req *ProcessBatchRequest) (*ProcessBatchResponse, error) {
	if len(req.Items) == 0 {
		return nil, aprot.ErrInvalidParams("items cannot be empty")
	}

	delay := req.Delay
	if delay <= 0 {
		delay = 500 // default 500ms per item
	}

	progress := aprot.Progress(ctx)
	results := make([]string, 0, len(req.Items))

	for i, item := range req.Items {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return nil, aprot.ErrCanceled()
		default:
		}

		// Report progress
		progress.Update(i+1, len(req.Items), fmt.Sprintf("Processing: %s", item))

		// Simulate work
		time.Sleep(time.Duration(delay) * time.Millisecond)

		results = append(results, fmt.Sprintf("processed_%s", item))
	}

	return &ProcessBatchResponse{
		Processed: len(results),
		Results:   results,
	}, nil
}

// SendNotification sends a notification to the requesting client
func (h *Handlers) SendNotification(ctx context.Context, req *SystemNotification) (*SystemNotification, error) {
	conn := aprot.Connection(ctx)
	if conn != nil {
		conn.Push("SystemNotification", req)
	}
	return req, nil
}
