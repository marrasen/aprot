package aprot

import "github.com/go-json-experiment/json/jsontext"

// MessageType represents the type of protocol message.
type MessageType string

const (
	TypeRequest  MessageType = "request"
	TypeCancel   MessageType = "cancel"
	TypeResponse MessageType = "response"
	TypeError    MessageType = "error"
	TypeProgress MessageType = "progress"
	TypePush     MessageType = "push"
	TypePing     MessageType = "ping"
	TypePong     MessageType = "pong"
	TypeConfig   MessageType = "config"
)

// IncomingMessage represents a message from client to server.
type IncomingMessage struct {
	Type   MessageType    `json:"type"`
	ID     string         `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
	Params jsontext.Value `json:"params,omitempty"`
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

// PongMessage represents a pong response to a client ping.
type PongMessage struct {
	Type MessageType `json:"type"`
}

// ConfigMessage represents server-pushed configuration for the client.
type ConfigMessage struct {
	Type                 MessageType `json:"type"`
	ReconnectInterval    int         `json:"reconnectInterval,omitempty"`
	ReconnectMaxInterval int         `json:"reconnectMaxInterval,omitempty"`
	ReconnectMaxAttempts int         `json:"reconnectMaxAttempts,omitempty"`
	HeartbeatInterval    int         `json:"heartbeatInterval,omitempty"`
	HeartbeatTimeout     int         `json:"heartbeatTimeout,omitempty"`
}
