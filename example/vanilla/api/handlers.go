package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/marrasen/aprot"
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
func (h *PublicHandlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	if req.Name == "" {
		return nil, aprot.ErrInvalidParams("name is required")
	}
	if req.Email == "" {
		return nil, aprot.ErrInvalidParams("email is required")
	}

	h.state.Mu.Lock()
	id := fmt.Sprintf("user_%d", h.state.NextID)
	h.state.NextID++
	user := &User{
		ID:    id,
		Name:  req.Name,
		Email: req.Email,
	}
	h.state.Users[id] = user
	h.state.Mu.Unlock()

	// Broadcast to all clients that a user was created
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
func (h *PublicHandlers) GetUser(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	if req.ID == "" {
		return nil, aprot.ErrInvalidParams("id is required")
	}

	h.state.Mu.RLock()
	user, ok := h.state.Users[req.ID]
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

// ListUsers returns all users.
func (h *PublicHandlers) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	h.state.Mu.RLock()
	defer h.state.Mu.RUnlock()

	users := make([]User, 0, len(h.state.Users))
	for _, u := range h.state.Users {
		users = append(users, *u)
	}

	return &ListUsersResponse{Users: users}, nil
}

// ProcessBatch processes items with progress reporting.
func (h *PublicHandlers) ProcessBatch(ctx context.Context, req *ProcessBatchRequest) (*ProcessBatchResponse, error) {
	if len(req.Items) == 0 {
		return nil, aprot.ErrInvalidParams("items cannot be empty")
	}

	delay := req.Delay
	if delay <= 0 {
		delay = 500
	}

	progress := aprot.Progress(ctx)
	results := make([]string, 0, len(req.Items))

	for i, item := range req.Items {
		select {
		case <-ctx.Done():
			return nil, aprot.ErrCanceled()
		default:
		}

		progress.Update(i+1, len(req.Items), fmt.Sprintf("Processing: %s", item))
		time.Sleep(time.Duration(delay) * time.Millisecond)
		results = append(results, fmt.Sprintf("processed_%s", item))
	}

	return &ProcessBatchResponse{
		Processed: len(results),
		Results:   results,
	}, nil
}

// SendNotification sends a notification to the requesting client.
func (h *PublicHandlers) SendNotification(ctx context.Context, req *SystemNotificationEvent) (*SystemNotificationEvent, error) {
	conn := aprot.Connection(ctx)
	if conn != nil {
		conn.Push(req)
	}
	return req, nil
}

// GetTask retrieves a task by ID (demo: returns hardcoded task).
func (h *PublicHandlers) GetTask(ctx context.Context, req *GetTaskRequest) (*GetTaskResponse, error) {
	if req.ID == "" {
		return nil, aprot.ErrInvalidParams("id is required")
	}
	return &GetTaskResponse{
		ID:     req.ID,
		Name:   "Example Task",
		Status: TaskStatusRunning,
	}, nil
}

// Login authenticates a user and returns a token.
// This is a public method that doesn't require authentication.
func (h *PublicHandlers) Login(ctx context.Context, req *LoginRequest) (*LoginResponse, error) {
	if req.Username == "" {
		return nil, aprot.ErrInvalidParams("username is required")
	}
	if req.Password == "" {
		return nil, aprot.ErrInvalidParams("password is required")
	}

	// Simple demo auth: any username/password combo works
	// In production, you'd verify against a database
	h.state.Mu.Lock()
	user, exists := h.state.AuthUsers[req.Username]
	if !exists {
		// Create new user on first login (demo only)
		user = &AuthUser{
			ID:       fmt.Sprintf("auth_%d", h.state.NextID),
			Username: req.Username,
		}
		h.state.NextID++
		h.state.AuthUsers[req.Username] = user
	}
	h.state.Mu.Unlock()

	// Generate token
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
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

// GetProfile returns the authenticated user's profile.
// This method requires authentication (middleware applied via registry).
func (h *ProtectedHandlers) GetProfile(ctx context.Context, req *GetProfileRequest) (*GetProfileResponse, error) {
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
func (h *ProtectedHandlers) SendMessage(ctx context.Context, req *SendMessageRequest) (*SendMessageResponse, error) {
	sender := AuthUserFromContext(ctx)
	if sender == nil {
		return nil, aprot.ErrUnauthorized("not authenticated")
	}

	if req.ToUserID == "" {
		return nil, aprot.ErrInvalidParams("to_user_id is required")
	}
	if req.Message == "" {
		return nil, aprot.ErrInvalidParams("message is required")
	}

	// Send push to the recipient
	if h.state.UserPusher != nil {
		h.state.UserPusher.PushToUser(req.ToUserID, &DirectMessageEvent{
			FromUserID: sender.ID,
			FromUser:   sender.Username,
			Message:    req.Message,
		})
	}

	return &SendMessageResponse{Sent: true}, nil
}
