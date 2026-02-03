package main

// Request/Response types for the example API

type CreateUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type CreateUserResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type GetUserRequest struct {
	ID string `json:"id"`
}

type GetUserResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type ListUsersRequest struct {
}

type ListUsersResponse struct {
	Users []User `json:"users"`
}

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type ProcessBatchRequest struct {
	Items []string `json:"items"`
	Delay int      `json:"delay"` // milliseconds per item
}

type ProcessBatchResponse struct {
	Processed int      `json:"processed"`
	Results   []string `json:"results"`
}

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
