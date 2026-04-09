package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// UserPusher interface for sending push messages to specific users.
type UserPusher interface {
	PushToUser(userID string, data any)
}

// SharedState holds state shared between handler groups.
type SharedState struct {
	Broadcaster aprot.Broadcaster
	UserPusher  UserPusher
	TokenStore  *TokenStore
	Users       map[string]*User
	AuthUsers   map[string]*AuthUser // username -> AuthUser
	Mu          sync.RWMutex
	NextID      int
}

// NewSharedState creates a new shared state instance.
func NewSharedState(tokenStore *TokenStore) *SharedState {
	return &SharedState{
		TokenStore: tokenStore,
		Users:      make(map[string]*User),
		AuthUsers:  make(map[string]*AuthUser),
		NextID:     1,
	}
}

// PublicHandlers implements public API methods that don't require authentication.
type PublicHandlers struct {
	state *SharedState
}

// NewPublicHandlers creates a new PublicHandlers instance.
func NewPublicHandlers(state *SharedState) *PublicHandlers {
	return &PublicHandlers{state: state}
}

// ProtectedHandlers implements API methods that require authentication.
type ProtectedHandlers struct {
	state *SharedState
}

// NewProtectedHandlers creates a new ProtectedHandlers instance.
func NewProtectedHandlers(state *SharedState) *ProtectedHandlers {
	return &ProtectedHandlers{state: state}
}

// CreateUser creates a new user.
func (h *PublicHandlers) CreateUser(ctx context.Context, name string, email string) (*CreateUserResponse, error) {
	if name == "" {
		return nil, aprot.ErrInvalidParams("name is required")
	}
	if email == "" {
		return nil, aprot.ErrInvalidParams("email is required")
	}

	h.state.Mu.Lock()
	id := fmt.Sprintf("user_%d", h.state.NextID)
	h.state.NextID++
	user := &User{
		ID:    id,
		Name:  name,
		Email: email,
	}
	h.state.Users[id] = user
	h.state.Mu.Unlock()

	// Re-run every subscribed ListUsers query and push the fresh result to all
	// clients that are watching. Clients using subscribeListUsers() receive the
	// update automatically — no manual refetch.
	aprot.TriggerRefresh(ctx, "users")

	// A push event is still broadcast so clients can log the creation event.
	// Refresh triggers and push events are complementary: triggers refresh
	// subscription data, events fire one-shot notifications.
	if h.state.Broadcaster != nil {
		h.state.Broadcaster.Broadcast(&UserCreatedEvent{
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
func (h *PublicHandlers) GetUser(ctx context.Context, id string) (*GetUserResponse, error) {
	if id == "" {
		return nil, aprot.ErrInvalidParams("id is required")
	}

	// Composite trigger key: only mutations affecting this specific user refresh
	// this subscription.
	aprot.RegisterRefreshTrigger(ctx, "user", id)

	h.state.Mu.RLock()
	user, ok := h.state.Users[id]
	h.state.Mu.RUnlock()

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
func (h *PublicHandlers) ListUsers(ctx context.Context) (*ListUsersResponse, error) {
	// Subscribe this query to the "users" trigger key. CreateUser fires it and
	// every subscribed client re-renders.
	aprot.RegisterRefreshTrigger(ctx, "users")

	h.state.Mu.RLock()
	defer h.state.Mu.RUnlock()

	users := make([]User, 0, len(h.state.Users))
	for _, u := range h.state.Users {
		users = append(users, *u)
	}

	return &ListUsersResponse{Users: users}, nil
}

// ProcessBatch processes items with progress reporting.
// When the request is canceled, it inspects aprot.CancelCause(ctx) to log
// whether the client hit cancel, the connection dropped, or the server is
// shutting down — useful for observability and cleanup decisions.
func (h *PublicHandlers) ProcessBatch(ctx context.Context, items []string, delay int) (*ProcessBatchResponse, error) {
	if len(items) == 0 {
		return nil, aprot.ErrInvalidParams("items cannot be empty")
	}

	if delay <= 0 {
		delay = 500
	}

	progress := aprot.Progress(ctx)
	results := make([]string, 0, len(items))

	bail := func() error {
		switch aprot.CancelCause(ctx) {
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
func (h *PublicHandlers) SendNotification(ctx context.Context, message string, level string) (*SystemNotificationEvent, error) {
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
func (h *PublicHandlers) GetTask(ctx context.Context, id string) (*GetTaskResponse, error) {
	if id == "" {
		return nil, aprot.ErrInvalidParams("id is required")
	}
	return &GetTaskResponse{
		ID:     id,
		Name:   "Example Task",
		Status: TaskStatusRunning,
	}, nil
}

// Login authenticates a user and returns a token.
// This is a public method that doesn't require authentication.
func (h *PublicHandlers) Login(ctx context.Context, username string, password string) (*LoginResponse, error) {
	if username == "" {
		return nil, aprot.ErrInvalidParams("username is required")
	}
	if password == "" {
		return nil, aprot.ErrInvalidParams("password is required")
	}

	// Simple demo auth: any username/password combo works
	// In production, you'd verify against a database
	h.state.Mu.Lock()
	user, exists := h.state.AuthUsers[username]
	if !exists {
		// Create new user on first login (demo only)
		user = &AuthUser{
			ID:       fmt.Sprintf("auth_%d", h.state.NextID),
			Username: username,
		}
		h.state.NextID++
		h.state.AuthUsers[username] = user
	}
	h.state.Mu.Unlock()

	// Generate token
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	// Store token
	h.state.TokenStore.Store(token, user)

	// Associate connection with user for push messages
	conn := aprot.Connection(ctx)
	if conn != nil {
		conn.SetUserID(user.ID)
	}

	return &LoginResponse{
		Token:    token,
		UserID:   user.ID,
		Username: user.Username,
	}, nil
}

// ProcessWithSubTasks demonstrates hierarchical sub-tasks with progress and output.
func (h *PublicHandlers) ProcessWithSubTasks(ctx context.Context, steps []string, delay int) (*ProcessWithSubTasksResponse, error) {
	if len(steps) == 0 {
		return nil, aprot.ErrInvalidParams("steps cannot be empty")
	}

	if delay <= 0 {
		delay = 50
	}

	completed := 0
	for i, step := range steps {
		err := tasks.SubTask(ctx, step, func(ctx context.Context) error {
			tasks.Output(ctx, fmt.Sprintf("Starting %s", step))
			time.Sleep(time.Duration(delay) * time.Millisecond)
			tasks.Output(ctx, fmt.Sprintf("Finished %s", step))
			return nil
		})
		if err != nil {
			return nil, err
		}
		completed = i + 1
	}

	return &ProcessWithSubTasksResponse{Completed: completed}, nil
}

// StartSharedWork creates a shared task visible to all clients.
// The handler body is the task body — no goroutine needed.
// The task auto-completes when the handler returns nil, or auto-fails on error.
func (h *PublicHandlers) StartSharedWork(ctx context.Context, title string, steps []string, delay int) error {
	if title == "" {
		return aprot.ErrInvalidParams("title is required")
	}
	if len(steps) == 0 {
		return aprot.ErrInvalidParams("steps cannot be empty")
	}

	if delay <= 0 {
		delay = 50
	}

	ctx, task := tasks.StartTask[TaskMeta](ctx, title, tasks.Shared())
	if task == nil {
		return aprot.ErrInternal(nil)
	}

	// Attach metadata visible to all clients
	conn := aprot.Connection(ctx)
	if conn != nil {
		task.SetMeta(TaskMeta{UserName: conn.UserID()})
	}

	for i, step := range steps {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		sub := task.SubTask(step)
		task.Output(fmt.Sprintf("Working on: %s", step))
		time.Sleep(time.Duration(delay) * time.Millisecond)
		sub.Close()
		task.Progress(i+1, len(steps))
	}
	return nil
}

// GetProfile returns the authenticated user's profile.
// This method requires authentication (middleware applied via registry).
func (h *ProtectedHandlers) GetProfile(ctx context.Context) (*GetProfileResponse, error) {
	user := AuthUserFromContext(ctx)
	if user == nil {
		return nil, aprot.ErrUnauthorized("not authenticated")
	}

	return &GetProfileResponse{
		UserID:   user.ID,
		Username: user.Username,
	}, nil
}

// SendMessage sends a direct message to another user.
// This method requires authentication (middleware applied via registry).
func (h *ProtectedHandlers) SendMessage(ctx context.Context, toUserID string, message string) (*SendMessageResponse, error) {
	sender := AuthUserFromContext(ctx)
	if sender == nil {
		return nil, aprot.ErrUnauthorized("not authenticated")
	}

	if toUserID == "" {
		return nil, aprot.ErrInvalidParams("to_user_id is required")
	}
	if message == "" {
		return nil, aprot.ErrInvalidParams("message is required")
	}

	// Send push to the recipient
	if h.state.UserPusher != nil {
		h.state.UserPusher.PushToUser(toUserID, &DirectMessageEvent{
			FromUserID: sender.ID,
			FromUser:   sender.Username,
			Message:    message,
		})
	}

	return &SendMessageResponse{Sent: true}, nil
}
