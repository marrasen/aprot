package aprot_test

// matrix_paths_test.go holds the dispatch-path half of the invariant matrix
// (#339): the fixture handlers every path executes, and one driver per path
// that runs a call and normalizes the outcome. The invariants live in
// matrix_test.go.
//
// Adding a dispatch path means adding one dispatchPath here — a column —
// not remembering N invariants.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/mcp"
	"github.com/marrasen/aprot/tasks"
)

// --- fixture ---

// probeResult is what every fixture method reports: enough state for any
// invariant to assert on, so one handler serves the whole matrix.
type probeResult struct {
	// Principal is PrincipalFrom(ctx) rendered as a string ("" if anonymous).
	Principal string `json:"principal"`
	// UserID is the address UserID(ctx) resolves.
	UserID string `json:"userId"`
	// HasConn reports whether Connection(ctx) was non-nil.
	HasConn bool `json:"hasConn"`
	// Chain is the middleware that ran, in order.
	Chain []string `json:"chain"`
	// TaskID is set by StartShared.
	TaskID string `json:"taskId"`
}

// probe builds the report from a handler context.
func probe(ctx context.Context) *probeResult {
	p, _ := aprot.PrincipalFrom(ctx).(string)
	return &probeResult{
		Principal: p,
		UserID:    aprot.UserID(ctx),
		HasConn:   aprot.Connection(ctx) != nil,
		Chain:     chainFrom(ctx),
	}
}

// matrixHandlers is the single fixture the whole matrix dispatches through.
type matrixHandlers struct {
	// panicValue is what Boom panics with; tests assert it never reaches a
	// caller.
	panicValue string
	// cancelTaskID is the task CancelShared cancels.
	cancelTaskID atomic.Value // string
	// panicOnRefresh makes SubscribeProbe panic on re-execution, so the
	// server-driven refresh path can be pointed at the panic invariant
	// without a first run that already blew up.
	panicOnRefresh atomic.Bool
}

// Probe reports the execution's state. The unary shape.
func (h *matrixHandlers) Probe(ctx context.Context) (*probeResult, error) {
	return probe(ctx), nil
}

// SubscribeProbe is a subscribable query, so refreshes re-report. It panics
// on re-execution when armed, which is how the refresh column exercises the
// panic invariant.
func (h *matrixHandlers) SubscribeProbe(ctx context.Context) (*probeResult, error) {
	aprot.RegisterRefreshTrigger(ctx, "probe")
	if h.panicOnRefresh.Load() {
		panic(h.panicValue)
	}
	return probe(ctx), nil
}

// Touch fires the refresh trigger SubscribeProbe registered.
func (h *matrixHandlers) Touch(ctx context.Context) (*probeResult, error) {
	aprot.TriggerRefresh(ctx, "probe")
	return probe(ctx), nil
}

// Boom panics. The value must never reach a caller.
func (h *matrixHandlers) Boom(ctx context.Context) (*probeResult, error) {
	panic(h.panicValue)
}

// StartShared starts a shared task that outlives the call and reports its ID.
func (h *matrixHandlers) StartShared(ctx context.Context) (*probeResult, error) {
	_, task := tasks.StartTask[struct{}](context.WithoutCancel(ctx), "matrix-task", tasks.Shared())
	res := probe(ctx)
	res.TaskID = task.ID()
	h.cancelTaskID.Store(task.ID())
	return res, nil
}

// CancelShared cancels the task StartShared created, exercising the cancel
// authorizer on whichever path the call arrived over.
func (h *matrixHandlers) CancelShared(ctx context.Context) (*probeResult, error) {
	id, _ := h.cancelTaskID.Load().(string)
	if err := tasks.CancelSharedTask(ctx, id); err != nil {
		return nil, err
	}
	return probe(ctx), nil
}

// matrixStreams holds the streaming twin of every fixture method the matrix
// dispatches. They live in their own handler group because EnableREST refuses
// a group containing stream handlers, and the unary group has to be reachable
// over REST and MCP. State is shared with the unary group through unary.
type matrixStreams struct {
	unary *matrixHandlers
}

// yieldJSON returns an iterator yielding res as JSON, the shape every
// streaming fixture method reports in.
func yieldJSON(res *probeResult) (iter.Seq[string], error) {
	encoded, _ := json.Marshal(res)
	return func(yield func(string) bool) {
		yield(string(encoded))
	}, nil
}

// StreamProbe reports the execution's state from a streaming handler — the
// dispatch path that bypasses Server.invoke.
func (h *matrixStreams) StreamProbe(ctx context.Context) (iter.Seq[string], error) {
	return yieldJSON(probe(ctx))
}

// StreamBoom panics from inside the iterator, so the streaming column
// exercises the panic invariant on its own dispatch path rather than
// borrowing the unary one.
func (h *matrixStreams) StreamBoom(ctx context.Context) (iter.Seq[string], error) {
	return func(yield func(string) bool) {
		panic(h.unary.panicValue)
	}, nil
}

// StreamTouch fires the refresh trigger from a streaming handler.
func (h *matrixStreams) StreamTouch(ctx context.Context) (iter.Seq[string], error) {
	aprot.TriggerRefresh(ctx, "probe")
	return yieldJSON(probe(ctx))
}

// StreamStartShared starts a shared task from a streaming handler.
func (h *matrixStreams) StreamStartShared(ctx context.Context) (iter.Seq[string], error) {
	_, task := tasks.StartTask[struct{}](context.WithoutCancel(ctx), "matrix-task", tasks.Shared())
	res := probe(ctx)
	res.TaskID = task.ID()
	h.unary.cancelTaskID.Store(task.ID())
	return yieldJSON(res)
}

// StreamCancelShared cancels the shared task from a streaming handler.
func (h *matrixStreams) StreamCancelShared(ctx context.Context) (iter.Seq[string], error) {
	id, _ := h.unary.cancelTaskID.Load().(string)
	if err := tasks.CancelSharedTask(ctx, id); err != nil {
		return nil, err
	}
	return yieldJSON(probe(ctx))
}

// --- middleware chain recording ---

type chainKey struct{}

// recordMW returns middleware that appends name to the execution's chain.
// The chain lives on the context rather than in shared state, so concurrent
// and server-driven executions cannot bleed into each other — a refresh runs
// on the server's schedule, long after the request that triggered it.
func recordMW(name string) aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			prev := chainFrom(ctx)
			next2 := make([]string, len(prev), len(prev)+1)
			copy(next2, prev)
			return next(context.WithValue(ctx, chainKey{}, append(next2, name)), req)
		}
	}
}

func chainFrom(ctx context.Context) []string {
	v, _ := ctx.Value(chainKey{}).([]string)
	return v
}

// --- outcome ---

// outcome is one call's result, normalized across transports so an invariant
// can assert without knowing which path produced it.
type outcome struct {
	// result is the decoded probeResult, nil when the call failed.
	result *probeResult
	// failed reports whether the call was rejected.
	failed bool
	// code is the protocol error code. Zero means the transport does not
	// carry one — MCP reports handler errors as text in the tool result —
	// so assert on it only when non-zero.
	code int
	// message is the caller-visible error text.
	message string
}

func failure(code int, message string) outcome {
	return outcome{failed: true, code: code, message: message}
}

// --- environment ---

// envConfig is what an invariant needs the server built with.
type envConfig struct {
	// principalProvider is registered on socket connections via OnAuth, and
	// on the detached connection the request-scoped drivers install.
	principalProvider aprot.PrincipalProvider
	// wrapperPrincipal, when set, is attached with WithPrincipal by the
	// request-scoped drivers, standing in for an authenticating wrapper.
	wrapperPrincipal string
	// userID is the address socket connections get and request-scoped
	// drivers attach with WithUserID.
	userID string
	// enableTasks turns on the tasks subsystem.
	enableTasks bool
	// cancelAuthorizer, when set, replaces the default cancel policy.
	cancelAuthorizer tasks.CancelAuthorizer
	// panicValue is what Boom panics with.
	panicValue string
}

// matrixEnv is one server reachable through every transport at once, plus a
// second registry with no server for the serverless REST column.
type matrixEnv struct {
	cfg      envConfig
	handlers *matrixHandlers
	server   *aprot.Server
	ts       *httptest.Server
	mcpAd    *mcp.Adapter
	// restAd and serverlessAd are driven in-process rather than over a real
	// HTTP connection: the wrapper context these paths depend on is installed
	// by an http.Handler in front of the adapter, and a context attached to a
	// client-side request never crosses the wire.
	restAd *aprot.RESTAdapter
	// serverlessAd is built from a registry that no Server was ever built
	// from, so registry.attachedServer is nil and the adapter takes its own
	// fallback chain.
	serverlessAd *aprot.RESTAdapter
}

// serverMWName and groupMWName are the two middleware layers the order
// invariant checks for.
const (
	serverMWName = "server"
	groupMWName  = "group"
)

func newMatrixEnv(t *testing.T, cfg envConfig) *matrixEnv {
	t.Helper()
	if cfg.panicValue == "" {
		cfg.panicValue = "secret-panic-value-do-not-leak"
	}
	h := &matrixHandlers{panicValue: cfg.panicValue}
	h.cancelTaskID.Store("")

	streams := &matrixStreams{unary: h}

	registry := aprot.NewRegistry()
	registry.Register(h, recordMW(groupMWName))
	registry.Register(streams, recordMW(groupMWName))
	registry.EnableREST(h)
	if cfg.enableTasks {
		opts := []tasks.EnableOption{}
		if cfg.cancelAuthorizer != nil {
			opts = append(opts, tasks.WithCancelAuthorizer(cfg.cancelAuthorizer))
		}
		tasks.Enable(registry, opts...)
	}
	registry.EnableMCP(h, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"Probe":        {ReadOnly: true},
		"Touch":        {},
		"Boom":         {},
		"StartShared":  {},
		"CancelShared": {},
	}})

	// The panic invariant asserts on what reaches the caller; the server-side
	// log is covered by its own tests, and a stack trace per cell would drown
	// the matrix output.
	server := aprot.NewServer(registry, aprot.ServerOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	server.Use(recordMW(serverMWName))
	server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
		if cfg.userID != "" {
			conn.SetUserID(cfg.userID)
		}
		if cfg.principalProvider != nil {
			conn.SetPrincipalProvider(cfg.principalProvider)
		}
		return nil
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	// Serverless column: a registry no Server was built from.
	slRegistry := aprot.NewRegistry()
	slRegistry.Register(h, recordMW(groupMWName))
	slRegistry.EnableREST(h)

	return &matrixEnv{
		cfg:          cfg,
		handlers:     h,
		server:       server,
		ts:           ts,
		mcpAd:        mcp.NewAdapter(server, mcp.Options{ServerName: "matrix"}),
		restAd:       aprot.NewRESTAdapter(registry),
		serverlessAd: aprot.NewRESTAdapter(slRegistry),
	}
}

// requestCtx builds the context an authenticating wrapper would hand a
// request-scoped adapter: the principal it resolved, the address, and a
// detached connection when the invariant is exercising provider resolution.
func (e *matrixEnv) requestCtx(base context.Context) context.Context {
	ctx := base
	if e.cfg.wrapperPrincipal != "" {
		ctx = aprot.WithPrincipal(ctx, e.cfg.wrapperPrincipal)
	}
	if e.cfg.userID != "" {
		ctx = aprot.WithUserID(ctx, e.cfg.userID)
	}
	if e.cfg.principalProvider != nil {
		conn := e.server.NewDetachedConn()
		conn.SetPrincipalProvider(e.cfg.principalProvider)
		ctx = aprot.WithConnection(ctx, conn)
	}
	return ctx
}

// dial opens a socket and drains the frames the server sends on connect.
func (e *matrixEnv) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	// Config frame, plus the tasks subsystem's on-connect snapshot.
	drain := 1
	if e.cfg.enableTasks {
		drain = 2
	}
	for range drain {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := ws.ReadMessage(); err != nil {
			t.Fatalf("drain connect frames: %v", err)
		}
	}
	return ws
}

// wireFrame is a permissive view of any server frame.
type wireFrame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Result  *probeResult    `json:"result"`
	Item    json.RawMessage `json:"item"`
	Event   string          `json:"event"`
}

// readFrameFor reads frames until one carries id, skipping pushes and
// progress that other subsystems emit.
func readFrameFor(t *testing.T, ws *websocket.Conn, id string) wireFrame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read frame for %q: %v", id, err)
		}
		var f wireFrame
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no frame for %q arrived", id)
	return wireFrame{}
}

// frameOutcome normalizes a socket frame.
func frameOutcome(f wireFrame) outcome {
	if f.Type == string(aprot.TypeError) {
		return failure(f.Code, f.Message)
	}
	return outcome{result: f.Result}
}

// --- path drivers ---

// dispatchPath is one column of the matrix.
type dispatchPath struct {
	name string
	// hasConnection is what Connection(ctx) must report on this path.
	hasConnection bool
	// streaming marks the path that dispatches iterator handlers.
	streaming bool
	// serverBacked is false only for the serverless REST fallback, which has
	// no Server and therefore no server middleware and no refresh machinery.
	serverBacked bool
	// serverDriven marks the refresh path: the server re-runs the handler on
	// its own schedule, with no caller to answer.
	serverDriven bool
	// run executes method on this path and normalizes the outcome.
	run func(t *testing.T, e *matrixEnv, method string) outcome
}

// wireMethod is the socket method name for a unary fixture method.
func wireMethod(method string) string { return "matrixHandlers." + method }

// streamingTwins maps each unary fixture method to the streaming handler that
// does the same thing, so the streaming column exercises its own dispatch
// path. A method with no twin is a loud failure, not a silent fallback.
var streamingTwins = map[string]string{
	"Probe":        "StreamProbe",
	"Boom":         "StreamBoom",
	"Touch":        "StreamTouch",
	"StartShared":  "StreamStartShared",
	"CancelShared": "StreamCancelShared",
}

// restPath is the REST route for a fixture method.
func restPath(method string) string {
	var b strings.Builder
	for i, r := range method {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + 32)
			continue
		}
		b.WriteRune(r)
	}
	return "/matrix-handlers/" + b.String()
}

// mcpTool is the MCP tool name for a fixture method: the same words as the
// REST route, snake_cased.
func mcpTool(method string) string {
	slug := strings.TrimPrefix(restPath(method), "/matrix-handlers/")
	return "matrix_handlers_" + strings.ReplaceAll(slug, "-", "_")
}

// restCall drives a REST adapter in process, through the context an
// authenticating wrapper would have installed. The route is looked up from
// the adapter rather than derived, so a naming change surfaces here as a
// missing route instead of a 404 the invariant would misread.
func restCall(t *testing.T, e *matrixEnv, ad *aprot.RESTAdapter, method string) outcome {
	t.Helper()
	var route *aprot.RouteInfo
	for i, rt := range ad.Routes() {
		if rt.MethodName == method {
			route = &ad.Routes()[i]
			break
		}
	}
	if route == nil {
		t.Fatalf("no REST route for %q", method)
	}

	req := httptest.NewRequest(string(route.HTTPMethod), route.Path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(e.requestCtx(req.Context()))
	w := httptest.NewRecorder()
	ad.ServeHTTP(w, req)

	var raw map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode REST response (HTTP %d): %v\n%s", w.Code, err, w.Body.String())
		}
	}
	if w.Code >= 400 {
		code, _ := raw["code"].(float64)
		msg, _ := raw["message"].(string)
		return failure(int(code), msg)
	}
	body, _ := json.Marshal(raw)
	var res probeResult
	_ = json.Unmarshal(body, &res)
	return outcome{result: &res}
}

var matrixPaths = []dispatchPath{
	{
		name:          "socket-unary",
		hasConnection: true,
		serverBacked:  true,
		run: func(t *testing.T, e *matrixEnv, method string) outcome {
			ws := e.dial(t)
			if err := ws.WriteJSON(aprot.IncomingMessage{Type: aprot.TypeRequest, ID: "m1", Method: wireMethod(method)}); err != nil {
				t.Fatalf("write: %v", err)
			}
			return frameOutcome(readFrameFor(t, ws, "m1"))
		},
	},
	{
		name:          "socket-streaming",
		hasConnection: true,
		streaming:     true,
		serverBacked:  true,
		run: func(t *testing.T, e *matrixEnv, method string) outcome {
			ws := e.dial(t)
			// Dispatch the streaming twin of whichever fixture method the
			// invariant asked for. This column exists to exercise the
			// iterator path, which bypasses Server.invoke, so falling back to
			// the unary method would make the cell a quiet duplicate of the
			// socket-unary column — fail loudly instead.
			twin, ok := streamingTwins[method]
			if !ok {
				t.Fatalf("no streaming twin for %q: add one to matrixStreams, or skip this cell with a reason", method)
			}
			if err := ws.WriteJSON(aprot.IncomingMessage{Type: aprot.TypeRequest, ID: "m1", Method: "matrixStreams." + twin}); err != nil {
				t.Fatalf("write: %v", err)
			}
			f := readFrameFor(t, ws, "m1")
			switch f.Type {
			case string(aprot.TypeError):
				return failure(f.Code, f.Message)
			case string(aprot.TypeStreamEnd):
				return failure(f.Code, f.Message)
			case string(aprot.TypeStreamItem):
				var encoded string
				_ = json.Unmarshal(f.Item, &encoded)
				var res probeResult
				if err := json.Unmarshal([]byte(encoded), &res); err != nil {
					t.Fatalf("decode streamed probe: %v", err)
				}
				return outcome{result: &res}
			}
			t.Fatalf("unexpected frame type %q", f.Type)
			return outcome{}
		},
	},
	{
		name:          "subscribe-first-run",
		hasConnection: true,
		serverBacked:  true,
		run: func(t *testing.T, e *matrixEnv, method string) outcome {
			ws := e.dial(t)
			if method == "Probe" {
				method = "SubscribeProbe"
			}
			if err := ws.WriteJSON(aprot.IncomingMessage{Type: aprot.TypeSubscribe, ID: "m1", Method: wireMethod(method)}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			return frameOutcome(readFrameFor(t, ws, "m1"))
		},
	},
	{
		name:          "refresh",
		hasConnection: true,
		serverBacked:  true,
		serverDriven:  true,
		run: func(t *testing.T, e *matrixEnv, method string) outcome {
			ws := e.dial(t)
			if err := ws.WriteJSON(aprot.IncomingMessage{Type: aprot.TypeSubscribe, ID: "sub", Method: wireMethod("SubscribeProbe")}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			if f := readFrameFor(t, ws, "sub"); f.Type == string(aprot.TypeError) {
				return failure(f.Code, f.Message)
			}
			// The first run succeeded; arm the panic now so it lands on the
			// server-driven re-execution rather than the subscribe.
			if method == "Boom" {
				e.handlers.panicOnRefresh.Store(true)
				defer e.handlers.panicOnRefresh.Store(false)
			}
			// Trigger the server-driven re-execution and read its frame.
			if err := ws.WriteJSON(aprot.IncomingMessage{Type: aprot.TypeRequest, ID: "touch", Method: wireMethod("Touch")}); err != nil {
				t.Fatalf("touch: %v", err)
			}
			return frameOutcome(readFrameFor(t, ws, "sub"))
		},
	},
	{
		name:          "rest-attached",
		hasConnection: false,
		serverBacked:  true,
		run: func(t *testing.T, e *matrixEnv, method string) outcome {
			return restCall(t, e, e.restAd, method)
		},
	},
	{
		name:          "mcp",
		hasConnection: false,
		serverBacked:  true,
		run: func(t *testing.T, e *matrixEnv, method string) outcome {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":%q,"arguments":{}}}`,
				mcpTool(method))
			req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
			req = req.WithContext(e.requestCtx(req.Context()))
			w := httptest.NewRecorder()
			e.mcpAd.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("MCP HTTP %d: %s", w.Code, w.Body.String())
			}
			var resp struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				Result *struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode MCP response: %v\n%s", err, w.Body.String())
			}
			if resp.Error != nil {
				return failure(resp.Error.Code, resp.Error.Message)
			}
			if resp.Result == nil || len(resp.Result.Content) == 0 {
				t.Fatalf("MCP result had no content: %s", w.Body.String())
			}
			text := resp.Result.Content[0].Text
			if resp.Result.IsError {
				// MCP reports handler errors as text, carrying no code.
				return failure(0, text)
			}
			var res probeResult
			if err := json.Unmarshal([]byte(text), &res); err != nil {
				t.Fatalf("decode MCP probe result %q: %v", text, err)
			}
			return outcome{result: &res}
		},
	},
	{
		name:          "rest-serverless",
		hasConnection: false,
		serverBacked:  false,
		run: func(t *testing.T, e *matrixEnv, method string) outcome {
			return restCall(t, e, e.serverlessAd, method)
		},
	},
}
