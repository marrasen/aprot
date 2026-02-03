package aprot

import (
	"context"

	"github.com/go-json-experiment/json/jsontext"
)

// Request contains information about the incoming request.
type Request struct {
	ID     string         // Request ID for correlation
	Method string         // Method name being called
	Params jsontext.Value // Raw JSON parameters
}

// Handler represents the next step in the middleware chain.
type Handler func(ctx context.Context, req *Request) (any, error)

// Middleware wraps a Handler to add cross-cutting behavior.
type Middleware func(next Handler) Handler
