package aprot

import "encoding/json"

// MessageType represents the type of protocol message.
type MessageType string

const (
	TypeRequest  MessageType = "request"
	TypeCancel   MessageType = "cancel"
	TypeResponse MessageType = "response"
	TypeError    MessageType = "error"
	TypeProgress MessageType = "progress"
	TypePush     MessageType = "push"
)

// IncomingMessage represents a message from client to server.
type IncomingMessage struct {
	Type   MessageType     `json:"type"`
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ResponseMessage represents a successful response from server to client.
type ResponseMessage struct {
	Type   MessageType `json:"type"`
	ID     string      `json:"id"`
	Result any         `json:"result,omitempty"`
}

// ErrorMessage represents an error response from server to client.
type ErrorMessage struct {
	Type    MessageType `json:"type"`
	ID      string      `json:"id"`
	Code    int         `json:"code"`
	Message string      `json:"message"`
}

// ProgressMessage represents a progress update from server to client.
type ProgressMessage struct {
	Type    MessageType `json:"type"`
	ID      string      `json:"id"`
	Current int         `json:"current,omitempty"`
	Total   int         `json:"total,omitempty"`
	Message string      `json:"message,omitempty"`
}

// PushMessage represents a server-initiated push message.
type PushMessage struct {
	Type  MessageType `json:"type"`
	Event string      `json:"event"`
	Data  any         `json:"data"`
}
