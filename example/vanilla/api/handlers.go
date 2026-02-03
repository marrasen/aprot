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
	PushToUser(userID string, event string, data any)
}

// Handlers implements the API methods.
type Handlers struct {
	broadcaster aprot.Broadcaster
	userPusher  UserPusher
	tokenStore  *TokenStore
	users       map[string]*User
	authUsers   map[string]*AuthUser // username -> AuthUser
	mu          sync.RWMutex
	nextID      int
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(tokenStore *TokenStore) *Handlers {
	return &Handlers{
		tokenStore: tokenStore,
		users:      make(map[string]*User),
		authUsers:  make(map[string]*AuthUser),
		nextID:     1,
	}
}

// SetBroadcaster sets the broadcaster for push events.
func (h *Handlers) SetBroadcaster(b aprot.Broadcaster) {
	h.broadcaster = b
}

// SetUserPusher sets the user pusher for targeted push events.
func (h *Handlers) SetUserPusher(p UserPusher) {
	h.userPusher = p
}

// CreateUser creates a new user.
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
	if h.broadcaster != nil {
		h.broadcaster.Broadcast("UserCreated", &UserCreatedEvent{
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

// ListUsers returns all users.
func (h *Handlers) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	users := make([]User, 0, len(h.users))
	for _, u := range h.users {
		users = append(users, *u)
	}

	return &ListUsersResponse{Users: users}, nil
}

// ProcessBatch processes items with progress reporting.
func (h *Handlers) ProcessBatch(ctx context.Context, req *ProcessBatchRequest) (*ProcessBatchResponse, error) {
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
func (h *Handlers) SendNotification(ctx context.Context, req *SystemNotification) (*SystemNotification, error) {
	conn := aprot.Connection(ctx)
	if conn != nil {
		conn.Push("SystemNotification", req)
	}
	return req, nil
}

// Login authenticates a user and returns a token.
// This is a public method that doesn't require authentication.
func (h *Handlers) Login(ctx context.Context, req *LoginRequest) (*LoginResponse, error) {
	if req.Username == "" {
		return nil, aprot.ErrInvalidParams("username is required")
	}
	if req.Password == "" {
		return nil, aprot.ErrInvalidParams("password is required")
	}

	// Simple demo auth: any username/password combo works
	// In production, you'd verify against a database
	h.mu.Lock()
	user, exists := h.authUsers[req.Username]
	if !exists {
		// Create new user on first login (demo only)
		user = &AuthUser{
			ID:       fmt.Sprintf("auth_%d", h.nextID),
			Username: req.Username,
		}
		h.nextID++
		h.authUsers[req.Username] = user
	}
	h.mu.Unlock()

	// Generate token
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	// Store token
	h.tokenStore.Store(token, user)

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
// This method requires authentication (marked with WithAuth in registry).
func (h *Handlers) GetProfile(ctx context.Context, req *GetProfileRequest) (*GetProfileResponse, error) {
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
// This method requires authentication (marked with WithAuth in registry).
func (h *Handlers) SendMessage(ctx context.Context, req *SendMessageRequest) (*SendMessageResponse, error) {
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
	if h.userPusher != nil {
		h.userPusher.PushToUser(req.ToUserID, "DirectMessage", &DirectMessage{
			FromUserID: sender.ID,
			FromUser:   sender.Username,
			Message:    req.Message,
		})
	}

	return &SendMessageResponse{Sent: true}, nil
}
