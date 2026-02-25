package tasks

import "github.com/marrasen/aprot"

// TaskNodeStatus is an alias for aprot.TaskNodeStatus.
// The type lives in the root package because the generator and base client
// template reference it unconditionally.
type TaskNodeStatus = aprot.TaskNodeStatus

// Re-export status constants so callers can use tasks.TaskNodeStatusCreated, etc.
const (
	TaskNodeStatusCreated   = aprot.TaskNodeStatusCreated
	TaskNodeStatusRunning   = aprot.TaskNodeStatusRunning
	TaskNodeStatusCompleted = aprot.TaskNodeStatusCompleted
	TaskNodeStatusFailed    = aprot.TaskNodeStatusFailed
)

// TaskNodeStatusValues re-exports aprot.TaskNodeStatusValues.
func TaskNodeStatusValues() []TaskNodeStatus {
	return aprot.TaskNodeStatusValues()
}

// TaskNode is an alias for aprot.TaskNode.
type TaskNode = aprot.TaskNode

// TaskStateEvent is the push event broadcast to all clients when shared tasks change.
type TaskStateEvent struct {
	Tasks []SharedTaskState `json:"tasks"`
}

// TaskUpdateEvent is the push event for per-node output and progress updates
// on shared tasks.
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
