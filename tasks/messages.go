package tasks

import "github.com/marrasen/aprot"

// taskTreeMessage sends a full task tree snapshot to the requesting client.
type taskTreeMessage struct {
	Type  aprot.MessageType `json:"type"`
	ID    string            `json:"id"`
	Tasks []*TaskNode       `json:"tasks"`
}

// taskNodeOutputMessage sends text output for a specific task node.
type taskNodeOutputMessage struct {
	Type   aprot.MessageType `json:"type"`
	ID     string            `json:"id"`
	TaskID string            `json:"taskId"`
	Output string            `json:"output"`
}

// taskNodeProgressMessage sends progress for a specific task node.
type taskNodeProgressMessage struct {
	Type    aprot.MessageType `json:"type"`
	ID      string            `json:"id"`
	TaskID  string            `json:"taskId"`
	Current int               `json:"current"`
	Total   int               `json:"total"`
}
