package aprot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"unicode"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()

type voidResponse struct{}

var voidResponseType = reflect.TypeOf(voidResponse{})

type noRequest struct{}

var noRequestType = reflect.TypeOf(noRequest{})

// HandlerInfo contains metadata about a registered handler method.
type HandlerInfo struct {
	Name         string
	RequestType  reflect.Type
	ResponseType reflect.Type
	StructName   string
	IsVoid       bool // true when handler returns only error
	NoRequest    bool // true when handler takes no request parameter
	method       reflect.Value
	handler      reflect.Value
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

// EnumValueInfo describes a single enum value.
type EnumValueInfo struct {
	Name  string // e.g., "Pending"
	Value any    // e.g., "pending" (string) or 0 (int)
}

// EnumInfo describes a registered enum type.
type EnumInfo struct {
	Name     string          // e.g., "StrState"
	Type     reflect.Type    // the reflect.Type for lookup
	IsString bool            // true for string-based, false for int-based
	Values   []EnumValueInfo
}

// HandlerGroup contains all methods from a single handler struct.
type HandlerGroup struct {
	Name       string
	Handlers   map[string]*HandlerInfo
	PushEvents []PushEventInfo
	middleware []Middleware
}

// Registry holds registered handlers and their methods.
type Registry struct {
	handlers       map[string]*HandlerInfo
	groups         map[string]*HandlerGroup
	pushEvents     []PushEventInfo
	pushEventTypes map[reflect.Type]string      // type → event name for type-safe push
	errorCodes     []ErrorCodeInfo               // custom error codes for generation
	errorMappings  []errorMapping                // error -> code mappings
	nextErrorCode  int                           // auto-incrementing error code
	enums          []EnumInfo                    // registered enum types
	enumTypes      map[reflect.Type]*EnumInfo    // for lookup in goTypeToTS
}

// NewRegistry creates a new handler registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers:       make(map[string]*HandlerInfo),
		groups:         make(map[string]*HandlerGroup),
		pushEvents:     []PushEventInfo{},
		pushEventTypes: make(map[reflect.Type]string),
		errorCodes:     []ErrorCodeInfo{},
		errorMappings:  []errorMapping{},
		nextErrorCode:  1000, // Start custom codes at 1000
		enums:          []EnumInfo{},
		enumTypes:      make(map[reflect.Type]*EnumInfo),
	}
}

// Register registers all valid handler methods from the given struct.
// Optional middleware will be applied to all methods in this handler.
// A valid handler method has the signature:
//
//	func(ctx context.Context, req *T) (*U, error)
//	func(ctx context.Context, req *T) error        // void response
//	func(ctx context.Context) (*U, error)           // no request parameter
//	func(ctx context.Context) error                 // no request, void response
//
// Example:
//
//	registry.Register(&PublicHandlers{})                    // No middleware
//	registry.Register(&UserHandlers{}, authMiddleware)      // With auth
//	registry.Register(&AdminHandlers{}, authMiddleware, adminMiddleware)
func (r *Registry) Register(handler any, middleware ...Middleware) {
	v := reflect.ValueOf(handler)
	t := v.Type()

	if t.Kind() != reflect.Ptr || t.Elem().Kind() != reflect.Struct {
		panic("aprot: Register requires a pointer to a struct")
	}

	structName := t.Elem().Name()
	group := &HandlerGroup{
		Name:       structName,
		Handlers:   make(map[string]*HandlerInfo),
		middleware: middleware,
	}

	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		if info := validateMethod(method, v, structName); info != nil {
			r.handlers[info.Name] = info
			group.Handlers[info.Name] = info
		}
	}

	r.groups[structName] = group
}

// RegisterPushEvent registers a push event type for code generation and type-safe broadcasting.
// The event name is the Go type name (e.g., UserCreatedEvent → "UserCreatedEvent").
// The event will be associated with the most recently registered handler struct.
// Broadcasting an unregistered push type will panic.
//
// Example:
//
//	registry.RegisterPushEvent(UserCreatedEvent{})
func (r *Registry) RegisterPushEvent(dataType any) {
	t := reflect.TypeOf(dataType)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	eventName := t.Name()

	// Find the last registered group to associate with
	var structName string
	for name := range r.groups {
		structName = name
	}

	event := PushEventInfo{
		Name:       eventName,
		DataType:   t,
		StructName: structName,
	}
	r.pushEvents = append(r.pushEvents, event)
	r.pushEventTypes[t] = eventName

	// Also add to the group
	if group, ok := r.groups[structName]; ok {
		group.PushEvents = append(group.PushEvents, event)
	}
}

// RegisterPushEventFor registers a push event associated with a specific handler.
// The event name is the Go type name (e.g., UserCreatedEvent → "UserCreatedEvent").
// Broadcasting an unregistered push type will panic.
//
// Example:
//
//	registry.RegisterPushEventFor(publicHandlers, UserCreatedEvent{})
func (r *Registry) RegisterPushEventFor(handler any, dataType any) {
	// Get struct name from handler
	ht := reflect.TypeOf(handler)
	if ht.Kind() == reflect.Ptr {
		ht = ht.Elem()
	}
	structName := ht.Name()

	// Get event type and derive name
	dt := reflect.TypeOf(dataType)
	if dt.Kind() == reflect.Ptr {
		dt = dt.Elem()
	}
	eventName := dt.Name()

	event := PushEventInfo{
		Name:       eventName,
		DataType:   dt,
		StructName: structName,
	}
	r.pushEvents = append(r.pushEvents, event)
	r.pushEventTypes[dt] = eventName

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

// eventName returns the registered event name for the given push data type.
// Panics if the type was not registered via RegisterPushEvent or RegisterPushEventFor.
func (r *Registry) eventName(data any) string {
	t := reflect.TypeOf(data)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name, ok := r.pushEventTypes[t]
	if !ok {
		panic("aprot: push type not registered: " + t.Name())
	}
	return name
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

// RegisterEnum registers an enum type for TypeScript generation.
// Pass the result of the Values() function generated by go-enum.
//
// Example:
//
//	registry.RegisterEnum(StrStateValues())   // string-based
//	registry.RegisterEnum(IntStatusValues())  // int-based with Stringer
func (r *Registry) RegisterEnum(values any) {
	v := reflect.ValueOf(values)
	if v.Kind() != reflect.Slice {
		panic(fmt.Sprintf("aprot: RegisterEnum requires a slice, got %v", v.Kind()))
	}
	if v.Len() == 0 {
		panic("aprot: RegisterEnum requires a non-empty slice")
	}

	elemType := v.Type().Elem()
	enumName := elemType.Name()

	// Determine if string-based or int-based
	isString := elemType.Kind() == reflect.String

	// Check if Stringer interface is implemented (for int enums)
	stringerType := reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
	hasStringer := reflect.PointerTo(elemType).Implements(stringerType)

	enumInfo := EnumInfo{
		Name:     enumName,
		Type:     elemType,
		IsString: isString,
		Values:   make([]EnumValueInfo, 0, v.Len()),
	}

	for i := 0; i < v.Len(); i++ {
		elem := v.Index(i)
		var name string
		var value any

		if isString {
			// String-based: capitalize first letter for name
			strVal := elem.String()
			name = capitalize(strVal)
			value = strVal
		} else {
			// Int-based: use String() method if available
			if hasStringer {
				// Call String() method on pointer receiver
				ptr := reflect.New(elemType)
				ptr.Elem().Set(elem)
				name = ptr.Interface().(fmt.Stringer).String()
			} else {
				// Fallback to Value0, Value1, etc.
				name = fmt.Sprintf("Value%d", i)
			}
			value = elem.Int()
		}

		enumInfo.Values = append(enumInfo.Values, EnumValueInfo{
			Name:  name,
			Value: value,
		})
	}

	r.enums = append(r.enums, enumInfo)
	r.enumTypes[elemType] = &r.enums[len(r.enums)-1]
}

// GetEnum returns the EnumInfo for a registered enum type, or nil if not registered.
func (r *Registry) GetEnum(t reflect.Type) *EnumInfo {
	return r.enumTypes[t]
}

// Enums returns all registered enum types.
func (r *Registry) Enums() []EnumInfo {
	return r.enums
}

// capitalize capitalizes the first letter of a string.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
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

// GetMiddleware returns the middleware for a specific handler method.
func (r *Registry) GetMiddleware(method string) []Middleware {
	info, ok := r.handlers[method]
	if !ok {
		return nil
	}
	group, ok := r.groups[info.StructName]
	if !ok {
		return nil
	}
	return group.middleware
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

// validateMethod checks if a method matches a handler signature.
// Accepted signatures:
//
//	func(ctx context.Context, req *T) (*U, error)
//	func(ctx context.Context, req *T) error
//	func(ctx context.Context) (*U, error)
//	func(ctx context.Context) error
func validateMethod(method reflect.Method, handlerValue reflect.Value, structName string) *HandlerInfo {
	mt := method.Type

	switch mt.NumIn() {
	case 2:
		// No request parameter: receiver + context.Context
		if !mt.In(1).Implements(contextType) {
			return nil
		}
		return validateOutputs(method, handlerValue, structName, noRequestType, true)
	case 3:
		// With request parameter: receiver + context.Context + *RequestType
		if !mt.In(1).Implements(contextType) {
			return nil
		}
		reqType := mt.In(2)
		if reqType.Kind() != reflect.Ptr || reqType.Elem().Kind() != reflect.Struct {
			return nil
		}
		return validateOutputs(method, handlerValue, structName, reqType.Elem(), false)
	default:
		return nil
	}
}

// validateOutputs checks the return values of a handler method and builds a HandlerInfo.
func validateOutputs(method reflect.Method, handlerValue reflect.Value, structName string, reqType reflect.Type, noRequest bool) *HandlerInfo {
	mt := method.Type
	switch mt.NumOut() {
	case 1:
		// func(...) error
		if !mt.Out(0).Implements(errorType) {
			return nil
		}
		return &HandlerInfo{
			Name:         method.Name,
			RequestType:  reqType,
			ResponseType: voidResponseType,
			IsVoid:       true,
			NoRequest:    noRequest,
			StructName:   structName,
			method:       handlerValue.Method(method.Index),
			handler:      handlerValue,
		}
	case 2:
		// func(...) (*Resp, error)
		respType := mt.Out(0)
		if respType.Kind() != reflect.Ptr || respType.Elem().Kind() != reflect.Struct {
			return nil
		}
		if !mt.Out(1).Implements(errorType) {
			return nil
		}
		return &HandlerInfo{
			Name:         method.Name,
			RequestType:  reqType,
			ResponseType: respType.Elem(),
			NoRequest:    noRequest,
			StructName:   structName,
			method:       handlerValue.Method(method.Index),
			handler:      handlerValue,
		}
	default:
		return nil
	}
}

// Call invokes the handler with the given context and JSON params.
func (info *HandlerInfo) Call(ctx context.Context, params jsontext.Value) (any, error) {
	var results []reflect.Value

	if info.NoRequest {
		// No request parameter — call with just context
		results = info.method.Call([]reflect.Value{
			reflect.ValueOf(ctx),
		})
	} else {
		// Create new request instance
		reqPtr := reflect.New(info.RequestType)

		// Unmarshal params if provided
		if len(params) > 0 {
			if err := json.Unmarshal(params, reqPtr.Interface(), json.MatchCaseInsensitiveNames(true)); err != nil {
				return nil, ErrInvalidParams(err.Error())
			}
		}

		// Call the method
		results = info.method.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			reqPtr,
		})
	}

	// Extract response and error
	if info.IsVoid {
		if errVal := results[0].Interface(); errVal != nil {
			return nil, errVal.(error)
		}
		return nil, nil
	}

	resp := results[0].Interface()
	errVal := results[1].Interface()

	if errVal != nil {
		return nil, errVal.(error)
	}

	return resp, nil
}
