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

type ListUsersResponse struct {
	Users []User `json:"users"`
}

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type GetTaskRequest struct {
	ID string `json:"id"`
}

type GetTaskResponse struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Status TaskStatus `json:"status"`
}

type ProcessBatchRequest struct {
	Items []string `json:"items"`
	Delay int      `json:"delay"` // milliseconds per item
}

type ProcessBatchResponse struct {
	Processed int      `json:"processed"`
	Results   []string `json:"results"`
}

// Tag is used in GetDashboardResponse to exercise complex type collection:
// map values, slice-of-pointer, and map-of-slice scenarios.
type Tag struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type GetDashboardResponse struct {
	UsersByRole   map[string][]User `json:"usersByRole"`
	FeaturedUsers []*User           `json:"featuredUsers"`
	TagsByID      map[int]Tag       `json:"tagsById"`
}
