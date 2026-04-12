package aprot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

// HTTPMethod represents an HTTP method.
type HTTPMethod string

const (
	HTTPGet    HTTPMethod = "GET"
	HTTPPost   HTTPMethod = "POST"
	HTTPPut    HTTPMethod = "PUT"
	HTTPPatch  HTTPMethod = "PATCH"
	HTTPDelete HTTPMethod = "DELETE"
)

// RouteInfo describes one HTTP endpoint derived from a handler method.
type RouteInfo struct {
	HTTPMethod  HTTPMethod
	Pattern     string // e.g., "GET /users/update-user/{id}"
	Path        string // e.g., "/users/update-user/{id}"
	GroupName   string
	MethodName  string
	WireMethod  string // e.g., "Users.UpdateUser"
	PathParams  []routeParam
	BodyParam   *ParamInfo
	HandlerInfo *HandlerInfo
}

type routeParam struct {
	Name string
	Info ParamInfo
}

// RESTOption configures a RESTAdapter.
type RESTOption func(*RESTAdapter)

// WithRESTMiddleware adds middleware to all REST endpoints.
func WithRESTMiddleware(mw ...Middleware) RESTOption {
	return func(a *RESTAdapter) {
		a.middleware = append(a.middleware, mw...)
	}
}

// WithRESTNaming sets the naming plugin for path generation.
func WithRESTNaming(n NamingPlugin) RESTOption {
	return func(a *RESTAdapter) {
		a.naming = n
	}
}

// RESTAdapter serves registered handlers over HTTP/REST.
// Only handlers registered via RegisterREST are exposed.
// It implements http.Handler and can be mounted on any stdlib-compatible router.
type RESTAdapter struct {
	registry   *Registry
	routes     []RouteInfo
	mux        *http.ServeMux
	naming     NamingPlugin
	middleware []Middleware
	meta       *sourceMeta
}

// NewRESTAdapter creates an HTTP/REST adapter from a registry.
// Handlers are mapped to REST endpoints using naming conventions.
func NewRESTAdapter(registry *Registry, opts ...RESTOption) *RESTAdapter {
	a := &RESTAdapter{
		registry: registry,
		mux:      http.NewServeMux(),
		naming:   DefaultNaming{FixAcronyms: true},
	}
	for _, opt := range opts {
		opt(a)
	}

	// Extract AST metadata (parameter names and godoc) for path generation
	dirs := make(map[string]bool)
	for _, group := range registry.Groups() {
		if dir := group.SourceDir(); dir != "" {
			dirs[dir] = true
		}
	}
	a.meta = extractSourceMeta(dirs)

	a.buildRoutes()
	a.registerRoutes()
	return a
}

// Routes returns all computed routes for inspection or documentation.
func (a *RESTAdapter) Routes() []RouteInfo {
	return a.routes
}

// ServeHTTP implements http.Handler.
func (a *RESTAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *RESTAdapter) buildRoutes() {
	// Sort group names for deterministic route order
	groupNames := make([]string, 0, len(a.registry.Groups()))
	for name := range a.registry.Groups() {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	for _, groupName := range groupNames {
		if !a.registry.IsREST(groupName) {
			continue
		}
		group := a.registry.Groups()[groupName]
		prefix := a.naming.PathPrefix(groupName)

		// Sort method names for deterministic order
		methodNames := make([]string, 0, len(group.Handlers))
		for name := range group.Handlers {
			methodNames = append(methodNames, name)
		}
		sort.Strings(methodNames)

		for _, methodName := range methodNames {
			info := group.Handlers[methodName]
			httpMethod := inferHTTPMethod(methodName)
			segment := a.naming.PathSegment(methodName)

			// Classify params: primitives -> path params, struct -> body
			var pathParams []routeParam
			var bodyParam *ParamInfo

			astNames := a.meta.paramNames(info.StructName, info.Name)

			for i := range info.Params {
				p := &info.Params[i]
				pt := p.Type
				if pt.Kind() == reflect.Ptr {
					pt = pt.Elem()
				}
				if pt.Kind() == reflect.Struct {
					bodyParam = p
				} else {
					name := fmt.Sprintf("arg%d", i)
					if i < len(astNames) {
						name = astNames[i]
					}
					pathParams = append(pathParams, routeParam{Name: name, Info: *p})
				}
			}

			// Build path: /prefix/segment/{param1}/{param2}
			path := prefix + "/" + segment
			for _, pp := range pathParams {
				path += "/{" + pp.Name + "}"
			}

			pattern := string(httpMethod) + " " + path

			a.routes = append(a.routes, RouteInfo{
				HTTPMethod:  httpMethod,
				Pattern:     pattern,
				Path:        path,
				GroupName:   groupName,
				MethodName:  methodName,
				WireMethod:  groupName + "." + methodName,
				PathParams:  pathParams,
				BodyParam:   bodyParam,
				HandlerInfo: info,
			})
		}
	}
}

func (a *RESTAdapter) registerRoutes() {
	for _, route := range a.routes {
		route := route // capture for closure
		a.mux.HandleFunc(route.Pattern, func(w http.ResponseWriter, r *http.Request) {
			a.handleRequest(w, r, &route)
		})
	}
}

func (a *RESTAdapter) handleRequest(w http.ResponseWriter, r *http.Request, route *RouteInfo) {
	ctx := r.Context()
	ctx = context.WithValue(ctx, httpRequestKey{}, r)

	// Build JSON params array (same format as WebSocket)
	var jsonParams []any

	// Add path params (in order)
	for _, pp := range route.PathParams {
		val := r.PathValue(pp.Name)
		jsonParams = append(jsonParams, val)
	}

	// Add body param if present
	if route.BodyParam != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, CodeInvalidParams, "failed to read request body")
			return
		}
		if len(body) > 0 {
			// Parse body as raw JSON and append
			jsonParams = append(jsonParams, jsontext.Value(body))
		} else if route.HTTPMethod != HTTPGet {
			writeJSONError(w, http.StatusBadRequest, CodeInvalidParams, "request body is required")
			return
		}
	}

	// Marshal params to JSON array
	var params jsontext.Value
	if len(jsonParams) > 0 {
		var err error
		params, err = marshalJSONParams(jsonParams)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, CodeInvalidParams, "failed to marshal params")
			return
		}
	}

	// Build and execute middleware chain
	handler := a.buildHandler(route.HandlerInfo)
	req := &Request{
		Method: route.WireMethod,
		Params: params,
	}
	result, err := handler(ctx, req)

	if err != nil {
		if perr, ok := err.(*ProtocolError); ok {
			status := protocolErrorToHTTPStatus(perr.Code)
			writeJSONErrorData(w, status, perr.Code, perr.Message, perr.Data)
		} else if code, found := a.registry.LookupError(err); found {
			writeJSONError(w, http.StatusInternalServerError, code, err.Error())
		} else {
			writeJSONError(w, http.StatusInternalServerError, CodeInternalError, err.Error())
		}
		return
	}

	if route.HandlerInfo.IsVoid {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	data, err := json.Marshal(result)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, CodeInternalError, "failed to marshal response")
		return
	}
	_, _ = w.Write(data)
}

// buildHandler builds a middleware chain for a handler method.
func (a *RESTAdapter) buildHandler(info *HandlerInfo) Handler {
	final := func(ctx context.Context, req *Request) (any, error) {
		return info.Call(ctx, req.Params)
	}

	handler := Handler(final)

	// Apply handler-group middleware
	groupMW := a.registry.GroupMiddleware(info.StructName)
	for i := len(groupMW) - 1; i >= 0; i-- {
		handler = groupMW[i](handler)
	}

	// Apply adapter-level middleware
	for i := len(a.middleware) - 1; i >= 0; i-- {
		handler = a.middleware[i](handler)
	}

	return handler
}

// marshalJSONParams marshals a mixed slice of values into a JSON array.
// jsontext.Value items are included as raw JSON; other values are marshaled.
func marshalJSONParams(params []any) (jsontext.Value, error) {
	var parts []string
	for _, p := range params {
		if raw, ok := p.(jsontext.Value); ok {
			parts = append(parts, string(raw))
		} else {
			data, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			parts = append(parts, string(data))
		}
	}
	return jsontext.Value("[" + strings.Join(parts, ",") + "]"), nil
}

// inferHTTPMethod guesses the HTTP method from the Go method name prefix.
func inferHTTPMethod(methodName string) HTTPMethod {
	prefixes := []struct {
		prefix string
		method HTTPMethod
	}{
		{"Get", HTTPGet},
		{"List", HTTPGet},
		{"Find", HTTPGet},
		{"Create", HTTPPost},
		{"Add", HTTPPost},
		{"Update", HTTPPut},
		{"Set", HTTPPatch},
		{"Delete", HTTPDelete},
		{"Remove", HTTPDelete},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(methodName, p.prefix) {
			return p.method
		}
	}
	return HTTPPost
}

// protocolErrorToHTTPStatus maps protocol error codes to HTTP status codes.
func protocolErrorToHTTPStatus(code int) int {
	switch code {
	case CodeInvalidParams:
		return http.StatusBadRequest
	case CodeValidationFailed:
		return http.StatusUnprocessableEntity
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeMethodNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func writeJSONError(w http.ResponseWriter, status int, code int, message string) {
	writeJSONErrorData(w, status, code, message, nil)
}

func writeJSONErrorData(w http.ResponseWriter, status int, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := ErrorMessage{
		Type:    TypeError,
		Code:    code,
		Message: message,
		Data:    data,
	}
	out, _ := json.Marshal(resp)
	_, _ = w.Write(out)
}

type httpRequestKey struct{}

// HTTPRequestFromContext returns the *http.Request associated with a REST handler call.
// Returns nil if the context is not from a REST request.
func HTTPRequestFromContext(ctx context.Context) *http.Request {
	r, _ := ctx.Value(httpRequestKey{}).(*http.Request)
	return r
}
