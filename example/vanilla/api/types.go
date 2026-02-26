package api

// Request/Response types for the API

// TaskStatus represents the status of a task.
type TaskStatus string

const (
	TaskStatusCreated   TaskStatus = "created"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

// TaskStatusValues returns all possible TaskStatus values.
func TaskStatusValues() []TaskStatus {
	return []TaskStatus{
		TaskStatusCreated,
		TaskStatusRunning,
		TaskStatusCompleted,
		TaskStatusFailed,
	}
}

type CreateUserResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type GetUserResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type ListUsersResponse struct {
	Users []User `json:"users"`
}

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type GetTaskResponse struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Status TaskStatus `json:"status"`
}

type ProcessBatchResponse struct {
	Processed int      `json:"processed"`
	Results   []string `json:"results"`
}

// Authentication types

type LoginResponse struct {
	Token    string `json:"token"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

type GetProfileResponse struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

type SendMessageResponse struct {
	Sent bool `json:"sent"`
}

// AuthUser represents an authenticated user in the context.
type AuthUser struct {
	ID       string
	Username string
}

// SubTask demo types

type ProcessWithSubTasksResponse struct {
	Completed int `json:"completed"`
}

// SharedTask demo types

type TaskMeta struct {
	UserName string `json:"userName,omitempty"`
	Error    string `json:"error,omitempty"`
}

