package aprot_test

// matrix_test.go is the invariant half of the invariant × dispatch-path
// matrix (#339). Every incident on 2026-08-19 — #321, #322, #325, #326, and
// the two found in pre-release review, #335 and #337 — was the same failure:
// an invariant installed at one dispatch seam and silently missing at
// another. This suite makes that class a CI failure.
//
// Adding a cross-path invariant means adding one invariant here — a row.
// Adding a dispatch path means adding one dispatchPath in
// matrix_paths_test.go — a column. Neither requires remembering the other.
//
// A cell that cannot hold must be an explicit skip carrying a reason, never
// an absent entry: TestInvariantMatrixIsComplete asserts every cell is either
// checked or skipped-with-a-reason, and the skip reasons are printed, so a
// gap is a visible decision rather than silence.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// invariant is one row of the matrix.
type invariant struct {
	name string
	// skip returns the reason this invariant cannot be asserted on p, or ""
	// when it can. A non-empty reason is reported, not hidden.
	skip func(p dispatchPath) string
	// check asserts the invariant on p.
	check func(t *testing.T, p dispatchPath)
}

// mustResult fails the test unless the call succeeded, and returns its
// result.
func mustResult(t *testing.T, o outcome) *probeResult {
	t.Helper()
	if o.failed {
		t.Fatalf("call failed: code=%d %s", o.code, o.message)
	}
	if o.result == nil {
		t.Fatal("call succeeded but carried no result")
	}
	return o.result
}

// never is the skip function for invariants that hold on every path.
func never(dispatchPath) string { return "" }

// --- rows ---

var matrixInvariants = []invariant{
	{
		// #321, #322: middleware installed at one seam and skipped at
		// another admitted unauthenticated calls.
		name: "middleware-runs-in-order",
		skip: never,
		check: func(t *testing.T, p dispatchPath) {
			e := newMatrixEnv(t, envConfig{})
			got := mustResult(t, p.run(t, e, "Probe")).Chain
			want := []string{serverMWName, groupMWName}
			if !p.serverBacked {
				// Asserted difference, not a gap: without a Server there is
				// no server middleware to run. Group middleware still must.
				want = []string{groupMWName}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("middleware chain = %v, want %v", got, want)
			}
		},
	},
	{
		// #325, #327: a handler panic must reach the caller as a generic
		// error, with the value confined to the server log, and must not
		// take down the connection or the process.
		name: "panic-is-generic-and-contained",
		skip: never,
		check: func(t *testing.T, p dispatchPath) {
			const secret = "panic-value-with-a-token-in-it"
			e := newMatrixEnv(t, envConfig{panicValue: secret})

			o := p.run(t, e, "Boom")
			if !o.failed {
				t.Fatalf("panicking handler did not fail the call: %+v", o.result)
			}
			if strings.Contains(o.message, secret) {
				t.Errorf("panic value leaked to the caller: %q", o.message)
			}
			if !strings.Contains(o.message, "handler panicked") {
				t.Errorf("message = %q, want it to report a generic panic", o.message)
			}
			if o.code != 0 && o.code != aprot.CodeInternalError {
				t.Errorf("code = %d, want CodeInternalError (%d)", o.code, aprot.CodeInternalError)
			}

			// The server survived: a later call on the same path still works.
			res := mustResult(t, p.run(t, e, "Probe"))
			if res == nil {
				t.Error("server did not survive the panic")
			}
		},
	},
	{
		// #330: a wrapper's principal must be visible on every path that can
		// carry one.
		name: "principal-from-wrapper",
		skip: func(p dispatchPath) string {
			if p.hasConnection {
				return "sockets have no wrapping http.Handler; they populate the principal with SetPrincipalProvider, covered by principal-from-provider"
			}
			return ""
		},
		check: func(t *testing.T, p dispatchPath) {
			e := newMatrixEnv(t, envConfig{wrapperPrincipal: "alice"})
			if got := mustResult(t, p.run(t, e, "Probe")).Principal; got != "alice" {
				t.Errorf("principal = %q, want alice", got)
			}
		},
	},
	{
		// #330, #337: a connection's provider must be resolved on every path
		// that dispatches through a connection — including the request-scoped
		// ones, where a wrapper installs a detached connection.
		name: "principal-from-provider",
		skip: func(p dispatchPath) string {
			if !p.serverBacked {
				return "no Server, so no Server.invoke seam to resolve a provider; a serverless deployment attaches the principal with WithPrincipal, covered by principal-from-wrapper"
			}
			return ""
		},
		check: func(t *testing.T, p dispatchPath) {
			var calls atomic.Int64
			e := newMatrixEnv(t, envConfig{
				principalProvider: func(ctx context.Context) (any, error) {
					calls.Add(1)
					return "alice", nil
				},
			})
			if got := mustResult(t, p.run(t, e, "Probe")).Principal; got != "alice" {
				t.Errorf("principal = %q, want alice", got)
			}
			if calls.Load() == 0 {
				t.Error("the connection's principal provider never ran")
			}
		},
	},
	{
		// #330, #337: a provider error rejects the execution with its own
		// wire code, rather than running the handler anonymously.
		name: "principal-provider-error-rejects",
		skip: func(p dispatchPath) string {
			if !p.serverBacked {
				return "no Server, so no provider is resolved on this path; there is no provider error to map"
			}
			if p.serverDriven {
				// An always-erroring provider would fail the driver's initial
				// subscribe, so the cell would silently assert against the
				// subscribe-first-run seam instead of the refresh. Arming the
				// error after the first run (the panicOnRefresh pattern) does
				// not work either: the Touch request that triggers the refresh
				// resolves the same provider and would fail first. The refresh
				// outcome of a provider error is refresh-specific anyway — an
				// error frame and a surviving subscription that re-resolves —
				// which this generic rejection assertion cannot express.
				return "an always-erroring provider fails the driver's subscribe before any refresh runs; the refresh-specific outcome (error frame, subscription survives and re-resolves) is asserted by TestPrincipal_ProviderErrorOnRefreshKeepsSubscription"
			}
			return ""
		},
		check: func(t *testing.T, p dispatchPath) {
			e := newMatrixEnv(t, envConfig{
				principalProvider: func(ctx context.Context) (any, error) {
					return nil, aprot.ErrUnauthorized("token expired")
				},
			})
			o := p.run(t, e, "Probe")
			if !o.failed {
				t.Fatalf("provider error did not reject the execution: %+v", o.result)
			}
			if !strings.Contains(o.message, "token expired") {
				t.Errorf("message = %q, want the provider's error", o.message)
			}
			if o.code != 0 && o.code != aprot.CodeUnauthorized {
				t.Errorf("code = %d, want CodeUnauthorized (%d)", o.code, aprot.CodeUnauthorized)
			}
		},
	},
	{
		// #326, #329: connection presence is a transport fact. It is non-nil
		// on sockets, nil on request-scoped paths, and nothing fakes it.
		name: "connection-presence-is-honest",
		skip: never,
		check: func(t *testing.T, p dispatchPath) {
			// No provider configured, so no detached connection is installed
			// — this is the default request-scoped shape.
			e := newMatrixEnv(t, envConfig{})
			got := mustResult(t, p.run(t, e, "Probe")).HasConn
			if got != p.hasConnection {
				t.Errorf("Connection(ctx) != nil = %v, want %v", got, p.hasConnection)
			}
		},
	},
	{
		// #336: the fan-out address is readable uniformly.
		name: "address-is-present-when-supplied",
		skip: never,
		check: func(t *testing.T, p dispatchPath) {
			e := newMatrixEnv(t, envConfig{userID: "u1"})
			if got := mustResult(t, p.run(t, e, "Probe")).UserID; got != "u1" {
				t.Errorf("UserID(ctx) = %q, want u1", got)
			}
		},
	},
	{
		// #335: a shared task registers with the server's manager on every
		// path, is visible to socket watchers, and its cancel runs the
		// authorizer.
		name: "shared-task-registers-and-authorizes-cancel",
		skip: func(p dispatchPath) string {
			if !p.serverBacked {
				return "no Server, so no task manager is ever installed; StartTask degrades to the documented no-op"
			}
			if p.serverDriven {
				return "the subscribable fixture method is deliberately side-effect free, so there is no task-starting refresh to observe; a task started from a refresh uses the same seam as subscribe-first-run, which is checked"
			}
			return ""
		},
		check: func(t *testing.T, p dispatchPath) {
			var authorized atomic.Int64
			e := newMatrixEnv(t, envConfig{
				enableTasks: true,
				userID:      "u1",
				cancelAuthorizer: func(ctx context.Context, task tasks.TaskCancelInfo) error {
					authorized.Add(1)
					return nil
				},
			})

			taskID := mustResult(t, p.run(t, e, "StartShared")).TaskID
			if taskID == "" {
				t.Fatal("no task ID came back — the task degraded to a detached no-op")
			}

			// Registered with the manager and visible to a socket watcher.
			if !socketSeesTask(t, e, taskID) {
				t.Errorf("task %s is not visible to a socket client", taskID)
			}

			// Cancelling over the same path runs the authorizer.
			if o := p.run(t, e, "CancelShared"); o.failed {
				t.Fatalf("cancel failed: code=%d %s", o.code, o.message)
			}
			if authorized.Load() == 0 {
				t.Error("the cancel authorizer never ran")
			}
		},
	},
	{
		// #316: a mutation's refresh trigger reaches subscribers, whichever
		// path the mutation arrived on.
		name: "refresh-trigger-reaches-subscribers",
		skip: func(p dispatchPath) string {
			if !p.serverBacked {
				return "no Server, so there are no subscriptions and no refresh queue; TriggerRefresh is a documented no-op on this path"
			}
			if p.serverDriven {
				return "this path is itself a server-driven refresh; a refresh that triggers a refresh is the cascade the subscription path deliberately prevents"
			}
			return ""
		},
		check: func(t *testing.T, p dispatchPath) {
			e := newMatrixEnv(t, envConfig{})

			// A socket subscriber, watching from another connection.
			ws := e.dial(t)
			if err := ws.WriteJSON(aprot.IncomingMessage{
				Type: aprot.TypeSubscribe, ID: "watch", Method: wireMethod("SubscribeProbe"),
			}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			if f := readFrameFor(t, ws, "watch"); f.Type == string(aprot.TypeError) {
				t.Fatalf("subscribe rejected: %d %s", f.Code, f.Message)
			}

			// The mutation arrives over the path under test.
			if o := p.run(t, e, "Touch"); o.failed {
				t.Fatalf("Touch failed: code=%d %s", o.code, o.message)
			}

			// The subscriber must be refreshed.
			if f := readFrameFor(t, ws, "watch"); f.Type == string(aprot.TypeError) {
				t.Fatalf("refresh frame was an error: %d %s", f.Code, f.Message)
			}
		},
	},
}

// socketSeesTask reports whether a socket client can see taskID in the shared
// task list — the property that makes a task started over MCP or REST usable
// by a browser UI.
func socketSeesTask(t *testing.T, e *matrixEnv, taskID string) bool {
	t.Helper()
	ws := e.dial(t)
	if err := ws.WriteJSON(aprot.IncomingMessage{
		Type: aprot.TypeRequest, ID: "list", Method: "tasksHandler.ListTasks",
	}); err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read ListTasks response: %v", err)
		}
		var f struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Result []struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		if err := json.Unmarshal(data, &f); err != nil || f.ID != "list" {
			continue
		}
		if f.Type == string(aprot.TypeError) {
			t.Fatalf("ListTasks failed: %s", data)
		}
		for _, task := range f.Result {
			if task.ID == taskID {
				return true
			}
		}
		return false
	}
	t.Fatal("no ListTasks response arrived")
	return false
}

// TestInvariantMatrix runs every invariant on every dispatch path.
func TestInvariantMatrix(t *testing.T) {
	for _, inv := range matrixInvariants {
		for _, p := range matrixPaths {
			t.Run(inv.name+"/"+p.name, func(t *testing.T) {
				if reason := inv.skip(p); reason != "" {
					t.Skip("cannot hold on this path: " + reason)
				}
				inv.check(t, p)
			})
		}
	}
}

// TestInvariantMatrixIsComplete asserts the table has no silent gaps: every
// cell is either checked or carries a skip reason, every invariant has a name
// and a check, and every path has a driver. Without this, deleting a check
// or a column would quietly shrink coverage.
func TestInvariantMatrixIsComplete(t *testing.T) {
	if len(matrixInvariants) == 0 || len(matrixPaths) == 0 {
		t.Fatal("the matrix is empty")
	}

	seenPaths := map[string]bool{}
	for _, p := range matrixPaths {
		if p.name == "" {
			t.Error("a dispatch path has no name")
		}
		if seenPaths[p.name] {
			t.Errorf("duplicate dispatch path %q", p.name)
		}
		seenPaths[p.name] = true
		if p.run == nil {
			t.Errorf("dispatch path %q has no driver", p.name)
		}
	}

	seenInvariants := map[string]bool{}
	skipped := 0
	for _, inv := range matrixInvariants {
		if inv.name == "" {
			t.Error("an invariant has no name")
		}
		if seenInvariants[inv.name] {
			t.Errorf("duplicate invariant %q", inv.name)
		}
		seenInvariants[inv.name] = true
		if inv.check == nil {
			t.Errorf("invariant %q has no check", inv.name)
		}
		if inv.skip == nil {
			t.Fatalf("invariant %q has no skip function: every cell must be a decision", inv.name)
		}
		for _, p := range matrixPaths {
			if reason := inv.skip(p); reason != "" {
				skipped++
				t.Logf("skip %s/%s: %s", inv.name, p.name, reason)
			}
		}
	}

	total := len(matrixInvariants) * len(matrixPaths)
	t.Logf("matrix: %d invariants x %d paths = %d cells, %d checked, %d skipped with a reason",
		len(matrixInvariants), len(matrixPaths), total, total-skipped, skipped)
}
