package aprot

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
