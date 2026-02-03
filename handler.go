package aprot

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// HandlerInfo contains metadata about a registered handler method.
type HandlerInfo struct {
	Name         string
	RequestType  reflect.Type
	ResponseType reflect.Type
	method       reflect.Value
	handler      reflect.Value
}

// Registry holds registered handlers and their methods.
type Registry struct {
	handlers map[string]*HandlerInfo
}

// NewRegistry creates a new handler registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]*HandlerInfo),
	}
}

// Register registers all valid handler methods from the given struct.
// A valid handler method has the signature:
//
//	func(ctx context.Context, req *T) (*U, error)
func (r *Registry) Register(handler any) error {
	v := reflect.ValueOf(handler)
	t := v.Type()

	if t.Kind() != reflect.Ptr || t.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("handler must be a pointer to a struct")
	}

	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		if info := validateMethod(method, v); info != nil {
			r.handlers[info.Name] = info
		}
	}

	return nil
}

// Get returns the handler info for the given method name.
func (r *Registry) Get(method string) (*HandlerInfo, bool) {
	info, ok := r.handlers[method]
	return info, ok
}

// Methods returns all registered method names.
func (r *Registry) Methods() []string {
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	return names
}

// Handlers returns all registered handler infos.
func (r *Registry) Handlers() map[string]*HandlerInfo {
	return r.handlers
}

// validateMethod checks if a method matches the handler signature.
func validateMethod(method reflect.Method, handlerValue reflect.Value) *HandlerInfo {
	mt := method.Type

	// Must have exactly 3 inputs: receiver, context.Context, *RequestType
	if mt.NumIn() != 3 {
		return nil
	}

	// First param (after receiver) must be context.Context
	if !mt.In(1).Implements(contextType) {
		return nil
	}

	// Second param must be a pointer to a struct
	reqType := mt.In(2)
	if reqType.Kind() != reflect.Ptr || reqType.Elem().Kind() != reflect.Struct {
		return nil
	}

	// Must have exactly 2 outputs: *ResponseType, error
	if mt.NumOut() != 2 {
		return nil
	}

	// First output must be a pointer to a struct
	respType := mt.Out(0)
	if respType.Kind() != reflect.Ptr || respType.Elem().Kind() != reflect.Struct {
		return nil
	}

	// Second output must be error
	if !mt.Out(1).Implements(errorType) {
		return nil
	}

	return &HandlerInfo{
		Name:         method.Name,
		RequestType:  reqType.Elem(),
		ResponseType: respType.Elem(),
		method:       handlerValue.Method(method.Index),
		handler:      handlerValue,
	}
}

// Call invokes the handler with the given context and JSON params.
func (info *HandlerInfo) Call(ctx context.Context, params json.RawMessage) (any, error) {
	// Create new request instance
	reqPtr := reflect.New(info.RequestType)

	// Unmarshal params if provided
	if len(params) > 0 {
		if err := json.Unmarshal(params, reqPtr.Interface()); err != nil {
			return nil, ErrInvalidParams(err.Error())
		}
	}

	// Call the method
	results := info.method.Call([]reflect.Value{
		reflect.ValueOf(ctx),
		reqPtr,
	})

	// Extract response and error
	resp := results[0].Interface()
	errVal := results[1].Interface()

	if errVal != nil {
		return nil, errVal.(error)
	}

	return resp, nil
}
