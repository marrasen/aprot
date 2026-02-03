package aprot

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// HandlerInfo contains metadata about a registered handler method.
type HandlerInfo struct {
	Name         string
	RequestType  reflect.Type
	ResponseType reflect.Type
	StructName   string
	method       reflect.Value
	handler      reflect.Value
	Options      HandlerOptions
}

// HandlerOptions contains per-method metadata for middleware.
type HandlerOptions struct {
	RequireAuth bool
	Tags        []string
	Custom      map[string]any
}

// Option is a function that modifies HandlerOptions.
type Option func(*HandlerOptions)

// WithAuth marks a handler as requiring authentication.
func WithAuth() Option {
	return func(o *HandlerOptions) {
		o.RequireAuth = true
	}
}

// WithTags adds tags to a handler for filtering/categorization.
func WithTags(tags ...string) Option {
	return func(o *HandlerOptions) {
		o.Tags = append(o.Tags, tags...)
	}
}

// WithCustom adds a custom key-value pair to handler options.
func WithCustom(key string, value any) Option {
	return func(o *HandlerOptions) {
		if o.Custom == nil {
			o.Custom = make(map[string]any)
		}
		o.Custom[key] = value
	}
}

// PushEventInfo describes a push event for code generation.
type PushEventInfo struct {
	Name       string
	DataType   reflect.Type
	StructName string
}

// ErrorCodeInfo describes a custom error code for code generation.
type ErrorCodeInfo struct {
	Name string // e.g., "EndOfFile"
	Code int    // e.g., 1000
}

// errorMapping maps a Go error to a code for automatic conversion.
type errorMapping struct {
	err  error
	code int
}

// HandlerGroup contains all methods from a single handler struct.
type HandlerGroup struct {
	Name       string
	Handlers   map[string]*HandlerInfo
	PushEvents []PushEventInfo
}

// Registry holds registered handlers and their methods.
type Registry struct {
	handlers      map[string]*HandlerInfo
	groups        map[string]*HandlerGroup
	pushEvents    []PushEventInfo
	errorCodes    []ErrorCodeInfo  // custom error codes for generation
	errorMappings []errorMapping   // error -> code mappings
	nextErrorCode int              // auto-incrementing error code
}

// NewRegistry creates a new handler registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers:      make(map[string]*HandlerInfo),
		groups:        make(map[string]*HandlerGroup),
		pushEvents:    []PushEventInfo{},
		errorCodes:    []ErrorCodeInfo{},
		errorMappings: []errorMapping{},
		nextErrorCode: 1000, // Start custom codes at 1000
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

	structName := t.Elem().Name()
	group := &HandlerGroup{
		Name:     structName,
		Handlers: make(map[string]*HandlerInfo),
	}

	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		if info := validateMethod(method, v, structName); info != nil {
			r.handlers[info.Name] = info
			group.Handlers[info.Name] = info
		}
	}

	r.groups[structName] = group
	return nil
}

// RegisterWithOptions registers all valid handler methods with per-method options.
// methodOptions maps method names to their options.
func (r *Registry) RegisterWithOptions(handler any, methodOptions map[string][]Option) error {
	v := reflect.ValueOf(handler)
	t := v.Type()

	if t.Kind() != reflect.Ptr || t.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("handler must be a pointer to a struct")
	}

	structName := t.Elem().Name()
	group := &HandlerGroup{
		Name:     structName,
		Handlers: make(map[string]*HandlerInfo),
	}

	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		if info := validateMethod(method, v, structName); info != nil {
			// Apply options if provided for this method
			if opts, ok := methodOptions[info.Name]; ok {
				for _, opt := range opts {
					opt(&info.Options)
				}
			}
			r.handlers[info.Name] = info
			group.Handlers[info.Name] = info
		}
	}

	r.groups[structName] = group
	return nil
}

// RegisterPushEvent registers a push event type for code generation.
// The event will be associated with the most recently registered handler struct,
// or can be associated with a specific struct by calling this after Register.
func (r *Registry) RegisterPushEvent(name string, dataType any) {
	t := reflect.TypeOf(dataType)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Find the last registered group to associate with
	var structName string
	for name := range r.groups {
		structName = name
	}

	event := PushEventInfo{
		Name:       name,
		DataType:   t,
		StructName: structName,
	}
	r.pushEvents = append(r.pushEvents, event)

	// Also add to the group
	if group, ok := r.groups[structName]; ok {
		group.PushEvents = append(group.PushEvents, event)
	}
}

// RegisterPushEventFor registers a push event associated with a specific handler struct.
func (r *Registry) RegisterPushEventFor(structName, eventName string, dataType any) {
	t := reflect.TypeOf(dataType)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	event := PushEventInfo{
		Name:       eventName,
		DataType:   t,
		StructName: structName,
	}
	r.pushEvents = append(r.pushEvents, event)

	if group, ok := r.groups[structName]; ok {
		group.PushEvents = append(group.PushEvents, event)
	}
}

// Groups returns all registered handler groups.
func (r *Registry) Groups() map[string]*HandlerGroup {
	return r.groups
}

// PushEvents returns all registered push events.
func (r *Registry) PushEvents() []PushEventInfo {
	return r.pushEvents
}

// RegisterError registers a Go error with a name for code generation.
// When handlers return this error, it will be automatically converted
// to a ProtocolError with the assigned code.
// Codes are auto-assigned starting at 1000.
func (r *Registry) RegisterError(err error, name string) {
	code := r.nextErrorCode
	r.nextErrorCode++

	r.errorCodes = append(r.errorCodes, ErrorCodeInfo{
		Name: name,
		Code: code,
	})
	r.errorMappings = append(r.errorMappings, errorMapping{
		err:  err,
		code: code,
	})
}

// RegisterErrorCode registers a custom error code name without an error mapping.
// Use this for errors that will be created manually with NewError().
func (r *Registry) RegisterErrorCode(name string) int {
	code := r.nextErrorCode
	r.nextErrorCode++

	r.errorCodes = append(r.errorCodes, ErrorCodeInfo{
		Name: name,
		Code: code,
	})
	return code
}

// ErrorCodes returns all registered custom error codes.
func (r *Registry) ErrorCodes() []ErrorCodeInfo {
	return r.errorCodes
}

// LookupError checks if an error matches any registered error mapping.
// Returns the code and true if found, or 0 and false if not.
func (r *Registry) LookupError(err error) (int, bool) {
	for _, m := range r.errorMappings {
		if errors.Is(err, m.err) {
			return m.code, true
		}
	}
	return 0, false
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
func validateMethod(method reflect.Method, handlerValue reflect.Value, structName string) *HandlerInfo {
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
		StructName:   structName,
		method:       handlerValue.Method(method.Index),
		handler:      handlerValue,
	}
}

// Call invokes the handler with the given context and JSON params.
func (info *HandlerInfo) Call(ctx context.Context, params jsontext.Value) (any, error) {
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
