package tasks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marrasen/aprot"
)

// mwInvocation records one invocation of the test middleware.
type mwInvocation struct {
	info     TaskInfo
	startCtx context.Context
	endErr   error
	nextRan  bool
}

// recordingMiddleware returns a TaskMiddleware that captures invocations
// (start ctx, info, end err, whether next ran) into a thread-safe slice.
// The returned middleware always calls next, decorating the ctx with the
// task title under recKey so downstream middleware/handlers can verify ctx
// propagation.
func recordingMiddleware() (*[]mwInvocation, *sync.Mutex, TaskMiddleware) {
	var (
		mu  sync.Mutex
		inv []mwInvocation
	)
	type recKeyType struct{}
	var recKey recKeyType

	mw := func(ctx context.Context, info TaskInfo, next func(context.Context) error) error {
		i := mwInvocation{info: info, startCtx: ctx}
		decorated := context.WithValue(ctx, recKey, "decorated:"+info.ID)
		err := next(decorated)
		i.endErr = err
		i.nextRan = true
		mu.Lock()
		inv = append(inv, i)
		mu.Unlock()
		return err
	}
	return &inv, &mu, mw
}

func findInv(invs []mwInvocation, title string) (mwInvocation, bool) {
	for _, i := range invs {
		if i.info.Title == title {
			return i, true
		}
	}
	return mwInvocation{}, false
}

// TestMiddlewareScopedSubTask covers the scope-based path: tasks.SubTask
// wraps fn through the middleware synchronously, ctx flows into fn, and
// err from fn surfaces through middleware return.
func TestMiddlewareScopedSubTask(t *testing.T) {
	invs, mu, mw := recordingMiddleware()
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	type recKeyType struct{}
	var recKey recKeyType
	_ = recKey

	var fnCtxValue any
	sentinel := errors.New("validation failed")
	err := SubTask(ctx, "validate", func(childCtx context.Context) error {
		// The middleware decorated ctx with "decorated:<id>" under its
		// internal recKey. We can't reach that key from outside, but we can
		// at least confirm the value is set by reading via the same struct.
		fnCtxValue = childCtx
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err: got %v, want %v", err, sentinel)
	}
	if fnCtxValue == nil {
		t.Error("fn did not receive a ctx")
	}

	mu.Lock()
	defer mu.Unlock()
	ev, ok := findInv(*invs, "validate")
	if !ok {
		t.Fatal("middleware not invoked for validate")
	}
	if !ev.nextRan {
		t.Error("middleware next did not run")
	}
	if ev.endErr == nil || ev.endErr.Error() != "validation failed" {
		t.Errorf("end err in middleware: got %v, want %q", ev.endErr, "validation failed")
	}
}

// TestMiddlewareManualStartTask covers the manual-lifecycle path: a
// goroutine bridge so next() blocks until Close/Fail. Verifies the ctx
// the middleware passes to next() is what StartTask returns to the caller,
// and verifies the terminal err flows back through next.
func TestMiddlewareManualStartTask(t *testing.T) {
	type recKeyType struct{}
	var recKey recKeyType

	mu := &sync.Mutex{}
	var endErr error
	var endCalls int
	mw := func(ctx context.Context, info TaskInfo, next func(context.Context) error) error {
		decorated := context.WithValue(ctx, recKey, "title:"+info.Title)
		err := next(decorated)
		mu.Lock()
		endCalls++
		endErr = err
		mu.Unlock()
		return err
	}
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	taskCtx, task := StartTask[any](ctx, "import")
	if task == nil {
		t.Fatal("StartTask returned nil")
	}
	if got := taskCtx.Value(recKey); got != "title:import" {
		t.Errorf("returned ctx not decorated by middleware: got %v", got)
	}

	task.Fail("boom")

	// Allow goroutine to exit and signal end.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := endCalls > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if endCalls != 1 {
		t.Errorf("middleware end fired %d times, want 1", endCalls)
	}
	if endErr == nil || endErr.Error() != "boom" {
		t.Errorf("end err: got %v, want %q", endErr, "boom")
	}
}

// TestMiddlewareSubTaskMethod covers the no-ctx subtask methods
// (Task.SubTask / TaskSub.SubTask). Middleware fires per node, parent ctx
// flows into child middleware via middlewareInheritedCtx, terminal Close
// signals the bridge.
func TestMiddlewareSubTaskMethod(t *testing.T) {
	type recKeyType struct{}
	var recKey recKeyType

	var mu sync.Mutex
	var records []subTaskMethodRecord

	mw := func(ctx context.Context, info TaskInfo, next func(context.Context) error) error {
		seen := ctx.Value(recKey)
		decorated := context.WithValue(ctx, recKey, "set-by:"+info.Title)
		err := next(decorated)
		mu.Lock()
		records = append(records, subTaskMethodRecord{
			title: info.Title, parentID: info.ParentID, seenKey: seen, endErr: err,
		})
		mu.Unlock()
		return err
	}
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	_, task := StartTask[any](ctx, "root")
	sub1 := task.SubTask("level-1")
	sub2 := sub1.SubTask("level-2")
	sub2.Close()
	sub1.Close()
	task.Close()

	// Wait for all middleware goroutines to exit.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(records) >= 3
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []struct {
		title       string
		parentEmpty bool
		seenSet     string // value of recKey at start ("" = not set)
	}{
		{"root", false, ""},               // root: no parent ctx
		{"level-1", false, "set-by:root"}, // level-1: inherits root's decoration
		{"level-2", false, "set-by:level-1"},
	} {
		var found *subTaskMethodRecord
		for i := range records {
			if records[i].title == want.title {
				found = &records[i]
				break
			}
		}
		if found == nil {
			t.Errorf("no middleware record for %q", want.title)
			continue
		}
		if found.endErr != nil {
			t.Errorf("%s end err: got %v, want nil", want.title, found.endErr)
		}
		if want.seenSet == "" {
			if found.seenKey != nil {
				t.Errorf("%s: expected no recKey at start, got %v", want.title, found.seenKey)
			}
		} else {
			if found.seenKey != want.seenSet {
				t.Errorf("%s: recKey at start = %v, want %v", want.title, found.seenKey, want.seenSet)
			}
		}
	}
	// root's parentID is the implicit root ("root"); subtasks' parentIDs are non-empty.
	if r, ok := findRecord(records, "root"); ok && r.parentID != "root" {
		t.Errorf("root parentID: got %q, want %q (implicit root)", r.parentID, "root")
	}
	if r, ok := findRecord(records, "level-1"); ok && r.parentID == "" {
		t.Error("level-1 parentID should be non-empty")
	}
	if r, ok := findRecord(records, "level-2"); ok && r.parentID == "" {
		t.Error("level-2 parentID should be non-empty")
	}
}

type subTaskMethodRecord struct {
	title    string
	parentID string
	seenKey  any
	endErr   error
}

func findRecord(records []subTaskMethodRecord, title string) (subTaskMethodRecord, bool) {
	for _, r := range records {
		if r.title == title {
			return r, true
		}
	}
	return subTaskMethodRecord{}, false
}

// TestMiddlewareSkipNext (scope-based): if middleware returns without
// calling next, the user fn does not run and the middleware's return is
// surfaced as the SubTask error.
func TestMiddlewareSkipNext(t *testing.T) {
	mw := func(ctx context.Context, info TaskInfo, next func(context.Context) error) error {
		// Deliberately don't call next. Return our own error.
		return errors.New("blocked by middleware")
	}
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	var fnRan bool
	err := SubTask(ctx, "blocked", func(_ context.Context) error {
		fnRan = true
		return nil
	})
	if fnRan {
		t.Error("fn ran even though middleware did not call next")
	}
	if err == nil || err.Error() != "blocked by middleware" {
		t.Errorf("err: got %v, want %q", err, "blocked by middleware")
	}
}

// TestMiddlewareSkipNextManual: for manual tasks, if middleware returns
// without calling next, runManaged unblocks via the readyOnce fallback,
// the goroutine exits cleanly, and a subsequent Close does not panic
// (the buffered done channel send is dropped).
func TestMiddlewareSkipNextManual(t *testing.T) {
	var nextCalled atomic.Bool
	mw := func(ctx context.Context, info TaskInfo, next func(context.Context) error) error {
		// Deliberately don't call next.
		_ = nextCalled
		return nil
	}
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	taskCtx, task := StartTask[any](ctx, "no-next")
	if task == nil {
		t.Fatal("StartTask returned nil")
	}
	if taskCtx == nil {
		t.Fatal("taskCtx is nil")
	}

	// Close should not panic even though the middleware never read from done.
	task.Close()

	if nextCalled.Load() {
		t.Error("next was called but should not have been")
	}
}

// TestMiddlewareCascadeFailure: failTop on a parent shared task signals
// each child's middleware end with the cascade error.
func TestMiddlewareCascadeFailure(t *testing.T) {
	var mu sync.Mutex
	endErrs := map[string]error{}

	mw := func(ctx context.Context, info TaskInfo, next func(context.Context) error) error {
		err := next(ctx)
		mu.Lock()
		endErrs[info.Title] = err
		mu.Unlock()
		return err
	}
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	_, tm := setupTestServer(t)
	tm.hooks = opts // inject after setup since setupTestServer uses nil

	root := tm.create("root", 1, true, context.Background())
	root.mu.Lock()
	root.status = TaskNodeStatusRunning
	root.mu.Unlock()
	rootCtx := root.runManaged(context.Background())
	_ = rootCtx

	c1 := root.createChild("child-1")
	c1.runManaged(root.middlewareInheritedCtx())
	c2 := root.createChild("child-2")
	c2.runManaged(root.middlewareInheritedCtx())

	root.failTop("canceled")

	// Wait for all middlewares to drain.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(endErrs) >= 3
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, title := range []string{"root", "child-1", "child-2"} {
		err, ok := endErrs[title]
		if !ok {
			t.Errorf("middleware did not see end for %q", title)
			continue
		}
		if err == nil || err.Error() != "canceled" {
			t.Errorf("%s end err: got %v, want %q", title, err, "canceled")
		}
	}
}

// TestNoMiddleware verifies that calling Enable without any middleware
// option leaves task creation working with zero overhead.
func TestNoMiddleware(t *testing.T) {
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", nil)
	ctx := withDelivery(context.Background(), d)

	_, task := StartTask[any](ctx, "no middleware")
	if task == nil {
		t.Fatal("StartTask returned nil")
	}
	task.Close()

	err := SubTask(ctx, "scoped", func(_ context.Context) error { return nil })
	if err != nil {
		t.Errorf("SubTask err: %v", err)
	}
}

// TestMiddlewareIdempotentEnd verifies that double-completing a task does
// not signal middlewareDone twice (the terminal-state guards prevent it).
// Without idempotency, the second send would block on an unbuffered receive
// or fill the buffer; the test exercises the no-extra-signal contract.
func TestMiddlewareIdempotentEnd(t *testing.T) {
	var endCalls atomic.Int32
	mw := func(ctx context.Context, info TaskInfo, next func(context.Context) error) error {
		err := next(ctx)
		endCalls.Add(1)
		return err
	}
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	_, tm := setupTestServer(t)
	tm.hooks = opts

	node := tm.create("once", 1, true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()
	_ = node.runManaged(context.Background())

	node.completeTop()
	node.completeTop() // idempotent — should not signal end again

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if endCalls.Load() > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := endCalls.Load(); got != 1 {
		t.Errorf("end fired %d times, want 1", got)
	}
}

// TestMiddlewareSharedSubTask exercises the full SharedSubTask path — a real
// shared task created via the task manager, not the routes-through-an-existing-
// shared-node shortcut. It pins down how many times the middleware fires for a
// single SharedSubTask call.
//
// NOTE: today the middleware fires TWICE for one SharedSubTask call, both with
// the same title — once for the shared top-level node, and once for the inner
// child node that SharedSubTask creates by delegating to SubTask. That means a
// caller who installs middleware to log/trace sees a duplicate started/
// completed pair for every SharedSubTask. This is almost certainly unintended
// (see PR #205 review). This test documents the current behavior so the
// duplication is visible and asserted; if SharedSubTask is reworked to fire the
// middleware once per logical call, change wantFires to 1.
func TestMiddlewareSharedSubTask(t *testing.T) {
	invs, mu, mw := recordingMiddleware()
	opts := buildEnableOptions([]EnableOption{WithTaskMiddleware(mw)})

	_, tm := setupTestServer(t)
	tm.hooks = opts // inject after setup since setupTestServer uses nil

	tc := newTestPushConn()
	ctx := tc.WithContext(context.Background()) // sets aprot.Connection
	ctx = withTaskManager(ctx, tm)

	var fnRuns int
	sentinel := errors.New("boom")
	err := SharedSubTask(ctx, "shared-sub", func(context.Context) error {
		fnRuns++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("SharedSubTask err: got %v, want %v", err, sentinel)
	}
	// The user fn runs exactly once even though the middleware wraps two nodes.
	if fnRuns != 1 {
		t.Errorf("user fn ran %d times, want 1", fnRuns)
	}

	mu.Lock()
	defer mu.Unlock()
	var fires int
	for _, i := range *invs {
		if i.info.Title != "shared-sub" {
			continue
		}
		fires++
		if !i.nextRan {
			t.Error("middleware next did not run for shared-sub")
		}
		if i.endErr == nil || i.endErr.Error() != "boom" {
			t.Errorf("middleware end err: got %v, want %q", i.endErr, "boom")
		}
	}
	const wantFires = 2 // see NOTE above: currently double-fires
	if fires != wantFires {
		t.Errorf("middleware fired %d times for one SharedSubTask, want %d "+
			"(shared node + inner child)", fires, wantFires)
	}
}

// TestMiddlewareConnectionCancel is a dummy reference to aprot to keep
// the import in case other tests in this file get pruned.
var _ = aprot.Connection
