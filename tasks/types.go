package tasks

// TaskNodeStatus represents the current state of a task node.
type TaskNodeStatus string

const (
	TaskNodeStatusCreated   TaskNodeStatus = "created"
	TaskNodeStatusRunning   TaskNodeStatus = "running"
	TaskNodeStatusCompleted TaskNodeStatus = "completed"
	TaskNodeStatusFailed    TaskNodeStatus = "failed"
)

// TaskNodeStatusValues returns all possible TaskNodeStatus values.
func TaskNodeStatusValues() []TaskNodeStatus {
	return []TaskNodeStatus{
		TaskNodeStatusCreated,
		TaskNodeStatusRunning,
		TaskNodeStatusCompleted,
		TaskNodeStatusFailed,
	}
}

// TaskNode is the JSON-serializable snapshot of a task sent to the client.
type TaskNode struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Status   TaskNodeStatus `json:"status"`
	Error    string         `json:"error,omitempty"`
	Current  int            `json:"current,omitempty"`
	Total    int            `json:"total,omitempty"`
	Meta     any            `json:"meta,omitempty"`
	Children []*TaskNode    `json:"children,omitempty"`
}

// TaskStateEvent is the push event broadcast to all clients when shared tasks change.
type TaskStateEvent struct {
	Tasks []SharedTaskState `json:"tasks"`
}

// TaskUpdateEvent is the push event for per-node output and progress updates
// on shared tasks. It replaces the former TaskOutputEvent with additional
// progress fields.
type TaskUpdateEvent struct {
	TaskID  string  `json:"taskId"`
	Output  *string `json:"output,omitempty"`
	Current *int    `json:"current,omitempty"`
	Total   *int    `json:"total,omitempty"`
}

// SharedTaskState is the wire representation of a shared task.
type SharedTaskState struct {
	ID       string         `json:"id"`
	ParentID string         `json:"parentId,omitempty"`
	Title    string         `json:"title"`
	Status   TaskNodeStatus `json:"status"`
	Error    string         `json:"error,omitempty"`
	Current  int            `json:"current,omitempty"`
	Total    int            `json:"total,omitempty"`
	Meta     any            `json:"meta,omitempty"`
	Children []*TaskNode    `json:"children,omitempty"`
	IsOwner  bool           `json:"isOwner"`
}

// TaskRef is the reference returned to the client from a handler
// when a shared task is created.
type TaskRef struct {
	TaskID string `json:"taskId"`
}

// CancelTaskRequest is the request payload for canceling a shared task.
type CancelTaskRequest struct {
	TaskID string `json:"taskId"`
}
