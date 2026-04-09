package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// Handlers implements the API methods.
type Handlers struct {
	broadcaster aprot.Broadcaster
	users       map[string]*User
	mu          sync.RWMutex
	nextID      int
}

// NewHandlers creates a new Handlers instance.
func NewHandlers() *Handlers {
	return &Handlers{
		users:  make(map[string]*User),
		nextID: 1,
	}
}

// SetBroadcaster sets the broadcaster for push events.
func (h *Handlers) SetBroadcaster(b aprot.Broadcaster) {
	h.broadcaster = b
}

// CreateUser creates a new user.
func (h *Handlers) CreateUser(ctx context.Context, name string, email string) (*CreateUserResponse, error) {
	if name == "" {
		return nil, aprot.ErrInvalidParams("name is required")
	}
	if email == "" {
		return nil, aprot.ErrInvalidParams("email is required")
	}

	h.mu.Lock()
	id := fmt.Sprintf("user_%d", h.nextID)
	h.nextID++
	user := &User{
		ID:    id,
		Name:  name,
		Email: email,
	}
	h.users[id] = user
	h.mu.Unlock()

	// Re-execute ListUsers / GetDashboard subscriptions on every client.
	// Subscribed React hooks re-render automatically — no client-side refetch code.
	aprot.TriggerRefresh(ctx, "users")

	// Also broadcast a push event so clients can show a transient notification
	// in the event log. This demonstrates that push events and refresh triggers
	// are complementary: triggers refresh data, events fire one-shot notifications.
	if h.broadcaster != nil {
		h.broadcaster.Broadcast(&UserCreatedEvent{
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

// GetUser retrieves a user by ID.
func (h *Handlers) GetUser(ctx context.Context, id string) (*GetUserResponse, error) {
	if id == "" {
		return nil, aprot.ErrInvalidParams("id is required")
	}

	// Subscribe to refreshes keyed by this specific user ID. A mutation that
	// edits user "42" only needs to fire TriggerRefresh(ctx, "user", "42") to
	// update clients viewing that user — not every GetUser subscription.
	aprot.RegisterRefreshTrigger(ctx, "user", id)

	h.mu.RLock()
	user, ok := h.users[id]
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

// ListUsers returns all users (no request parameter needed).
func (h *Handlers) ListUsers(ctx context.Context) (*ListUsersResponse, error) {
	// Declare the trigger key this query depends on. Any handler that calls
	// aprot.TriggerRefresh(ctx, "users") will cause every subscribed client's
	// useListUsers() hook to re-render with fresh data automatically.
	aprot.RegisterRefreshTrigger(ctx, "users")

	h.mu.RLock()
	defer h.mu.RUnlock()

	users := make([]User, 0, len(h.users))
	for _, u := range h.users {
		users = append(users, *u)
	}

	return &ListUsersResponse{Users: users}, nil
}

// ProcessBatch processes items with progress reporting.
// When canceled, it distinguishes the cancel cause so the client (or an
// operator reading the logs) can tell whether the user hit Cancel, the
// connection dropped, or the server is shutting down.
func (h *Handlers) ProcessBatch(ctx context.Context, items []string, delay int) (*ProcessBatchResponse, error) {
	if len(items) == 0 {
		return nil, aprot.ErrInvalidParams("items cannot be empty")
	}

	if delay <= 0 {
		delay = 500
	}

	progress := aprot.Progress(ctx)
	results := make([]string, 0, len(items))

	bail := func() error {
		cause := aprot.CancelCause(ctx)
		switch cause {
		case aprot.ErrClientCanceled:
			log.Printf("ProcessBatch: client canceled after %d/%d items", len(results), len(items))
		case aprot.ErrConnectionClosed:
			log.Printf("ProcessBatch: connection closed after %d/%d items", len(results), len(items))
		case aprot.ErrServerShutdown:
			log.Printf("ProcessBatch: server shutdown after %d/%d items", len(results), len(items))
		}
		return aprot.ErrCanceled()
	}

	for i, item := range items {
		select {
		case <-ctx.Done():
			return nil, bail()
		default:
		}

		progress.Update(i+1, len(items), fmt.Sprintf("Processing: %s", item))
		select {
		case <-ctx.Done():
			return nil, bail()
		case <-time.After(time.Duration(delay) * time.Millisecond):
		}
		results = append(results, fmt.Sprintf("processed_%s", item))
	}

	return &ProcessBatchResponse{
		Processed: len(results),
		Results:   results,
	}, nil
}

// SendNotification sends a notification to the requesting client.
func (h *Handlers) SendNotification(ctx context.Context, message string, level string) (*SystemNotificationEvent, error) {
	evt := &SystemNotificationEvent{Message: message, Level: level}
	conn := aprot.Connection(ctx)
	if conn != nil {
		if err := conn.Push(evt); err != nil {
			return nil, err
		}
	}
	return evt, nil
}

// GetTask retrieves a task by ID (demo: returns hardcoded task).
func (h *Handlers) GetTask(ctx context.Context, id string) (*GetTaskResponse, error) {
	if id == "" {
		return nil, aprot.ErrInvalidParams("id is required")
	}
	return &GetTaskResponse{
		ID:     id,
		Name:   "Example Task",
		Status: TaskStatusRunning,
	}, nil
}

// GetDashboard returns a dashboard summary (no request parameter needed).
// Exercises complex type generation: map-of-struct, slice-of-pointer, map-of-slice.
func (h *Handlers) GetDashboard(ctx context.Context) (*GetDashboardResponse, error) {
	// Dashboard depends on user data; any user mutation re-renders it.
	aprot.RegisterRefreshTrigger(ctx, "users")

	h.mu.RLock()
	defer h.mu.RUnlock()

	users := make([]User, 0, len(h.users))
	for _, u := range h.users {
		users = append(users, *u)
	}

	return &GetDashboardResponse{
		UsersByRole:   map[string][]User{"admin": users},
		FeaturedUsers: make([]*User, 0),
		TagsByID:      map[int]Tag{1: {ID: "1", Name: "important", Color: "#ff0000"}},
	}, nil
}

// StartSharedWork creates a shared task visible to all clients.
// The task auto-completes when the handler returns nil, or auto-fails on error.
// Each step runs inside tasks.SubTask so errors are captured per-step in the
// task tree. The "Lint" step always fails to demonstrate error handling.
func (h *Handlers) StartSharedWork(ctx context.Context, title string, steps []string, delay int) (*StartSharedWorkResponse, error) {
	if title == "" {
		return nil, aprot.ErrInvalidParams("title is required")
	}
	if len(steps) == 0 {
		return nil, aprot.ErrInvalidParams("steps cannot be empty")
	}

	if delay <= 0 {
		delay = 500
	}

	ctx, task := tasks.StartTask[TaskMeta](ctx, title, tasks.Shared())
	if task == nil {
		return nil, aprot.ErrInternal(nil)
	}

	conn := aprot.Connection(ctx)
	if conn != nil {
		task.SetMeta(TaskMeta{UserName: conn.UserID()})
	}

	results := make([]StepResult, 0, len(steps))
	totalDuration := 0

	for i, step := range steps {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		stepIdx := i
		stepName := step

		var sr StepResult
		err := tasks.SubTask(ctx, stepName, func(ctx context.Context) error {
			tasks.Output(ctx, fmt.Sprintf("Working on: %s", stepName))

			stepStart := time.Now()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(delay) * time.Millisecond):
			}

			// "Lint" step always fails to demonstrate error capture
			if stepName == "Lint" {
				return fmt.Errorf("lint failed: 3 warnings, 1 error in main.go")
			}

			hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", title, stepName, stepIdx)))
			sr = StepResult{
				Step:     stepName,
				Duration: int(time.Since(stepStart).Milliseconds()),
				Hash:     hex.EncodeToString(hash[:8]),
			}
			return ctx.Err()
		})

		if err != nil {
			sr = StepResult{
				Step:  stepName,
				Error: err.Error(),
			}
		}

		results = append(results, sr)
		totalDuration += sr.Duration
		task.Progress(i+1, len(steps))
	}

	return &StartSharedWorkResponse{
		Completed:     len(results),
		TotalSteps:    len(steps),
		TotalDuration: totalDuration,
		Results:       results,
	}, nil
}
