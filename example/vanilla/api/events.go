package api

// Push event types

type UserCreatedEvent struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type UserUpdatedEvent struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type SystemNotificationEvent struct {
	Message string `json:"message"`
	Level   string `json:"level"` // info, warning, error
}

type DirectMessageEvent struct {
	FromUserID string `json:"from_user_id"`
	FromUser   string `json:"from_user"`
	Message    string `json:"message"`
}
