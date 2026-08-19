package aprot

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/go-json-experiment/json/jsontext"
)

// invokeTestHandlers exercises the transport-agnostic pipeline seam.
type invokeTestHandlers struct{}

type invokeItemList struct {
	Items []string `json:"items"`
}

func (invokeTestHandlers) GetItems(ctx context.Context) (*invokeItemList, error) {
	RegisterRefreshTrigger(ctx, "invoke-items")
	return &invokeItemList{Items: []string{"a"}}, nil
}

func (invokeTestHandlers) AddItem(ctx context.Context, name string) (*invokeItemList, error) {
	TriggerRefresh(ctx, "invoke-items")
	return &invokeItemList{Items: []string{"a", name}}, nil
}

func (invokeTestHandlers) Fail(ctx context.Context) (*invokeItemList, error) {
	TriggerRefresh(ctx, "invoke-items")
	return nil, errors.New("boom")
}

func TestServerInvoke_HappyPath(t *testing.T) {
	r := NewRegistry()
	r.Register(&invokeTestHandlers{})
	s := NewServer(r)

	result, err := s.Invoke(context.Background(), "invokeTestHandlers.AddItem", jsontext.Value(`["b"]`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	list, ok := result.(*invokeItemList)
	if !ok || len(list.Items) != 2 || list.Items[1] != "b" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestServerInvoke_Errors(t *testing.T) {
	r := NewRegistry()
	r.Register(&invokeTestHandlers{})
	s := NewServer(r)

	_, err := s.Invoke(context.Background(), "nope.Nope", nil)
	var perr *ProtocolError
	if !errors.As(err, &perr) || perr.Code != CodeMethodNotFound {
		t.Fatalf("unknown method: expected CodeMethodNotFound, got %v", err)
	}

	_, err = s.Invoke(context.Background(), "invokeTestHandlers.Fail", nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected handler error, got %v", err)
	}
}

func TestServerInvoke_MiddlewareAndContext(t *testing.T) {
	r := NewRegistry()
	r.Register(&invokeTestHandlers{})

	var sawInfo *HandlerInfo
	var sawReq *Request
	s := NewServer(r)
	s.Use(func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			sawInfo = HandlerInfoFromContext(ctx)
			sawReq = RequestFromContext(ctx)
			return next(ctx, req)
		}
	})

	if _, err := s.Invoke(context.Background(), "invokeTestHandlers.AddItem", jsontext.Value(`["b"]`)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if sawInfo == nil || sawInfo.Name != "AddItem" {
		t.Fatalf("server middleware did not see handler info: %+v", sawInfo)
	}
	if sawReq == nil || sawReq.Method != "invokeTestHandlers.AddItem" {
		t.Fatalf("server middleware did not see request: %+v", sawReq)
	}
}

// TestServerInvoke_FlushesRefreshTriggers: Invoke owns the refresh queue and
// flushes it on success — a WS subscriber sees a refresh from a bare Invoke.
func TestServerInvoke_FlushesRefreshTriggers(t *testing.T) {
	r := NewRegistry()
	r.Register(&invokeTestHandlers{})
	s := NewServer(r)

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
		Method: "invokeTestHandlers.GetItems",
	})
	n := len(drainMessages(t, rt))

	if _, err := s.Invoke(context.Background(), "invokeTestHandlers.AddItem", jsontext.Value(`["b"]`)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	s.requestsWg.Wait()

	after := drainMessages(t, rt)[n:]
	if len(after) != 1 || after[0]["type"] != "response" || after[0]["id"] != "sub-1" {
		t.Fatalf("expected refresh response for sub-1, got %v", after)
	}
}

// TestServerInvoke_ErrorDropsTriggers: triggers queued by a failing handler
// are not flushed, matching the socket dispatch semantics.
func TestServerInvoke_ErrorDropsTriggers(t *testing.T) {
	r := NewRegistry()
	r.Register(&invokeTestHandlers{})
	s := NewServer(r)

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
		Method: "invokeTestHandlers.GetItems",
	})
	n := len(drainMessages(t, rt))

	if _, err := s.Invoke(context.Background(), "invokeTestHandlers.Fail", nil); err == nil {
		t.Fatal("expected handler error")
	}
	s.requestsWg.Wait()

	if after := drainMessages(t, rt)[n:]; len(after) != 0 {
		t.Fatalf("expected no refresh after handler error, got %v", after)
	}
}

func TestDetachedConn(t *testing.T) {
	r := NewRegistry()
	s := NewServer(r)

	conn := s.NewDetachedConn()
	if conn.ID() == 0 {
		t.Error("detached conn should have a unique non-zero ID")
	}

	// Value store works.
	type key struct{}
	conn.Set(key{}, "v")
	if conn.Get(key{}) != "v" {
		t.Error("value store did not round-trip")
	}

	// Identity works without entering the user index.
	conn.SetUserID("u1")
	if conn.UserID() != "u1" {
		t.Errorf("UserID = %q, want u1", conn.UserID())
	}
	s.mu.Lock()
	_, associated := s.userConns["u1"]
	s.mu.Unlock()
	if associated {
		t.Error("detached conn must not be associated into the server user index")
	}

	// Sends fail with the sentinel instead of panicking.
	if err := conn.sendRaw([]byte("{}")); !errors.Is(err, ErrDetachedConn) {
		t.Errorf("sendRaw error = %v, want ErrDetachedConn", err)
	}

	// WithConnection/Connection round-trip.
	ctx := WithConnection(context.Background(), conn)
	if Connection(ctx) != conn {
		t.Error("Connection(ctx) did not return the attached conn")
	}
}

// SchemaForTestBase is embedded to verify flattening.
type SchemaForTestBase struct {
	ID string `json:"id"`
}

type schemaForTestNode struct {
	SchemaForTestBase
	// Label names the node.
	Label    string              `json:"label" validate:"min=2"`
	Optional *int                `json:"count,omitempty"`
	Children []schemaForTestNode `json:"children"`
}

func TestSchemaFor_InlineStruct(t *testing.T) {
	r := NewRegistry()
	schema := r.SchemaFor(reflect.TypeOf(&schemaForTestNode{}))

	if schema.Type != "object" {
		t.Fatalf("Type = %q, want object", schema.Type)
	}
	if schema.Ref != "" {
		t.Fatalf("inline schema must not use $ref, got %q", schema.Ref)
	}
	if _, ok := schema.Properties["id"]; !ok {
		t.Error("embedded field id not flattened into properties")
	}
	label := schema.Properties["label"]
	if label == nil || label.Type != "string" {
		t.Fatalf("label schema = %+v", label)
	}
	if label.MinLength == nil || *label.MinLength != 2 {
		t.Errorf("validate min=2 not applied: %+v", label)
	}
	// required: id and label and children, not the omitempty pointer.
	req := strings.Join(schema.Required, ",")
	if !strings.Contains(req, "label") || strings.Contains(req, "count") {
		t.Errorf("required = %v", schema.Required)
	}
	// Recursion must terminate: children is an array whose item schema is a
	// plain object placeholder, not an infinite expansion.
	children := schema.Properties["children"]
	if children == nil || children.Type != "array" || children.Items == nil {
		t.Fatalf("children schema = %+v", children)
	}
	if children.Items.Type != "object" {
		t.Errorf("recursive item type = %q, want object", children.Items.Type)
	}
}

func TestGenerateSourceDocsGo(t *testing.T) {
	r := NewRegistry()
	r.RegisterREST(&RESTHandlers{})

	src, err := GenerateSourceDocsGo(r, "api")
	if err != nil {
		t.Fatalf("GenerateSourceDocsGo: %v", err)
	}
	code := string(src)
	if !strings.Contains(code, "CreateUser provisions a new user account.") {
		t.Error("handler godoc missing from generated docs")
	}
	if !strings.Contains(code, `"Email is the user's primary contact address.`) {
		t.Error("field godoc missing from generated docs")
	}
	// Emitted file must parse as Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "docs.go", src, 0); err != nil {
		t.Fatalf("generated docs do not parse: %v\n%s", err, code)
	}
	// Deterministic output.
	src2, _ := GenerateSourceDocsGo(r, "api")
	if string(src2) != code {
		t.Error("GenerateSourceDocsGo output is not deterministic")
	}
}

func TestSetSourceDocs_UsedInsteadOfAST(t *testing.T) {
	r := NewRegistry()
	r.RegisterREST(&RESTHandlers{})
	r.SetSourceDocs(&SourceDocs{
		Handlers: map[string]map[string]HandlerDoc{
			"RESTHandlers": {
				"GetUser": {ParamNames: []string{"userIdentifier"}, Doc: "Baked doc."},
			},
		},
	})

	// The REST adapter derives path parameter names from docs metadata:
	// baked docs must win over AST extraction.
	adapter := NewRESTAdapter(r)
	var getUserPath string
	for _, route := range adapter.Routes() {
		if route.MethodName == "GetUser" {
			getUserPath = route.Path
		}
	}
	if !strings.Contains(getUserPath, "{userIdentifier}") {
		t.Errorf("baked param name not used: path = %q", getUserPath)
	}
}

func TestEnableMCP_Resolution(t *testing.T) {
	r := NewRegistry()
	r.Register(&RESTHandlers{})
	r.EnableMCP(&RESTHandlers{}, MCPOptions{Tools: map[string]MCPTool{
		"CreateUser": {Destructive: false, Idempotent: false},
		"GetUser":    {Name: "fetch_user", Title: "Fetch user", ReadOnly: true},
	}})

	tools := r.MCPTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	// Sorted by name: fetch_user < rest_handlers_create_user
	if tools[0].Name != "fetch_user" || tools[1].Name != "rest_handlers_create_user" {
		t.Fatalf("tool names = %q, %q", tools[0].Name, tools[1].Name)
	}

	create := tools[1]
	if create.Description == "" || !strings.Contains(create.Description, "CreateUser provisions") {
		t.Errorf("description not resolved from godoc: %q", create.Description)
	}
	if !create.SingleStruct || len(create.Params) != 1 || !create.Params[0].Struct {
		t.Errorf("CreateUser should bind as single struct: %+v", create.Params)
	}
	if create.InputSchema == nil || create.InputSchema.Properties["name"] == nil {
		t.Errorf("input schema not expanded inline: %+v", create.InputSchema)
	}

	fetch := tools[0]
	if fetch.SingleStruct || len(fetch.Params) != 1 || fetch.Params[0].Name != "id" {
		t.Errorf("GetUser should bind named primitive id: %+v", fetch.Params)
	}
	if !fetch.ReadOnly || fetch.Destructive {
		t.Errorf("hints not carried: %+v", fetch)
	}
}

func TestEnableMCP_Panics(t *testing.T) {
	r := NewRegistry()
	r.Register(&RESTHandlers{})

	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected panic", name)
			}
		}()
		fn()
	}
	mustPanic("unregistered group", func() {
		r.EnableMCP(&invokeTestHandlers{}, MCPOptions{Tools: map[string]MCPTool{"GetItems": {}}})
	})
	mustPanic("unknown method", func() {
		r.EnableMCP(&RESTHandlers{}, MCPOptions{Tools: map[string]MCPTool{"Nope": {}}})
	})
}

// restOnlyInvokeHandlers is registered via RegisterREST only, so it is
// deliberately absent from the WS dispatch map. Regression fixture for
// #321/#322.
type restOnlyInvokeHandlers struct{}

// Ping returns a greeting.
func (restOnlyInvokeHandlers) Ping(ctx context.Context) (string, error) { return "pong", nil }

// Regression test for #322: Invoke must resolve RegisterREST-only methods,
// and for #321: their group middleware must run when it does.
func TestServerInvoke_RESTOnlyGroup(t *testing.T) {
	var mwCalls int
	mw := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			mwCalls++
			return next(ctx, req)
		}
	}
	r := NewRegistry()
	r.RegisterREST(&restOnlyInvokeHandlers{}, mw)
	s := NewServer(r)

	result, err := s.Invoke(context.Background(), "restOnlyInvokeHandlers.Ping", nil)
	if err != nil {
		t.Fatalf("Invoke on REST-only method: %v", err)
	}
	if result != "pong" {
		t.Fatalf("result = %v, want pong", result)
	}
	if mwCalls != 1 {
		t.Fatalf("group middleware ran %d times, want 1", mwCalls)
	}

	// Socket isolation must survive the widened lookup: Get feeds the WS
	// dispatch and must keep excluding REST-only methods.
	if _, ok := r.Get("restOnlyInvokeHandlers.Ping"); ok {
		t.Fatal("Get must not resolve REST-only methods (socket isolation)")
	}
}
