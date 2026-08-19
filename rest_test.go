package aprot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type UserResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// CreateUserReq is the payload accepted by the CreateUser and UpdateUser endpoints.
type CreateUserReq struct {
	// Name is the user's full display name.
	Name string `json:"name"  validate:"required,min=2"`
	// Email is the user's primary contact address.
	Email string `json:"email" validate:"required,email"`
}

type RESTHandlers struct{}

func (h *RESTHandlers) GetUser(ctx context.Context, id string) (*UserResponse, error) {
	return &UserResponse{ID: id, Name: "Alice", Age: 30}, nil
}

// CreateUser provisions a new user account.
//
// The created user is immediately active and can sign in right away.
func (h *RESTHandlers) CreateUser(ctx context.Context, req *CreateUserReq) (*UserResponse, error) {
	return &UserResponse{ID: "new-123", Name: req.Name, Age: 25}, nil
}

func (h *RESTHandlers) UpdateUser(ctx context.Context, id string, req *CreateUserReq) (*UserResponse, error) {
	return &UserResponse{ID: id, Name: req.Name, Age: 25}, nil
}

func (h *RESTHandlers) DeleteUser(ctx context.Context, id string) error {
	return nil
}

func (h *RESTHandlers) ListUsers(ctx context.Context) ([]*UserResponse, error) {
	return []*UserResponse{{ID: "1", Name: "Alice", Age: 30}}, nil
}

func TestRESTAdapter_RouteComputation(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	adapter := NewRESTAdapter(registry)
	routes := adapter.Routes()

	if len(routes) == 0 {
		t.Fatal("expected routes to be computed")
	}

	routeMap := make(map[string]RouteInfo)
	for _, r := range routes {
		routeMap[r.MethodName] = r
	}

	// GetUser -> GET with path param
	if r, ok := routeMap["GetUser"]; ok {
		if r.HTTPMethod != HTTPGet {
			t.Errorf("GetUser: expected GET, got %s", r.HTTPMethod)
		}
		if len(r.PathParams) != 1 {
			t.Errorf("GetUser: expected 1 path param, got %d", len(r.PathParams))
		}
		if !strings.Contains(r.Path, "{id}") {
			t.Errorf("GetUser: expected path to contain {id}, got %s", r.Path)
		}
	} else {
		t.Error("GetUser route not found")
	}

	// CreateUser -> POST with body
	if r, ok := routeMap["CreateUser"]; ok {
		if r.HTTPMethod != HTTPPost {
			t.Errorf("CreateUser: expected POST, got %s", r.HTTPMethod)
		}
		if r.BodyParam == nil {
			t.Error("CreateUser: expected body param")
		}
		if len(r.PathParams) != 0 {
			t.Errorf("CreateUser: expected 0 path params, got %d", len(r.PathParams))
		}
	} else {
		t.Error("CreateUser route not found")
	}

	// UpdateUser -> PUT with path param + body
	if r, ok := routeMap["UpdateUser"]; ok {
		if r.HTTPMethod != HTTPPut {
			t.Errorf("UpdateUser: expected PUT, got %s", r.HTTPMethod)
		}
		if r.BodyParam == nil {
			t.Error("UpdateUser: expected body param")
		}
		if len(r.PathParams) != 1 {
			t.Errorf("UpdateUser: expected 1 path param, got %d", len(r.PathParams))
		}
	} else {
		t.Error("UpdateUser route not found")
	}

	// DeleteUser -> DELETE with path param
	if r, ok := routeMap["DeleteUser"]; ok {
		if r.HTTPMethod != HTTPDelete {
			t.Errorf("DeleteUser: expected DELETE, got %s", r.HTTPMethod)
		}
		if len(r.PathParams) != 1 {
			t.Errorf("DeleteUser: expected 1 path param, got %d", len(r.PathParams))
		}
	} else {
		t.Error("DeleteUser route not found")
	}

	// ListUsers -> GET with no params
	if r, ok := routeMap["ListUsers"]; ok {
		if r.HTTPMethod != HTTPGet {
			t.Errorf("ListUsers: expected GET, got %s", r.HTTPMethod)
		}
		if len(r.PathParams) != 0 {
			t.Errorf("ListUsers: expected 0 path params, got %d", len(r.PathParams))
		}
	} else {
		t.Error("ListUsers route not found")
	}
}

func TestRESTAdapter_GET(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	resp, err := http.Get(server.URL + "/rest-handlers/get-user/abc-123")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var user UserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if user.ID != "abc-123" {
		t.Errorf("expected ID 'abc-123', got %q", user.ID)
	}
	if user.Name != "Alice" {
		t.Errorf("expected Name 'Alice', got %q", user.Name)
	}
}

func TestRESTAdapter_POST(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	body := `{"name": "Bob", "email": "bob@example.com"}`
	resp, err := http.Post(server.URL+"/rest-handlers/create-user", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var user UserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if user.Name != "Bob" {
		t.Errorf("expected Name 'Bob', got %q", user.Name)
	}
}

func TestRESTAdapter_PUT_PathParamAndBody(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	body := `{"name": "Updated", "email": "updated@example.com"}`
	req, _ := http.NewRequest(http.MethodPut, server.URL+"/rest-handlers/update-user/user-42", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var user UserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if user.ID != "user-42" {
		t.Errorf("expected ID 'user-42', got %q", user.ID)
	}
	if user.Name != "Updated" {
		t.Errorf("expected Name 'Updated', got %q", user.Name)
	}
}

func TestRESTAdapter_DELETE(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/rest-handlers/delete-user/user-42", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestRESTAdapter_ValidationError(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	registry.SetValidator(NewPlaygroundValidator())
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	// Missing required fields and invalid email
	body := `{"name": "", "email": "not-an-email"}`
	resp, err := http.Post(server.URL+"/rest-handlers/create-user", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", resp.StatusCode)
	}

	var errResp ErrorMessage
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if errResp.Code != CodeValidationFailed {
		t.Errorf("expected code %d, got %d", CodeValidationFailed, errResp.Code)
	}
}

func TestRESTAdapter_Middleware(t *testing.T) {
	registry := NewRegistry()

	// Register with auth middleware that checks header
	authMW := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			httpReq := HTTPRequestFromContext(ctx)
			if httpReq == nil || httpReq.Header.Get("Authorization") == "" {
				return nil, ErrUnauthorized("missing auth")
			}
			return next(ctx, req)
		}
	}
	registry.RegisterREST(&RESTHandlers{}, authMW)
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	// Without auth header -> 401
	resp, err := http.Get(server.URL + "/rest-handlers/list-users")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// With auth header -> 200
	req, _ := http.NewRequest("GET", server.URL+"/rest-handlers/list-users", nil)
	req.Header.Set("Authorization", "Bearer token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp2.StatusCode)
	}
}

func TestInferHTTPMethod(t *testing.T) {
	tests := []struct {
		method string
		want   HTTPMethod
	}{
		{"GetUser", HTTPGet},
		{"ListUsers", HTTPGet},
		{"FindByEmail", HTTPGet},
		{"CreateUser", HTTPPost},
		{"AddItem", HTTPPost},
		{"UpdateUser", HTTPPut},
		{"SetAge", HTTPPatch},
		{"DeleteUser", HTTPDelete},
		{"RemoveItem", HTTPDelete},
		{"DoSomething", HTTPPost}, // default
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := inferHTTPMethod(tt.method)
			if got != tt.want {
				t.Errorf("inferHTTPMethod(%q) = %s, want %s", tt.method, got, tt.want)
			}
		})
	}
}

func TestHTTPRequestFromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), httpRequestKey{}, &http.Request{})
	r := HTTPRequestFromContext(ctx)
	if r == nil {
		t.Error("expected non-nil request from context")
	}

	// Nil context
	r = HTTPRequestFromContext(context.Background())
	if r != nil {
		t.Error("expected nil request from plain context")
	}
}

func TestRESTAdapter_ListUsers_GET(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	resp, err := http.Get(server.URL + "/rest-handlers/list-users")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var users []UserResponse
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("expected 1 user, got %d", len(users))
	}
}

func TestRESTAdapter_NamingPlugin(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	// Use FixAcronyms naming
	adapter := NewRESTAdapter(registry, WithRESTNaming(DefaultNaming{FixAcronyms: true}))
	routes := adapter.Routes()

	for _, r := range routes {
		if r.MethodName == "GetUser" {
			if !strings.Contains(r.Path, "/rest-handlers/") {
				t.Errorf("expected path with /rest-handlers/, got %s", r.Path)
			}
		}
	}
}

type AdminHandlers struct{}

func (h *AdminHandlers) DeleteEverything(ctx context.Context) error {
	return nil
}

func TestRESTAdapter_RegisterREST(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	registry.Register(&AdminHandlers{}) // not REST

	adapter := NewRESTAdapter(registry)
	routes := adapter.Routes()

	for _, r := range routes {
		if r.GroupName == "AdminHandlers" {
			t.Errorf("AdminHandlers should not be exposed, found route: %s", r.Pattern)
		}
	}

	if len(routes) == 0 {
		t.Fatal("expected at least some routes from RESTHandlers")
	}

	found := false
	for _, r := range routes {
		if r.GroupName == "RESTHandlers" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected RESTHandlers routes to be present")
	}
}

func TestRESTAdapter_NoRESTGroups(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&RESTHandlers{}) // Register, not RegisterREST

	adapter := NewRESTAdapter(registry)
	routes := adapter.Routes()

	if len(routes) != 0 {
		t.Errorf("expected 0 routes when no groups are REST-registered, got %d", len(routes))
	}
}

func TestRegisterREST_NotInWSDispatch(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	// REST-only handlers should NOT be in the WS dispatch map
	_, ok := registry.Get("RESTHandlers.GetUser")
	if ok {
		t.Error("REST-only handler should not be accessible via Get() (WS dispatch)")
	}

	// But should be in groups
	group, ok := registry.Groups()["RESTHandlers"]
	if !ok {
		t.Fatal("REST-only handler should be in groups")
	}
	if _, ok := group.Handlers["GetUser"]; !ok {
		t.Error("GetUser should be in group handlers")
	}
}

func TestEnableREST(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&RESTHandlers{})
	registry.EnableREST(&RESTHandlers{})

	// Should be in WS dispatch
	_, ok := registry.Get("RESTHandlers.GetUser")
	if !ok {
		t.Error("expected handler in WS dispatch")
	}

	// Should also be REST-enabled
	if !registry.IsREST("RESTHandlers") {
		t.Error("expected RESTHandlers to be REST-enabled")
	}

	// REST adapter should serve it
	adapter := NewRESTAdapter(registry)
	if len(adapter.Routes()) == 0 {
		t.Error("expected routes from EnableREST handler")
	}
}

// restRefreshHandlers exposes a subscribable query (WS) and a mutation
// (REST via EnableREST) sharing a trigger key, to verify the REST request
// path drives subscription refreshes like the WS/SSE path does.
type restRefreshHandlers struct{}

type restRefreshList struct {
	Items []string `json:"items"`
}

type restRefreshCreateReq struct {
	Name string `json:"name"`
}

func (restRefreshHandlers) GetItems(ctx context.Context) (*restRefreshList, error) {
	RegisterRefreshTrigger(ctx, "rest-items")
	return &restRefreshList{Items: []string{"a"}}, nil
}

func (restRefreshHandlers) CreateItem(ctx context.Context, req *restRefreshCreateReq) (*restRefreshList, error) {
	TriggerRefresh(ctx, "rest-items")
	return &restRefreshList{Items: []string{"a", req.Name}}, nil
}

// routePathFor returns the adapter path for a method with no path params.
func routePathFor(t *testing.T, adapter *RESTAdapter, methodName string) string {
	t.Helper()
	for _, route := range adapter.Routes() {
		if route.MethodName == methodName {
			return route.Path
		}
	}
	t.Fatalf("route for %s not found", methodName)
	return ""
}

func TestRESTAdapter_TriggerRefresh_RefreshesSubscribers(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&restRefreshHandlers{})
	registry.EnableREST(&restRefreshHandlers{})
	s := NewServer(registry)
	adapter := NewRESTAdapter(registry)

	// Subscribe over a recorded WS-style conn.
	rt := &recordingTransport{}
	c := &Conn{
		transport: rt,
		server:    s,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        1,
	}
	s.requestsWg.Add(1)
	c.handleSubscribe(IncomingMessage{
		Type:   TypeSubscribe,
		ID:     "sub-1",
		Method: "restRefreshHandlers.GetItems",
	})
	msgs := drainMessages(t, rt)
	if len(msgs) != 1 || msgs[0]["type"] != "response" {
		t.Fatalf("subscribe: expected initial response, got %v", msgs)
	}
	n := len(msgs)

	// Mutate over REST: the handler's TriggerRefresh must reach the
	// WS subscriber.
	path := routePathFor(t, adapter, "CreateItem")
	req := httptest.NewRequest("POST", path, strings.NewReader(`{"name":"b"}`))
	w := httptest.NewRecorder()
	adapter.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("REST call failed: %d %s", w.Code, w.Body.String())
	}
	s.requestsWg.Wait()

	after := drainMessages(t, rt)[n:]
	if len(after) != 1 {
		t.Fatalf("expected 1 refresh frame after REST mutation, got %d: %v", len(after), after)
	}
	if after[0]["type"] != "response" || after[0]["id"] != "sub-1" {
		t.Fatalf("expected refresh response for sub-1, got %v", after[0])
	}
}

func TestRESTAdapter_TriggerRefresh_NoServerNoOp(t *testing.T) {
	// No Server built from this registry: TriggerRefresh must stay a
	// silent no-op over REST instead of panicking.
	registry := NewRegistry()
	registry.RegisterREST(&restRefreshHandlers{})
	adapter := NewRESTAdapter(registry)

	path := routePathFor(t, adapter, "CreateItem")
	req := httptest.NewRequest("POST", path, strings.NewReader(`{"name":"b"}`))
	w := httptest.NewRecorder()
	adapter.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("REST call failed: %d %s", w.Code, w.Body.String())
	}
}

func TestRESTAdapter_ContextValues(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	var gotInfo *HandlerInfo
	var gotReq *Request
	capture := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			gotInfo = HandlerInfoFromContext(ctx)
			gotReq = RequestFromContext(ctx)
			return next(ctx, req)
		}
	}
	adapter := NewRESTAdapter(registry, WithRESTMiddleware(capture))

	req := httptest.NewRequest("GET", "/rest-handlers/get-user/abc-123", nil)
	w := httptest.NewRecorder()
	adapter.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("REST call failed: %d %s", w.Code, w.Body.String())
	}

	if gotInfo == nil {
		t.Fatal("HandlerInfoFromContext returned nil in REST middleware")
	}
	if gotInfo.Name != "GetUser" {
		t.Errorf("handler info name = %q, want GetUser", gotInfo.Name)
	}
	if gotReq == nil {
		t.Fatal("RequestFromContext returned nil in REST middleware")
	}
	if gotReq.Method != "RESTHandlers.GetUser" {
		t.Errorf("request method = %q, want RESTHandlers.GetUser", gotReq.Method)
	}
}

func init() {
	// Suppress unused variable warning
	_ = fmt.Sprint
}

// Regression test for #321: group middleware must run identically whether or
// not a Server is attached to the registry. With a server, dispatch goes
// through Server.invoke (the 0.58.0 seam), which resolved middleware through
// the WS dispatch map and silently dropped it for RegisterREST-only groups.
func TestRESTAdapter_GroupMiddlewareWithAttachedServer(t *testing.T) {
	deny := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			return nil, ErrUnauthorized("authentication required")
		}
	}
	for _, withServer := range []bool{false, true} {
		t.Run(fmt.Sprintf("server=%v", withServer), func(t *testing.T) {
			registry := NewRegistry()
			registry.RegisterREST(&RESTHandlers{}, deny)
			if withServer {
				NewServer(registry)
			}
			adapter := NewRESTAdapter(registry)
			server := httptest.NewServer(adapter)
			defer server.Close()

			resp, err := http.Get(server.URL + "/rest-handlers/list-users")
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("group middleware was skipped: expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

// PanicRESTHandlers is the fixture for #325: a handler panic must become a
// 500 JSON error, not a dropped connection.
type PanicRESTHandlers struct{}

func (PanicRESTHandlers) GetBoom(ctx context.Context) (*UserResponse, error) {
	panic("nil map write")
}

// TestRESTAdapter_HandlerPanic covers both dispatch paths: the serverless
// adapter-owned chain and the Server.invoke seam (#325). Before the fix the
// panic unwound into net/http, which dropped the connection.
func TestRESTAdapter_HandlerPanic(t *testing.T) {
	for _, withServer := range []bool{false, true} {
		t.Run(fmt.Sprintf("server=%v", withServer), func(t *testing.T) {
			registry := NewRegistry()
			registry.RegisterREST(&PanicRESTHandlers{})
			if withServer {
				NewServer(registry)
			}
			adapter := NewRESTAdapter(registry)
			server := httptest.NewServer(adapter)
			defer server.Close()

			resp, err := http.Get(server.URL + "/panic-rest-handlers/get-boom")
			if err != nil {
				t.Fatalf("GET failed (panic dropped the connection?): %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusInternalServerError {
				t.Errorf("expected 500, got %d", resp.StatusCode)
			}
			var errResp ErrorMessage
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			if errResp.Code != CodeInternalError {
				t.Errorf("code = %d, want CodeInternalError", errResp.Code)
			}
			if errResp.Message != "handler panicked" {
				t.Errorf("message = %q, want %q", errResp.Message, "handler panicked")
			}
			if strings.Contains(errResp.Message, "nil map write") {
				t.Errorf("panic value leaked to the client: %q", errResp.Message)
			}
		})
	}
}

// panicMarshalResult panics in its custom marshaler — response marshaling
// runs after the handler-level recovers, so it needs its own coverage.
type panicMarshalResult struct{}

func (panicMarshalResult) MarshalJSON() ([]byte, error) {
	panic("marshal boom: secret-dsn")
}

func (PanicRESTHandlers) GetWeird(ctx context.Context) (*panicMarshalResult, error) {
	return &panicMarshalResult{}, nil
}

// TestRESTAdapter_ResponseMarshalPanic: a panic in a result type's custom
// MarshalJSON must still be a 500 JSON error, not a dropped connection
// (#325 review finding 1).
func TestRESTAdapter_ResponseMarshalPanic(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&PanicRESTHandlers{})
	adapter := NewRESTAdapter(registry)
	server := httptest.NewServer(adapter)
	defer server.Close()

	resp, err := http.Get(server.URL + "/panic-rest-handlers/get-weird")
	if err != nil {
		t.Fatalf("GET failed (panic dropped the connection?): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
	var errResp ErrorMessage
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if errResp.Code != CodeInternalError {
		t.Errorf("code = %d, want CodeInternalError", errResp.Code)
	}
	if strings.Contains(errResp.Message, "secret-dsn") {
		t.Errorf("panic value leaked to the client: %q", errResp.Message)
	}
}

func (PanicRESTHandlers) GetAbort(ctx context.Context) (*UserResponse, error) {
	panic(http.ErrAbortHandler)
}

// TestRESTAdapter_ErrAbortHandlerPropagates: net/http's abort sentinel must
// keep its stdlib meaning — tear the connection down quietly — instead of
// being converted into a 500 by the panic recovers. Covered in both
// serverless and attached-server modes, since each adds its own recover.
func TestRESTAdapter_ErrAbortHandlerPropagates(t *testing.T) {
	for _, withServer := range []bool{false, true} {
		t.Run(fmt.Sprintf("server=%v", withServer), func(t *testing.T) {
			registry := NewRegistry()
			registry.RegisterREST(&PanicRESTHandlers{})
			if withServer {
				NewServer(registry)
			}
			adapter := NewRESTAdapter(registry)
			server := httptest.NewServer(adapter)
			defer server.Close()

			resp, err := http.Get(server.URL + "/panic-rest-handlers/get-abort")
			if err == nil {
				defer resp.Body.Close()
				t.Fatalf("expected an aborted connection, got HTTP %d", resp.StatusCode)
			}
		})
	}
}
