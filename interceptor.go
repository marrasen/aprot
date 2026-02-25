package aprot

import "context"

// RequestInterceptor allows external packages to hook into the request
// lifecycle. BeforeRequest is called before the handler executes and may
// enrich the context. AfterRequest is called after the handler returns.
type RequestInterceptor interface {
	BeforeRequest(ctx context.Context) context.Context
	AfterRequest(ctx context.Context, err error)
}
