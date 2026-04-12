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

// CreateUserResponse is returned after a user is successfully created.
type CreateUserResponse struct {
	// ID is the generated unique identifier for the new user.
	ID string `json:"id"`
	// Name is the display name that was stored for the user.
	Name string `json:"name"`
	// Email is the contact email address that was stored for the user.
	Email string `json:"email"`
}

// GetUserResponse is the payload returned when looking up a single user.
type GetUserResponse struct {
	// ID is the unique identifier of the user.
	ID string `json:"id"`
	// Name is the display name of the user.
	Name string `json:"name"`
	// Email is the contact email address of the user.
	Email string `json:"email"`
}

// ListUsersResponse is the payload returned by ListUsers.
type ListUsersResponse struct {
	// Users is the full list of known users in insertion order is not guaranteed.
	Users []User `json:"users"`
}

// User represents a registered user in the demo application.
type User struct {
	// ID is the unique identifier of the user.
	ID string `json:"id"`
	// Name is the display name of the user.
	Name string `json:"name"`
	// Email is the contact email address of the user.
	Email string `json:"email"`
}

// GetTaskResponse describes the current state of a background task.
type GetTaskResponse struct {
	// ID is the unique task identifier.
	ID string `json:"id"`
	// Name is a human-readable task title.
	Name string `json:"name"`
	// Status is the current lifecycle state of the task.
	Status TaskStatus `json:"status"`
}

// ProcessBatchResponse is returned after a batch processing run completes.
type ProcessBatchResponse struct {
	// Processed is the number of items that were successfully handled.
	Processed int `json:"processed"`
	// Results contains one output entry per processed item, in input order.
	Results []string `json:"results"`
}

// SharedTask demo types

// TaskMeta is user-provided metadata attached to a shared task.
type TaskMeta struct {
	// UserName is the display name of the user who initiated the task.
	UserName string `json:"userName,omitempty"`
}

// StepResult captures the outcome of a single step in a shared task run.
type StepResult struct {
	// Step is the name of the executed step.
	Step string `json:"step"`
	// Duration is the step's execution time in milliseconds.
	Duration int `json:"duration"`
	// Hash is a deterministic hash computed from the step name, for demo purposes.
	Hash string `json:"hash,omitempty"`
	// Error is the error message if the step failed; empty on success.
	Error string `json:"error,omitempty"`
}

// StartSharedWorkResponse summarizes the outcome of a StartSharedWork invocation.
type StartSharedWorkResponse struct {
	// Completed is the number of steps that finished (successfully or not).
	Completed int `json:"completed"`
	// TotalSteps is the number of steps that were originally requested.
	TotalSteps int `json:"totalSteps"`
	// TotalDuration is the sum of all step durations in milliseconds.
	TotalDuration int `json:"totalDuration"`
	// Results holds one entry per executed step, in execution order.
	Results []StepResult `json:"results"`
}

// Tag is used in GetDashboardResponse to exercise complex type collection:
// map values, slice-of-pointer, and map-of-slice scenarios.
type Tag struct {
	// ID is the unique identifier of the tag.
	ID string `json:"id"`
	// Name is the human-readable label of the tag.
	Name string `json:"name"`
	// Color is a CSS color string used to render the tag in the UI.
	Color string `json:"color"`
}

// GetDashboardResponse is a demo payload exercising complex nested container types.
type GetDashboardResponse struct {
	// UsersByRole groups users by their role name.
	UsersByRole map[string][]User `json:"usersByRole"`
	// FeaturedUsers is a curated list of highlighted users.
	FeaturedUsers []*User `json:"featuredUsers"`
	// TagsByID maps numeric tag IDs to their full tag definitions.
	TagsByID map[int]Tag `json:"tagsById"`
}

// Validated request types — struct tags flow to server-side validation,
// generated Zod schemas, and OpenAPI spec constraints.

// CreateUserRequest is the payload accepted by the CreateUser endpoint.
type CreateUserRequest struct {
	// Name is the user's display name (2–100 characters).
	Name string `json:"name"  validate:"required,min=2,max=100"`
	// Email is the user's primary contact address; must be a valid email.
	Email string `json:"email" validate:"required,email"`
}

// UpdateUserRequest is the payload accepted by the UpdateUser endpoint.
// All fields are optional; only provided fields are updated.
type UpdateUserRequest struct {
	// Name is the new display name (2–100 characters) if provided.
	Name string `json:"name,omitempty"  validate:"omitempty,min=2,max=100"`
	// Email is the new contact address if provided; must be a valid email.
	Email string `json:"email,omitempty" validate:"omitempty,email"`
}

// CreateTodoRequest is the payload accepted by the CreateTodo endpoint.
type CreateTodoRequest struct {
	// Title is the short title of the todo item (1–200 characters).
	Title string `json:"title"       validate:"required,min=1,max=200"`
	// Description is an optional longer description (up to 1000 characters).
	Description string `json:"description" validate:"max=1000"`
	// Priority is a 1–5 ranking, with 1 being highest priority.
	Priority int `json:"priority"    validate:"gte=1,lte=5"`
}

// UpdateTodoRequest is the payload accepted by the UpdateTodo endpoint.
// All fields are optional; only provided fields are updated.
type UpdateTodoRequest struct {
	// Title is the new title of the todo item (1–200 characters) if provided.
	Title string `json:"title,omitempty"       validate:"omitempty,min=1,max=200"`
	// Description is the new description (up to 1000 characters) if provided.
	Description string `json:"description,omitempty" validate:"omitempty,max=1000"`
	// Priority is the new 1–5 priority ranking if provided.
	Priority int `json:"priority,omitempty"     validate:"omitempty,gte=1,lte=5"`
	// Done is the new completion state if provided.
	Done *bool `json:"done,omitempty"`
}

// Todo represents a todo item stored by the demo API.
type Todo struct {
	// ID is the unique identifier of the todo item.
	ID string `json:"id"`
	// Title is the short title of the todo item.
	Title string `json:"title"`
	// Description is an optional longer description of the todo item.
	Description string `json:"description"`
	// Priority is a 1–5 ranking, with 1 being highest priority.
	Priority int `json:"priority"`
	// Done is true when the todo item has been marked complete.
	Done bool `json:"done"`
}

// ListTodosResponse is the payload returned by ListTodos.
type ListTodosResponse struct {
	// Todos is the full list of todo items.
	Todos []Todo `json:"todos"`
	// Total is the number of todo items in Todos, for convenience.
	Total int `json:"total"`
}
