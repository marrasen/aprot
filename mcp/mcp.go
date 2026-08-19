// Package mcp serves aprot handlers as MCP (Model Context Protocol) tools
// over the Streamable HTTP transport, so an AI assistant calls the same
// handlers a browser does — through the same [aprot.Server] pipeline, the
// same middleware, and the same auth (issue #316).
//
// Only methods opted in via [aprot.Registry.EnableMCP] are exposed. Dispatch
// goes through [aprot.Server.Invoke], so refresh triggers fired by a tool
// call refresh subscribed WebSocket/SSE clients like any other mutation.
//
//	registry.Register(&TodoHandlers{})
//	registry.EnableMCP(&TodoHandlers{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
//	    "ListTodos":  {ReadOnly: true, Idempotent: true},
//	    "CreateTodo": {Description: "Add a todo item to the list."},
//	}})
//	server := aprot.NewServer(registry)
//	http.Handle("/mcp", mcp.NewAdapter(server, mcp.Options{ServerName: "todos"}))
//
// Each request runs with a detached connection in context (unless the caller
// installed one via [aprot.WithConnection], e.g. from session-aware auth in
// a wrapping http.Handler), so connection-shaped middleware works unchanged.
//
// The adapter is stateless: it implements the JSON-RPC subset MCP requires
// for tool serving (initialize, ping, tools/list, tools/call) over HTTP POST,
// without sessions or server-initiated streams.
package mcp

import (
	"errors"
	"fmt"
	json "github.com/go-json-experiment/json"
	"io"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/marrasen/aprot"
)

// protocolVersion is the newest MCP revision the adapter implements.
// Older revisions are accepted during negotiation: the tool-serving subset
// used here is compatible across them.
const protocolVersion = "2025-06-18"

var supportedVersions = map[string]bool{
	"2025-06-18": true,
	"2025-03-26": true,
	"2024-11-05": true,
}

// Options configures the MCP adapter's server identity.
type Options struct {
	ServerName    string // serverInfo.name; default "aprot"
	ServerTitle   string // serverInfo.title (display name), optional
	ServerVersion string // serverInfo.version; default "0.0.0"
	Instructions  string // optional hints for the model, sent on initialize
}

// Adapter serves MCP over HTTP. It implements http.Handler and can be
// mounted on any stdlib-compatible router.
type Adapter struct {
	server *aprot.Server
	opts   Options
	tools  map[string]aprot.MCPToolInfo // by tool name
	order  []string                     // listing order (sorted by name)
}

// NewAdapter builds an MCP adapter over the server's registry. Tools are
// resolved once, at construction — register handlers and call EnableMCP
// before creating the adapter.
//
// A RegisterMCP group with no tools enabled via EnableMCP panics here: MCP
// is that group's only surface, so a tool-less group is reachable nowhere
// and the registration is a mistake.
func NewAdapter(server *aprot.Server, opts Options) *Adapter {
	if opts.ServerName == "" {
		opts.ServerName = "aprot"
	}
	if opts.ServerVersion == "" {
		opts.ServerVersion = "0.0.0"
	}
	a := &Adapter{
		server: server,
		opts:   opts,
		tools:  make(map[string]aprot.MCPToolInfo),
	}
	reg := server.Registry()
	toolGroups := make(map[string]bool)
	for _, t := range reg.MCPTools() {
		a.tools[t.Name] = t
		a.order = append(a.order, t.Name)
		groupName, _, _ := strings.Cut(t.Method, ".")
		toolGroups[groupName] = true
	}
	var toolless []string
	for name := range reg.Groups() {
		if reg.IsMCPOnly(name) && !toolGroups[name] {
			toolless = append(toolless, name)
		}
	}
	if len(toolless) > 0 {
		sort.Strings(toolless)
		panic("mcp: RegisterMCP group(s) with no tools enabled via EnableMCP: " + strings.Join(toolless, ", "))
	}
	return a
}

// JSON-RPC 2.0 wire types (subset).

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitempty"` // absent for notifications
	Method  string         `json:"method"`
	Params  jsontext.Value `json:"params,omitempty"`
}

// rpcSuccess and rpcFailure are separate shapes because JSON-RPC requires
// exactly one of result/error to be present — and v2 json's omitempty would
// drop an empty-object result (e.g. ping's {}) entirely.
type rpcSuccess struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitempty"`
	Result  any            `json:"result"`
}

type rpcFailure struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitempty"`
	Error   *rpcError      `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// ServeHTTP implements the MCP Streamable HTTP transport for a stateless
// tool server: JSON-RPC messages arrive as HTTP POST bodies and are answered
// with application/json. GET (server-initiated streams) is not supported.
func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, nil, &rpcError{Code: codeParseError, Message: "failed to read request body"})
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// A JSON array is a batch; batching was removed in 2025-06-18 and
		// this adapter does not accept it from older clients either.
		if len(body) > 0 && body[0] == '[' {
			writeError(w, http.StatusBadRequest, nil, &rpcError{Code: codeInvalidRequest, Message: "batch requests are not supported"})
			return
		}
		writeError(w, http.StatusBadRequest, nil, &rpcError{Code: codeParseError, Message: "invalid JSON-RPC message"})
		return
	}

	// Notifications get no response body.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result any
	var rerr *rpcError
	// Handler panics become errors inside Server.Invoke, but adapter code
	// around it — argument binding, result marshaling (including custom
	// MarshalJSON on result types) — runs outside that recover. Catch
	// anything that escapes so the client gets a JSON-RPC error correlated
	// to the request id instead of a dropped connection (#325). The
	// client-facing message is generic; the value and stack go to the log.
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				a.server.Logger().Error("aprot: panic serving MCP request",
					"rpcMethod", req.Method, "panic", rec, "stack", string(debug.Stack()))
				result = nil
				rerr = &rpcError{Code: codeInternalError, Message: "internal error"}
			}
		}()
		switch req.Method {
		case "initialize":
			result = a.initialize(req.Params)
		case "ping":
			result = struct{}{}
		case "tools/list":
			result = a.listTools()
		case "tools/call":
			result, rerr = a.callTool(r, req.Params)
		default:
			rerr = &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
		}
	}()
	if rerr != nil {
		writeError(w, http.StatusOK, req.ID, rerr)
		return
	}
	writeResult(w, req.ID, result)
}

func writeResult(w http.ResponseWriter, id jsontext.Value, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	data, err := json.Marshal(rpcSuccess{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		// The response value is assembled from marshalable parts; a failure
		// here means a tool result carried something unmarshalable, which
		// callTool already guards. Emit a minimal hand-built error.
		data = []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"failed to marshal response"}}`)
	}
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, id jsontext.Value, rerr *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	data, _ := json.Marshal(rpcFailure{JSONRPC: "2.0", ID: id, Error: rerr})
	_, _ = w.Write(data)
}

func (a *Adapter) initialize(params jsontext.Value) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := protocolVersion
	if supportedVersions[p.ProtocolVersion] {
		version = p.ProtocolVersion
	}

	serverInfo := map[string]any{
		"name":    a.opts.ServerName,
		"version": a.opts.ServerVersion,
	}
	if a.opts.ServerTitle != "" {
		serverInfo["title"] = a.opts.ServerTitle
	}
	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      serverInfo,
	}
	if a.opts.Instructions != "" {
		result["instructions"] = a.opts.Instructions
	}
	return result
}

func (a *Adapter) listTools() any {
	tools := make([]map[string]any, 0, len(a.order))
	for _, name := range a.order {
		t := a.tools[name]
		entry := map[string]any{
			"name":        t.Name,
			"inputSchema": t.InputSchema,
			// All four hints are emitted explicitly: the spec's defaults
			// (destructive and open-world true) assume the worst, and a
			// curated EnableMCP registration has stated what is true.
			"annotations": map[string]any{
				"readOnlyHint":    t.ReadOnly,
				"destructiveHint": t.Destructive,
				"idempotentHint":  t.Idempotent,
				"openWorldHint":   t.OpenWorld,
			},
		}
		if t.Title != "" {
			entry["title"] = t.Title
		}
		if t.Description != "" {
			entry["description"] = t.Description
		}
		tools = append(tools, entry)
	}
	return map[string]any{"tools": tools}
}

// callTool dispatches a tools/call request through Server.Invoke. Argument
// problems (unknown tool, missing or malformed arguments) are JSON-RPC
// protocol errors; errors from the handler itself are reported as a tool
// result with isError set, so the model can read them and retry.
func (a *Adapter) callTool(r *http.Request, params jsontext.Value) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments jsontext.Value `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params"}
	}
	tool, ok := a.tools[p.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
	}

	positional, rerr := bindArguments(tool, p.Arguments)
	if rerr != nil {
		return nil, rerr
	}

	ctx := aprot.WithHTTPRequest(r.Context(), r)
	if aprot.Connection(ctx) == nil {
		ctx = aprot.WithConnection(ctx, a.server.NewDetachedConn())
	}

	result, err := a.server.Invoke(ctx, tool.Method, positional)
	if err != nil {
		var perr *aprot.ProtocolError
		if errors.As(err, &perr) && perr.Code == aprot.CodeInvalidParams {
			// Malformed arguments (decode or validation failure) are the
			// caller's fault at the protocol level, mirroring the REST
			// adapter's 400 mapping.
			return nil, &rpcError{Code: codeInvalidParams, Message: perr.Message}
		}
		return toolResult(jsontext.Value(nil), err.Error(), true), nil
	}

	if result == nil {
		return toolResult(nil, "OK", false), nil
	}
	data, err := aprot.MarshalWire(result)
	if err != nil {
		return toolResult(nil, "failed to marshal tool result: "+err.Error(), true), nil
	}
	return toolResult(jsontext.Value(data), string(data), false), nil
}

// toolResult assembles an MCP CallToolResult. structured is included as
// structuredContent when non-nil (2025-06-18); text is always present since
// older clients only read content.
func toolResult(structured jsontext.Value, text string, isErr bool) map[string]any {
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
	if structured != nil {
		res["structuredContent"] = structured
	}
	return res
}

// bindArguments converts the MCP arguments object into the positional JSON
// params array Server.Invoke expects, following the tool's binding plan.
func bindArguments(tool aprot.MCPToolInfo, arguments jsontext.Value) (jsontext.Value, *rpcError) {
	if tool.SingleStruct {
		if len(arguments) == 0 || string(arguments) == "null" {
			if tool.Params[0].Required {
				return nil, &rpcError{Code: codeInvalidParams, Message: "missing arguments"}
			}
			return jsontext.Value("[null]"), nil
		}
		return jsontext.Value("[" + string(arguments) + "]"), nil
	}

	var argMap map[string]jsontext.Value
	if len(arguments) > 0 && string(arguments) != "null" {
		if err := json.Unmarshal(arguments, &argMap); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "arguments must be an object"}
		}
	}

	parts := make([]string, 0, len(tool.Params))
	for _, param := range tool.Params {
		v, ok := argMap[param.Name]
		if !ok {
			if param.Required {
				return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("missing required argument %q", param.Name)}
			}
			v = jsontext.Value("null")
		}
		parts = append(parts, string(v))
	}
	return jsontext.Value("[" + strings.Join(parts, ",") + "]"), nil
}
