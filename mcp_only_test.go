package aprot

import (
	"context"
	"encoding/json/jsontext"
	"iter"
	"strings"
	"testing"
)

// mcpOnlyHandlers is registered via RegisterMCP: reachable as MCP tools
// only — no socket dispatch, no REST routes, no generated client.
type mcpOnlyHandlers struct{}

// SearchOrders finds orders matching a query.
func (mcpOnlyHandlers) SearchOrders(ctx context.Context, query string) (string, error) {
	return "orders for " + query, nil
}

type mcpOnlyStreamHandlers struct{}

func (mcpOnlyStreamHandlers) StreamStuff(ctx context.Context) (iter.Seq[int], error) {
	return nil, nil
}

func TestRegisterMCP_Isolation(t *testing.T) {
	var mwCalls int
	mw := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			mwCalls++
			return next(ctx, req)
		}
	}
	r := NewRegistry()
	r.RegisterMCP(&mcpOnlyHandlers{}, mw)

	if !r.IsMCPOnly("mcpOnlyHandlers") {
		t.Error("IsMCPOnly should report true for a RegisterMCP group")
	}
	if r.IsREST("mcpOnlyHandlers") {
		t.Error("RegisterMCP group must not be a REST group")
	}
	if _, ok := r.Get("mcpOnlyHandlers.SearchOrders"); ok {
		t.Error("RegisterMCP methods must stay out of the WS dispatch map")
	}
	if routes := NewRESTAdapter(r).Routes(); len(routes) != 0 {
		t.Errorf("RegisterMCP group must get no REST routes, got %d", len(routes))
	}

	spec, err := NewOpenAPIGenerator(r, "t", "1").Generate()
	if err != nil {
		t.Fatalf("OpenAPI generate: %v", err)
	}
	if len(spec.Paths) != 0 {
		t.Errorf("RegisterMCP group must not appear in OpenAPI, got %d paths", len(spec.Paths))
	}

	// The one surface that must work: the shared pipeline seam, with group
	// middleware applied.
	s := NewServer(r)
	res, err := s.Invoke(context.Background(), "mcpOnlyHandlers.SearchOrders", jsontext.Value(`["boots"]`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res != "orders for boots" {
		t.Errorf("result = %v", res)
	}
	if mwCalls != 1 {
		t.Errorf("group middleware ran %d times, want 1", mwCalls)
	}
}

func TestRegisterMCP_StreamHandlerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterMCP with a streaming handler must panic")
		}
	}()
	r := NewRegistry()
	r.RegisterMCP(&mcpOnlyStreamHandlers{})
}

// The TypeScript client must contain socket-reachable groups only:
// RegisterREST-only and RegisterMCP groups get client functions that call
// over the socket, where their methods deliberately do not exist.
func TestGenerate_SkipsNonSocketGroups(t *testing.T) {
	r := NewRegistry()
	r.Register(&PublicTestHandler{})
	r.RegisterREST(&RESTHandlers{})
	r.RegisterMCP(&mcpOnlyHandlers{})

	files, err := NewGenerator(r).Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := files["public-test-handler.ts"]; !ok {
		t.Errorf("socket group missing from output; files: %v", fileNames(files))
	}
	for name, content := range files {
		if strings.Contains(name, "r-e-s-t-handlers") || strings.Contains(name, "mcp-only") {
			t.Errorf("non-socket group emitted: %s", name)
		}
		if strings.Contains(content, "RESTHandlers.") || strings.Contains(content, "mcpOnlyHandlers.") {
			t.Errorf("non-socket wire method referenced in %s", name)
		}
	}

	// Single-file mode: same rule.
	var sb strings.Builder
	if err := NewGenerator(r).GenerateTo(&sb); err != nil {
		t.Fatalf("GenerateTo: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "PublicTestHandler.Echo") {
		t.Error("socket method missing from single-file output")
	}
	if strings.Contains(out, "RESTHandlers.") || strings.Contains(out, "mcpOnlyHandlers.") {
		t.Error("non-socket wire method in single-file output")
	}
	// Types reachable only from non-socket groups must not be emitted.
	if strings.Contains(out, "UserResponse") {
		t.Error("REST-only group's types leaked into single-file output")
	}
}

// Register + EnableREST groups stay socket-reachable and stay in the client.
func TestGenerate_KeepsEnableRESTGroups(t *testing.T) {
	r := NewRegistry()
	r.Register(&RESTHandlers{})
	r.EnableREST(&RESTHandlers{})

	files, err := NewGenerator(r).Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := files["r-e-s-t-handlers.ts"]; !ok {
		t.Errorf("EnableREST group must stay in the client; files: %v", fileNames(files))
	}
}

func fileNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	return names
}
