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

type SystemNotification struct {
	Message string `json:"message"`
	Level   string `json:"level"` // info, warning, error
}
