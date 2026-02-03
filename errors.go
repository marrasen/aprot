package aprot

import "fmt"

// Standard error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeCanceled       = -32800
)

// ProtocolError represents an error that can be sent to the client.
type ProtocolError struct {
	Code    int
	Message string
	Cause   error
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
