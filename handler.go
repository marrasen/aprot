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

// HandlerKind categorizes a handler by its return shape so the dispatcher
// knows how to invoke and marshal its output.
type HandlerKind uint8

const (
	// HandlerKindUnary: func(ctx, ...) error | (*T, error) | (T, error)
	HandlerKindUnary HandlerKind = iota
	// HandlerKindStream: func(ctx, ...) (iter.Seq[T], error)
	HandlerKindStream
	// HandlerKindStream2: func(ctx, ...) (iter.Seq2[K, V], error)
	HandlerKindStream2
)

// ParamInfo describes a single handler parameter (after context.Context).
type ParamInfo struct {
	Type     reflect.Type // the actual parameter type (e.g., string, *CreateUserRequest)
	Variadic bool         // true for the last param if the method is variadic
}

// HandlerInfo contains metadata about a registered handler method.
type HandlerInfo struct {
	Name         string
	Params       []ParamInfo // handler parameters (after ctx), empty for no-params handlers
	ResponseType reflect.Type
	StructName   string
	IsVoid       bool        // true when handler returns only error
	Kind         HandlerKind // unary, stream, or stream2
	// StreamKeyType is set only when Kind == HandlerKindStream2; it holds the
	// key type K of iter.Seq2[K, V]. ResponseType holds the value type V.
	StreamKeyType reflect.Type
	method        reflect.Value
	handler       reflect.Value
	registry      *Registry // back-reference for accessing validator
}

// isIterSeq reports whether t has the shape of iter.Seq[T]:
//
//	func(yield func(T) bool)
//
// On success it returns the element type T. Detection is structural, so an
// anonymous func type with the same signature is accepted alongside
// iter.Seq[T] instantiations.
func isIterSeq(t reflect.Type) (elem reflect.Type, ok bool) {
	if t == nil || t.Kind() != reflect.Func {
		return nil, false
	}
	if t.NumIn() != 1 || t.NumOut() != 0 || t.IsVariadic() {
		return nil, false
	}
	y := t.In(0)
	if y.Kind() != reflect.Func {
		return nil, false
	}
	if y.NumIn() != 1 || y.NumOut() != 1 || y.IsVariadic() {
		return nil, false
	}
	if y.Out(0).Kind() != reflect.Bool {
		return nil, false
	}
	return y.In(0), true
}

// isIterSeq2 reports whether t has the shape of iter.Seq2[K, V]:
//
//	func(yield func(K, V) bool)
//
// On success it returns the key type K and value type V.
func isIterSeq2(t reflect.Type) (key, val reflect.Type, ok bool) {
	if t == nil || t.Kind() != reflect.Func {
		return nil, nil, false
	}
	if t.NumIn() != 1 || t.NumOut() != 0 || t.IsVariadic() {
		return nil, nil, false
	}
	y := t.In(0)
	if y.Kind() != reflect.Func {
		return nil, nil, false
	}
	if y.NumIn() != 2 || y.NumOut() != 1 || y.IsVariadic() {
		return nil, nil, false
	}
	if y.Out(0).Kind() != reflect.Bool {
		return nil, nil, false
	}
	return y.In(0), y.In(1), true
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
	Name     string       // e.g., "StrState"
	Type     reflect.Type // the reflect.Type for lookup
	IsString bool         // true for string-based, false for int-based
	Values   []EnumValueInfo
}

// HandlerGroup contains all methods from a single handler struct.
type HandlerGroup struct {
	Name       string
	Handlers   map[string]*HandlerInfo
	PushEvents []PushEventInfo
	Enums      []EnumInfo
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
	handlers        map[string]*HandlerInfo
	groups          map[string]*HandlerGroup
	pushEvents      []PushEventInfo
	pushEventTypes  map[reflect.Type]string                            // type → event name for type-safe push
	errorCodes      []ErrorCodeInfo                                    // custom error codes for generation
	errorMappings   []errorMapping                                     // error -> code mappings
	nextErrorCode   int                                                // auto-incrementing error code
	enumTypes       map[reflect.Type]*EnumInfo                         // for lookup in goTypeToTS
	sharedEnums     []EnumInfo                                         // enums not tied to a handler group
	generateHooks   []func(results map[string]string, mode OutputMode) // hooks run after generation
	serverInitHooks []func(s *Server)                                  // hooks run during NewServer
	validator       StructValidator                                    // optional struct validator (nil = disabled)
	restGroups      map[string]bool                                    // groups registered via RegisterREST
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
		enumTypes:      make(map[reflect.Type]*EnumInfo),
		restGroups:     make(map[string]bool),
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
	r.register(handler, true, middleware...)
}

// RegisterREST registers a handler for REST/HTTP only.
// The handler is NOT available via WebSocket — only through the REST adapter
// and OpenAPI generator.
//
// Streaming handlers (iter.Seq / iter.Seq2 return shapes) cannot be exposed
// via REST and will panic at registration time.
//
// To expose a handler via both WebSocket and REST, use Register + EnableREST:
//
//	registry.Register(&UserHandlers{})          // WebSocket only
//	registry.RegisterREST(&TodoHandlers{})      // REST only
//	registry.Register(&BothHandlers{})          // WebSocket...
//	registry.EnableREST(&BothHandlers{})        // ...and also REST
func (r *Registry) RegisterREST(handler any, middleware ...Middleware) {
	r.register(handler, false, middleware...)
	t := reflect.TypeOf(handler)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := t.Name()
	r.assertNoStreamHandlers(name, "RegisterREST")
	r.restGroups[name] = true
}

// EnableREST marks an already-registered handler for REST/HTTP exposure
// in addition to WebSocket. Streaming handlers cannot be exposed via REST
// and will panic at registration time.
func (r *Registry) EnableREST(handler any) {
	t := reflect.TypeOf(handler)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := t.Name()
	if _, ok := r.groups[name]; !ok {
		panic("aprot: EnableREST called with unregistered handler: " + name)
	}
	r.assertNoStreamHandlers(name, "EnableREST")
	r.restGroups[name] = true
}

// assertNoStreamHandlers panics if the named handler group contains any
// streaming handlers. Streaming is websocket/SSE only — the REST adapter
// cannot deliver multi-message responses through a single HTTP request.
func (r *Registry) assertNoStreamHandlers(groupName, call string) {
	group, ok := r.groups[groupName]
	if !ok {
		return
	}
	for _, info := range group.Handlers {
		if info.Kind != HandlerKindUnary {
			panic(fmt.Sprintf(
				"aprot: streaming handler %s.%s cannot be exposed via REST; use WebSocket or SSE (%s)",
				groupName, info.Name, call,
			))
		}
	}
}

func (r *Registry) register(handler any, addToWSDispatch bool, middleware ...Middleware) {
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
			info.registry = r
			if addToWSDispatch {
				if existing, exists := r.handlers[wireMethod]; exists {
					panic(fmt.Sprintf("aprot: duplicate method %s (registered by %s and %s)", wireMethod, existing.StructName, structName))
				}
				r.handlers[wireMethod] = info
			}
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

// IsREST reports whether the named handler group was registered via RegisterREST.
func (r *Registry) IsREST(groupName string) bool {
	return r.restGroups[groupName]
}

// RESTGroups returns the set of handler group names registered via RegisterREST.
func (r *Registry) RESTGroups() map[string]bool {
	return r.restGroups
}

// RegisterPushEventFor registers a push event associated with a specific handler.
// The handler must have been previously registered via Register().
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

	group, ok := r.groups[structName]
	if !ok {
		panic("aprot: RegisterPushEventFor called with unregistered handler: " + structName)
	}

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

	group.PushEvents = append(group.PushEvents, event)
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
// Panics if the type was not registered via RegisterPushEventFor.
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

// RegisterEnumFor registers an enum type associated with a handler group for TypeScript generation.
// The enum will be generated in the handler's TypeScript file.
// Pass the handler instance (same one used in Register) and the result of the Values() function.
//
// Example:
//
//	handler := &MyHandlers{}
//	registry.Register(handler)
//	registry.RegisterEnumFor(handler, StrStateValues())   // string-based
//	registry.RegisterEnumFor(handler, IntStatusValues())  // int-based with Stringer
func (r *Registry) RegisterEnumFor(handler any, values any) {
	// Resolve handler to group
	ht := reflect.TypeOf(handler)
	if ht.Kind() == reflect.Ptr {
		ht = ht.Elem()
	}
	structName := ht.Name()

	group, ok := r.groups[structName]
	if !ok {
		panic("aprot: RegisterEnumFor called with unregistered handler: " + structName)
	}

	enumInfo := r.buildEnumInfo(values)
	group.Enums = append(group.Enums, enumInfo)
	r.enumTypes[enumInfo.Type] = &group.Enums[len(group.Enums)-1]
}

// RegisterEnum registers an enum type that is not tied to any handler group.
// The enum will be generated in a shared TypeScript file, importable by all handler files.
// Pass the result of a Values() function (e.g. EventTypeValues()).
//
// Example:
//
//	registry.RegisterEnum(EventTypeValues())
//	registry.RegisterEnum(RSVPStatusValues())
func (r *Registry) RegisterEnum(values any) {
	enumInfo := r.buildEnumInfo(values)
	r.sharedEnums = append(r.sharedEnums, enumInfo)
	r.enumTypes[enumInfo.Type] = &r.sharedEnums[len(r.sharedEnums)-1]
}

// buildEnumInfo creates an EnumInfo from a slice of enum values.
func (r *Registry) buildEnumInfo(values any) EnumInfo {
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

	return enumInfo
}

// GetEnum returns the EnumInfo for a registered enum type, or nil if not registered.
func (r *Registry) GetEnum(t reflect.Type) *EnumInfo {
	return r.enumTypes[t]
}

// Enums returns all registered enum types across all handler groups and shared enums.
func (r *Registry) Enums() []EnumInfo {
	var all []EnumInfo
	for _, group := range r.groups {
		all = append(all, group.Enums...)
	}
	all = append(all, r.sharedEnums...)
	return all
}

// SharedEnums returns enum types registered via RegisterEnum (not tied to any handler group).
func (r *Registry) SharedEnums() []EnumInfo {
	return r.sharedEnums
}

// SetValidator sets the struct validator used for automatic parameter validation.
// When set, struct parameters are validated before handler dispatch.
// Pass nil to disable validation.
//
// Example:
//
//	registry.SetValidator(aprot.NewPlaygroundValidator())
func (r *Registry) SetValidator(v StructValidator) {
	r.validator = v
}

// OnGenerate registers a hook called after code generation.
// The hook receives the results map (filename → content) and the output mode.
// Hooks can modify existing entries or add new files.
func (r *Registry) OnGenerate(hook func(results map[string]string, mode OutputMode)) {
	r.generateHooks = append(r.generateHooks, hook)
}

// OnServerInit registers a hook called during NewServer after the server is
// constructed but before the run loop starts. This lets subpackages like
// tasks/ defer server-side setup to server creation time.
func (r *Registry) OnServerInit(hook func(s *Server)) {
	r.serverInitHooks = append(r.serverInitHooks, hook)
}

// GroupMiddleware returns the middleware for a handler group by group name.
// Works for both WS-registered and REST-only handlers.
func (r *Registry) GroupMiddleware(groupName string) []Middleware {
	group, ok := r.groups[groupName]
	if !ok {
		return nil
	}
	return group.middleware
}

// GenerateHooks returns the registered generation hooks.
func (r *Registry) GenerateHooks() []func(results map[string]string, mode OutputMode) {
	return r.generateHooks
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
		// func(...) (T, error) where T is any JSON-serializable type,
		// or func(...) (iter.Seq[T], error) / (iter.Seq2[K,V], error) for streams.
		if !mt.Out(1).Implements(errorType) {
			return nil
		}
		out0 := mt.Out(0)

		// Detect iter.Seq[T] first.
		if elem, ok := isIterSeq(out0); ok {
			et := elem
			if et.Kind() == reflect.Ptr {
				et = et.Elem()
			}
			return &HandlerInfo{
				Name:         method.Name,
				Params:       params,
				ResponseType: et,
				StructName:   structName,
				Kind:         HandlerKindStream,
				method:       handlerValue.Method(method.Index),
				handler:      handlerValue,
			}
		}

		// Detect iter.Seq2[K, V].
		if k, v, ok := isIterSeq2(out0); ok {
			vt := v
			if vt.Kind() == reflect.Ptr {
				vt = vt.Elem()
			}
			return &HandlerInfo{
				Name:          method.Name,
				Params:        params,
				ResponseType:  vt,
				StreamKeyType: k,
				StructName:    structName,
				Kind:          HandlerKindStream2,
				method:        handlerValue.Method(method.Index),
				handler:       handlerValue,
			}
		}

		// Unwrap pointer to store the element type
		rt := out0
		if rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		return &HandlerInfo{
			Name:         method.Name,
			Params:       params,
			ResponseType: rt,
			StructName:   structName,
			method:       handlerValue.Method(method.Index),
			handler:      handlerValue,
		}
	default:
		return nil
	}
}

// buildArgs unmarshals params and validates struct arguments, returning the
// full reflect.Value argument list (ctx first) ready for info.method.Call.
func (info *HandlerInfo) buildArgs(ctx context.Context, params jsontext.Value) ([]reflect.Value, error) {
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

	// Validate struct parameters if a validator is set
	if info.registry != nil && info.registry.validator != nil {
		for i, p := range info.Params {
			pt := p.Type
			if pt.Kind() == reflect.Ptr {
				pt = pt.Elem()
			}
			if pt.Kind() == reflect.Struct {
				val := args[i+1] // +1 because args[0] is ctx
				if val.Kind() == reflect.Ptr {
					val = val.Elem()
				}
				if err := info.registry.validator.ValidateStruct(val.Interface()); err != nil {
					return nil, err
				}
			}
		}
	}

	return args, nil
}

// Call invokes a unary handler with the given context and JSON params and
// returns its (response, error) pair. Streaming handlers must use CallStream.
// Params must be a JSON array (positional arguments) or empty/nil for no-params handlers.
func (info *HandlerInfo) Call(ctx context.Context, params jsontext.Value) (any, error) {
	args, err := info.buildArgs(ctx, params)
	if err != nil {
		return nil, err
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

// CallStream invokes a streaming handler and returns the raw reflect.Value
// of the returned iter.Seq / iter.Seq2. The caller is responsible for
// driving the iterator (typically via reflect.MakeFunc). If the handler
// returns a preflight error it is returned without invoking the iterator.
func (info *HandlerInfo) CallStream(ctx context.Context, params jsontext.Value) (reflect.Value, error) {
	if info.Kind != HandlerKindStream && info.Kind != HandlerKindStream2 {
		return reflect.Value{}, fmt.Errorf("aprot: CallStream on non-stream handler %s.%s", info.StructName, info.Name)
	}
	args, err := info.buildArgs(ctx, params)
	if err != nil {
		return reflect.Value{}, err
	}
	results := info.method.Call(args)
	if errVal := results[1].Interface(); errVal != nil {
		return reflect.Value{}, errVal.(error)
	}
	return results[0], nil
}

// unmarshalParam unmarshals a JSON value into the appropriate reflect.Value for a parameter type.
func unmarshalParam(t reflect.Type, data jsontext.Value) (reflect.Value, error) {
	if t.Kind() == reflect.Ptr {
		// Pointer param: create *T, unmarshal into it, return the pointer
		ptr := reflect.New(t.Elem())
		if err := unmarshalJSON(data, ptr.Interface()); err != nil {
			return reflect.Value{}, err
		}
		return ptr, nil
	}
	// Value param: create *T, unmarshal, return the dereferenced value
	ptr := reflect.New(t)
	if err := unmarshalJSON(data, ptr.Interface()); err != nil {
		return reflect.Value{}, err
	}
	return ptr.Elem(), nil
}
