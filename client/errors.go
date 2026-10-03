package client

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
)

// Standard error codes. They mirror the server's aprot.Code* constants, which
// this package deliberately does not import: a client must not drag in the
// server's dependencies.
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

var (
	// ErrClosed is returned once the client has been closed with
	// [Client.Close]. Subscriptions and streams that were still open report
	// it from their Err method.
	ErrClosed = errors.New("aprot client: closed")

	// ErrConnectionLost is returned by a call or stream that was in flight
	// when the connection dropped. The call is not retried, because the
	// server may already have run it. Subscriptions are not affected: they
	// are re-established when the client reconnects.
	ErrConnectionLost = errors.New("aprot client: connection lost")

	// ErrMessageTooLarge is returned for a request whose frame is larger
	// than the server accepts (the server's MaxMessageSize, which it reports
	// in its config frame). The server would close the connection on such a
	// frame, so the client refuses it locally: [Call] and [Stream] return
	// the error, a [Subscription] closes with it, and the connection stays
	// up. The returned error wraps ErrMessageTooLarge and names both sizes.
	// Servers that do not report a limit get no local check.
	ErrMessageTooLarge = errors.New("aprot client: message too large")
)

// Error is an error response from the server.
type Error struct {
	Code    int
	Message string
	// Data is the structured payload the server attached to the error, if
	// any. For CodeValidationFailed it holds a list of field errors; use
	// [Error.ValidationErrors] to read it.
	Data jsontext.Value
}

func (e *Error) Error() string {
	return fmt.Sprintf("aprot: %s (code %d)", e.Message, e.Code)
}

// FieldError is one entry in the payload of a CodeValidationFailed error.
// It mirrors the server's aprot.FieldError.
type FieldError struct {
	Field   string `json:"field"`
	Tag     string `json:"tag"`
	Value   any    `json:"value"`
	Param   string `json:"param"`
	Message string `json:"message"`
}

// ValidationErrors returns the field errors of a CodeValidationFailed error,
// or nil for any other error.
func (e *Error) ValidationErrors() []FieldError {
	if e.Code != CodeValidationFailed || len(e.Data) == 0 {
		return nil
	}
	var out []FieldError
	if err := json.Unmarshal(e.Data, &out); err != nil {
		return nil
	}
	return out
}

// HasCode reports whether err is, or wraps, an [*Error] with the given code.
func HasCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
