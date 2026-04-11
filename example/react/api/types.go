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

// SharedTask demo types

type TaskMeta struct {
	UserName string `json:"userName,omitempty"`
}

type StepResult struct {
	Step     string `json:"step"`
	Duration int    `json:"duration"`        // milliseconds
	Hash     string `json:"hash,omitempty"`  // computed hash of step name
	Error    string `json:"error,omitempty"` // non-empty if the step failed
}

type StartSharedWorkResponse struct {
	Completed     int          `json:"completed"`
	TotalSteps    int          `json:"totalSteps"`
	TotalDuration int          `json:"totalDuration"` // milliseconds
	Results       []StepResult `json:"results"`
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

// Validated request types — struct tags flow to server-side validation,
// generated Zod schemas, and OpenAPI spec constraints.

type CreateUserRequest struct {
	Name  string `json:"name"  validate:"required,min=2,max=100"`
	Email string `json:"email" validate:"required,email"`
}

type UpdateUserRequest struct {
	Name  string `json:"name,omitempty"  validate:"omitempty,min=2,max=100"`
	Email string `json:"email,omitempty" validate:"omitempty,email"`
}

type CreateTodoRequest struct {
	Title       string `json:"title"       validate:"required,min=1,max=200"`
	Description string `json:"description" validate:"max=1000"`
	Priority    int    `json:"priority"    validate:"gte=1,lte=5"`
}

type UpdateTodoRequest struct {
	Title       string `json:"title,omitempty"       validate:"omitempty,min=1,max=200"`
	Description string `json:"description,omitempty" validate:"omitempty,max=1000"`
	Priority    int    `json:"priority,omitempty"     validate:"omitempty,gte=1,lte=5"`
	Done        *bool  `json:"done,omitempty"`
}

type Todo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Done        bool   `json:"done"`
}

type ListTodosResponse struct {
	Todos []Todo `json:"todos"`
	Total int    `json:"total"`
}
