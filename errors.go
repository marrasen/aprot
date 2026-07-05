package aprot

import "fmt"

// Standard error codes.
const (
	CodeParseError         = -32700
	CodeInvalidRequest     = -32600
	CodeMethodNotFound     = -32601
	CodeInvalidParams      = -32602
	CodeInternalError      = -32603
	CodeValidationFailed   = -32604
	CodeCanceled           = -32800
	CodeUnauthorized       = -32001
	CodeConnectionRejected = -32002
	CodeForbidden          = -32003
	CodeTooManyRequests    = -32004
	CodeAuthFailed         = -32005
)

// ProtocolError represents an error that can be sent to the client.
type ProtocolError struct {
	Code    int
	Message string
	Cause   error
	Data    any // optional structured data (e.g., []FieldError for validation errors)
}

func (e *ProtocolError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *ProtocolError) Unwrap() error {
	return e.Cause
}

// NewError creates a new protocol error.
func NewError(code int, message string) *ProtocolError {
	return &ProtocolError{Code: code, Message: message}
}

// WrapError creates a new protocol error wrapping an existing error.
func WrapError(code int, message string, cause error) *ProtocolError {
	return &ProtocolError{Code: code, Message: message, Cause: cause}
}

// ErrMethodNotFound returns a method not found error.
func ErrMethodNotFound(method string) *ProtocolError {
	return NewError(CodeMethodNotFound, fmt.Sprintf("method not found: %s", method))
}

// ErrInvalidParams returns an invalid params error.
func ErrInvalidParams(reason string) *ProtocolError {
	return NewError(CodeInvalidParams, fmt.Sprintf("invalid params: %s", reason))
}

// ErrInternal returns an internal error.
func ErrInternal(cause error) *ProtocolError {
	return WrapError(CodeInternalError, "internal error", cause)
}

// ErrCanceled returns a canceled error.
func ErrCanceled() *ProtocolError {
	return NewError(CodeCanceled, "request canceled")
}

// ErrUnauthorized returns an unauthorized error.
func ErrUnauthorized(message string) *ProtocolError {
	return NewError(CodeUnauthorized, message)
}

// ErrForbidden returns a forbidden error.
func ErrForbidden(message string) *ProtocolError {
	return NewError(CodeForbidden, message)
}

// ErrConnectionRejected returns a connection rejected error.
func ErrConnectionRejected(message string) *ProtocolError {
	return NewError(CodeConnectionRejected, message)
}

// ErrTooManyRequests returns a rate/concurrency-limit error. The server sends
// this when a connection exceeds its in-flight request or subscription cap
// (see [ServerOptions.MaxConcurrentRequests], [ServerOptions.MaxSubscriptions],
// and [ServerOptions.MaxServerConcurrentRequests]).
func ErrTooManyRequests(message string) *ProtocolError {
	return NewError(CodeTooManyRequests, message)
}

// ErrAuthFailed returns an authentication failure error. Return it from a
// [Server.OnAuth] hook to reject a token; the server relays the message to the
// client in an auth_error frame (see [Server.OnAuth]).
func ErrAuthFailed(message string) *ProtocolError {
	return NewError(CodeAuthFailed, message)
}

// CancelReason represents why a request context was canceled.
type CancelReason struct {
	reason string
}

func (r *CancelReason) Error() string { return r.reason }

var (
	// ErrClientCanceled indicates the client explicitly canceled the request.
	ErrClientCanceled = &CancelReason{"client canceled"}
	// ErrConnectionClosed indicates the client disconnected.
	ErrConnectionClosed = &CancelReason{"connection closed"}
	// ErrServerShutdown indicates the server is shutting down.
	ErrServerShutdown = &CancelReason{"server shutdown"}
	// ErrBinaryUnsupported indicates the transport cannot send binary frames.
	ErrBinaryUnsupported = fmt.Errorf("binary frames unsupported")
)
