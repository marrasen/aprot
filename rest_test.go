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

func init() {
	// Suppress unused variable warning
	_ = fmt.Sprint
}
