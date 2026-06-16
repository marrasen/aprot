package aprot

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type ItemResponse struct {
	ID      int     `json:"id"`
	Enabled bool    `json:"enabled"`
	Score   float64 `json:"score"`
}

type TypedParamHandlers struct{}

func (h *TypedParamHandlers) GetItem(ctx context.Context, id int) (*ItemResponse, error) {
	return &ItemResponse{ID: id}, nil
}

func (h *TypedParamHandlers) GetFlag(ctx context.Context, enabled bool) (*ItemResponse, error) {
	return &ItemResponse{Enabled: enabled}, nil
}

func (h *TypedParamHandlers) GetScore(ctx context.Context, value float64) (*ItemResponse, error) {
	return &ItemResponse{Score: value}, nil
}

type NullableRESTResponse struct {
	Name sql.NullString `json:"name"`
	Age  sql.NullInt64  `json:"age"`
}

type NullRESTHandlers struct{}

func (h *NullRESTHandlers) GetProfile(ctx context.Context) (*NullableRESTResponse, error) {
	return &NullableRESTResponse{
		Name: sql.NullString{String: "Alice", Valid: true},
		Age:  sql.NullInt64{Valid: false},
	}, nil
}

func setupTypedParamServer(t *testing.T) (*httptest.Server, *RESTAdapter) {
	t.Helper()
	registry := NewRegistry()
	registry.RegisterREST(&TypedParamHandlers{})
	adapter := NewRESTAdapter(registry)
	ts := httptest.NewServer(adapter)
	t.Cleanup(ts.Close)
	return ts, adapter
}

// findRoutePath resolves the route path for a method and substitutes the
// single path parameter with val.
func findRoutePath(t *testing.T, adapter *RESTAdapter, method, val string) string {
	t.Helper()
	for _, r := range adapter.Routes() {
		if r.MethodName == method {
			if len(r.PathParams) != 1 {
				t.Fatalf("%s: expected 1 path param, got %d", method, len(r.PathParams))
			}
			return strings.Replace(r.Path, "{"+r.PathParams[0].Name+"}", val, 1)
		}
	}
	t.Fatalf("route for %s not found", method)
	return ""
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Non-string scalar path params must be converted to their JSON types
// instead of being passed as JSON strings (which the strict decoder rejects).
func TestRESTIntPathParam(t *testing.T) {
	ts, adapter := setupTypedParamServer(t)

	status, body := getBody(t, ts.URL+findRoutePath(t, adapter, "GetItem", "42"))
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", status, body)
	}
	if !strings.Contains(body, `"id":42`) {
		t.Errorf("expected id 42 in body, got %s", body)
	}
}

func TestRESTBoolPathParam(t *testing.T) {
	ts, adapter := setupTypedParamServer(t)

	status, body := getBody(t, ts.URL+findRoutePath(t, adapter, "GetFlag", "true"))
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", status, body)
	}
	if !strings.Contains(body, `"enabled":true`) {
		t.Errorf("expected enabled true in body, got %s", body)
	}
}

func TestRESTFloatPathParam(t *testing.T) {
	ts, adapter := setupTypedParamServer(t)

	status, body := getBody(t, ts.URL+findRoutePath(t, adapter, "GetScore", "2.5"))
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", status, body)
	}
	if !strings.Contains(body, `"score":2.5`) {
		t.Errorf("expected score 2.5 in body, got %s", body)
	}
}

// A non-numeric value for an int path param must be a 400, not a 500.
func TestRESTInvalidIntPathParam(t *testing.T) {
	ts, adapter := setupTypedParamServer(t)

	status, _ := getBody(t, ts.URL+findRoutePath(t, adapter, "GetItem", "abc"))
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", status)
	}
}

// REST responses must use the same sql.Null-aware marshaling as the
// WebSocket/SSE path, so the wire format is identical across transports.
func TestRESTSQLNullResponse(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&NullRESTHandlers{})
	adapter := NewRESTAdapter(registry)
	ts := httptest.NewServer(adapter)
	t.Cleanup(ts.Close)

	var path string
	for _, r := range adapter.Routes() {
		if r.MethodName == "GetProfile" {
			path = r.Path
		}
	}
	if path == "" {
		t.Fatal("GetProfile route not found")
	}

	status, body := getBody(t, ts.URL+path)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", status, body)
	}
	if !strings.Contains(body, `"name":"Alice"`) {
		t.Errorf("expected unwrapped null-string, got %s", body)
	}
	if !strings.Contains(body, `"age":null`) {
		t.Errorf("expected null for invalid NullInt64, got %s", body)
	}
	if strings.Contains(body, `"Valid"`) {
		t.Errorf("raw sql.Null struct leaked into REST response: %s", body)
	}
}
