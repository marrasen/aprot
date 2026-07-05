package aprot

import "github.com/go-json-experiment/json/jsontext"

const binaryFrameVersion byte = 1

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
	TypeStreamChunk MessageType = "stream_chunk"
	TypeStreamEnd   MessageType = "stream_end"
	// TypeAuth is a client->server frame carrying a token for first-message
	// authentication (and mid-session token refresh). TypeAuthOK / TypeAuthError
	// are the server's responses.
	TypeAuth      MessageType = "auth"
	TypeAuthOK    MessageType = "auth_ok"
	TypeAuthError MessageType = "auth_error"
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
	// Token carries the auth token on a TypeAuth frame (first-message auth and
	// mid-session refresh). Empty for all other message types.
	Token string `json:"token,omitempty"`
}

// AuthResultMessage is the server's response to a TypeAuth frame: TypeAuthOK on
// success, or TypeAuthError (with a Message) on failure.
type AuthResultMessage struct {
	Type    MessageType `json:"type"`
	Message string      `json:"message,omitempty"`
}

// ResponseMessage represents a successful response from server to client.
type ResponseMessage struct {
	Type   MessageType `json:"type"`
	ID     string      `json:"id"`
	Result any         `json:"result"`
}

type binaryFrameHeader struct {
	Version     byte   `json:"version"`
	Type        string `json:"type"`
	ID          string `json:"id"`
	ContentType string `json:"contentType,omitempty"`
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

// StreamChunkMessage carries a batch of consecutive elements of a
// server-streamed iterator in a single frame. It is sent instead of per-item
// [StreamItemMessage] frames when the server enables
// [ServerOptions.StreamChunking]. Each element of Items has the same shape a
// StreamItemMessage.Item would have (the element value, or a 2-element
// [K, V] array for HandlerKindStream2 handlers); items are pre-marshaled so
// a chunk is assembled without re-encoding them.
type StreamChunkMessage struct {
	Type  MessageType      `json:"type"`
	ID    string           `json:"id"`
	Items []jsontext.Value `json:"items"`
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
