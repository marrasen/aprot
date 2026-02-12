package aprot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"unicode"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()

type voidResponse struct{}

var voidResponseType = reflect.TypeOf(voidResponse{})

// ParamInfo describes a single handler parameter (after context.Context).
type ParamInfo struct {
	Type     reflect.Type // the actual parameter type (e.g., string, *CreateUserRequest)
	Variadic bool         // true for the last param if the method is variadic
}

// HandlerInfo contains metadata about a registered handler method.
type HandlerInfo struct {
	Name         string
	Params       []ParamInfo   // handler parameters (after ctx), empty for no-params handlers
	ResponseType reflect.Type
	StructName   string
	IsVoid       bool // true when handler returns only error
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
	sourceDir  string // directory containing handler source files (auto-detected)
}

// SourceDir returns the directory containing the handler's Go source files.
// Discovered automatically via runtime.FuncForPC during Register().
func (g *HandlerGroup) SourceDir() string {
	return g.sourceDir
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
	tasksEnabled   bool                          // true when EnableTasks() has been called
	taskMetaType   reflect.Type                  // nil when EnableTasks() used without meta
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
// A valid handler method must accept context.Context as its first parameter
// (after the receiver), followed by any number of additional parameters of any type.
// It must return either error or (*T, error).
//
// Example signatures:
//
//	func(ctx context.Context) error
//	func(ctx context.Context) (*Resp, error)
//	func(ctx context.Context, req *T) (*Resp, error)
//	func(ctx context.Context, name string, age int) (*Resp, error)
//	func(ctx context.Context, items ...string) error
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

	if _, exists := r.groups[structName]; exists {
		panic("aprot: handler already registered: " + structName)
	}

	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		if info := validateMethod(method, v, structName); info != nil {
			wireMethod := structName + "." + info.Name
			if existing, exists := r.handlers[wireMethod]; exists {
				panic(fmt.Sprintf("aprot: duplicate method %s (registered by %s and %s)", wireMethod, existing.StructName, structName))
			}
			r.handlers[wireMethod] = info
			group.Handlers[info.Name] = info

			// Extract source directory from the first valid method
			if group.sourceDir == "" {
				pc := method.Func.Pointer()
				fn := runtime.FuncForPC(pc)
				if fn != nil {
					file, _ := fn.FileLine(pc)
					group.sourceDir = filepath.Dir(file)
				}
			}
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

// EnableTasks enables the shared task system.
// It registers a CancelTask handler and the TaskStateEvent and TaskOutputEvent
// push events. Call this before creating the Server.
func (r *Registry) EnableTasks() {
	r.tasksEnabled = true
	handler := &taskCancelHandler{}
	r.Register(handler)
	r.RegisterPushEventFor(handler, TaskStateEvent{})
	r.RegisterPushEventFor(handler, TaskOutputEvent{})
}

// EnableTasksWithMeta enables the shared task system with a typed metadata struct.
// The meta type will be used in the generated TypeScript client for type-safe
// metadata on SharedTaskState and TaskNode.
//
// Example:
//
//	type TaskMeta struct {
//	    UserName string `json:"userName,omitempty"`
//	    Error    string `json:"error,omitempty"`
//	}
//	registry.EnableTasksWithMeta(TaskMeta{})
func (r *Registry) EnableTasksWithMeta(meta any) {
	t := reflect.TypeOf(meta)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	r.taskMetaType = t
	r.EnableTasks()
}

// TaskMetaType returns the registered meta type, or nil if EnableTasks() was used without meta.
func (r *Registry) TaskMetaType() reflect.Type {
	return r.taskMetaType
}

// TasksEnabled returns whether EnableTasks() has been called.
func (r *Registry) TasksEnabled() bool {
	return r.tasksEnabled
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
// Accepted signatures (any number of params after ctx):
//
//	func(ctx context.Context) error
//	func(ctx context.Context) (*U, error)
//	func(ctx context.Context, p1 T1, p2 T2, ...) error
//	func(ctx context.Context, p1 T1, p2 T2, ...) (*U, error)
//	func(ctx context.Context, items ...T) error          // variadic
//	func(ctx context.Context, items ...T) (*U, error)    // variadic
func validateMethod(method reflect.Method, handlerValue reflect.Value, structName string) *HandlerInfo {
	mt := method.Type

	// Need at least 2 inputs: receiver + context.Context
	if mt.NumIn() < 2 {
		return nil
	}

	// First param after receiver must be context.Context
	if !mt.In(1).Implements(contextType) {
		return nil
	}

	// Collect params (after receiver and ctx)
	var params []ParamInfo
	isVariadic := mt.IsVariadic()
	for i := 2; i < mt.NumIn(); i++ {
		paramType := mt.In(i)
		if isVariadic && i == mt.NumIn()-1 {
			// Variadic param: mt.In(i) is []T, store the element type
			params = append(params, ParamInfo{
				Type:     paramType.Elem(),
				Variadic: true,
			})
		} else {
			params = append(params, ParamInfo{
				Type:     paramType,
				Variadic: false,
			})
		}
	}

	return validateOutputs(method, handlerValue, structName, params)
}

// validateOutputs checks the return values of a handler method and builds a HandlerInfo.
func validateOutputs(method reflect.Method, handlerValue reflect.Value, structName string, params []ParamInfo) *HandlerInfo {
	mt := method.Type
	switch mt.NumOut() {
	case 1:
		// func(...) error
		if !mt.Out(0).Implements(errorType) {
			return nil
		}
		return &HandlerInfo{
			Name:         method.Name,
			Params:       params,
			ResponseType: voidResponseType,
			IsVoid:       true,
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
			Params:       params,
			ResponseType: respType.Elem(),
			StructName:   structName,
			method:       handlerValue.Method(method.Index),
			handler:      handlerValue,
		}
	default:
		return nil
	}
}

// Call invokes the handler with the given context and JSON params.
// Params must be a JSON array (positional arguments) or empty/nil for no-params handlers.
func (info *HandlerInfo) Call(ctx context.Context, params jsontext.Value) (any, error) {
	args := []reflect.Value{reflect.ValueOf(ctx)}

	if len(info.Params) > 0 {
		// Parse params as JSON array
		var jsonArray []jsontext.Value
		if len(params) > 0 {
			if err := json.Unmarshal(params, &jsonArray); err != nil {
				return nil, ErrInvalidParams(err.Error())
			}
		}

		// Count fixed (non-variadic) params
		fixedCount := len(info.Params)
		hasVariadic := len(info.Params) > 0 && info.Params[len(info.Params)-1].Variadic
		if hasVariadic {
			fixedCount--
		}

		// Validate param count
		if hasVariadic {
			if len(jsonArray) < fixedCount {
				return nil, ErrInvalidParams(fmt.Sprintf("expected at least %d params, got %d", fixedCount, len(jsonArray)))
			}
		} else {
			if len(jsonArray) != len(info.Params) {
				return nil, ErrInvalidParams(fmt.Sprintf("expected %d params, got %d", len(info.Params), len(jsonArray)))
			}
		}

		// Unmarshal fixed params
		for i := 0; i < fixedCount; i++ {
			val, err := unmarshalParam(info.Params[i].Type, jsonArray[i])
			if err != nil {
				return nil, ErrInvalidParams(err.Error())
			}
			args = append(args, val)
		}

		// Unmarshal variadic params (passed as individual args to Call)
		if hasVariadic {
			variadicType := info.Params[len(info.Params)-1].Type
			for i := fixedCount; i < len(jsonArray); i++ {
				val, err := unmarshalParam(variadicType, jsonArray[i])
				if err != nil {
					return nil, ErrInvalidParams(err.Error())
				}
				args = append(args, val)
			}
		}
	}

	results := info.method.Call(args)

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

// unmarshalParam unmarshals a JSON value into the appropriate reflect.Value for a parameter type.
func unmarshalParam(t reflect.Type, data jsontext.Value) (reflect.Value, error) {
	if t.Kind() == reflect.Ptr {
		// Pointer param: create *T, unmarshal into it, return the pointer
		ptr := reflect.New(t.Elem())
		if err := json.Unmarshal(data, ptr.Interface()); err != nil {
			return reflect.Value{}, err
		}
		return ptr, nil
	}
	// Value param: create *T, unmarshal, return the dereferenced value
	ptr := reflect.New(t)
	if err := json.Unmarshal(data, ptr.Interface()); err != nil {
		return reflect.Value{}, err
	}
	return ptr.Elem(), nil
}
