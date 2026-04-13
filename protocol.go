package aprot

import "github.com/go-json-experiment/json/jsontext"

// MessageType represents the type of protocol message.
type MessageType string

const (
	TypeRequest     MessageType = "request"
	TypeCancel      MessageType = "cancel"
	TypeResponse    MessageType = "response"
	TypeError       MessageType = "error"
	TypeProgress    MessageType = "progress"
	TypePush        MessageType = "push"
	TypeConfig      MessageType = "config"
	TypeConnected   MessageType = "connected"
	TypeSubscribe   MessageType = "subscribe"
	TypeUnsubscribe MessageType = "unsubscribe"
	TypeStreamItem  MessageType = "stream_item"
	TypeStreamEnd   MessageType = "stream_end"
)

// ConnectedMessage is sent as the first SSE event to provide the connection ID.
type ConnectedMessage struct {
	Type         MessageType `json:"type"`
	ConnectionID string      `json:"connectionId"`
}

// IncomingMessage represents a message from client to server.
// Method uses qualified "Group.Method" format (e.g., "PublicHandlers.CreateUser").
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
	Result any         `json:"result"`
}

// ErrorMessage represents an error response from server to client.
type ErrorMessage struct {
	Type    MessageType `json:"type"`
	ID      string      `json:"id"`
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    any         `json:"data,omitempty"`
}

// ProgressMessage represents a request-level progress update from server to client.
type ProgressMessage struct {
	Type    MessageType `json:"type"`
	ID      string      `json:"id"`
	Current *int        `json:"current,omitempty"`
	Total   *int        `json:"total,omitempty"`
	Message string      `json:"message,omitempty"`
}

// PushMessage represents a server-initiated push message.
type PushMessage struct {
	Type  MessageType `json:"type"`
	Event string      `json:"event"`
	Data  any         `json:"data"`
}

// StreamItemMessage carries one element of a server-streamed iterator.
//
// For HandlerKindStream handlers the Item holds the element value. For
// HandlerKindStream2 handlers the Item is a 2-element JSON array [K, V].
type StreamItemMessage struct {
	Type MessageType `json:"type"`
	ID   string      `json:"id"`
	Item any         `json:"item"`
}

// StreamEndMessage terminates a server-streamed iterator for a request.
// Code/Message/Data are set only on abnormal termination (handler panic,
// unexpected error). Clean completion (including client cancellation)
// produces an empty StreamEndMessage.
type StreamEndMessage struct {
	Type    MessageType `json:"type"`
	ID      string      `json:"id"`
	Code    int         `json:"code,omitempty"`
	Message string      `json:"message,omitempty"`
	Data    any         `json:"data,omitempty"`
}

// ConfigMessage represents server-pushed configuration for the client.
type ConfigMessage struct {
	Type                 MessageType `json:"type"`
	ReconnectInterval    int         `json:"reconnectInterval,omitempty"`
	ReconnectMaxInterval int         `json:"reconnectMaxInterval,omitempty"`
	ReconnectMaxAttempts int         `json:"reconnectMaxAttempts,omitempty"`
}
