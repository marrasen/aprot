package tasks

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/marrasen/aprot"
)

// hookEvent is one captured invocation of either start or end hook.
type hookEvent struct {
	kind     string // "start" or "end"
	id       string
	title    string
	parentID string
	err      error
	ctxValue any // value pulled from ctx with key probeKey, if any
}

// recorder collects hook invocations from concurrent goroutines.
type recorder struct {
	mu     sync.Mutex
	events []hookEvent
}

func (r *recorder) snapshot() []hookEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]hookEvent, len(r.events))
	copy(out, r.events)
	return out
}

type probeKeyType struct{}

var probeKey probeKeyType

// makeRecorder builds an enableOptions with both hooks installed; the start
// hook also stores a marker on ctx so we can verify the decorated ctx
// reaches the end hook.
func makeRecorder() (*recorder, *enableOptions) {
	r := &recorder{}
	opts := buildEnableOptions([]EnableOption{
		WithTaskStartHook(func(ctx context.Context, id, title, parent string) context.Context {
			r.mu.Lock()
			r.events = append(r.events, hookEvent{
				kind: "start", id: id, title: title, parentID: parent,
				ctxValue: ctx.Value(probeKey),
			})
			r.mu.Unlock()
			return context.WithValue(ctx, probeKey, "decorated:"+id)
		}),
		WithTaskEndHook(func(ctx context.Context, id, title, parent string, err error) {
			r.mu.Lock()
			r.events = append(r.events, hookEvent{
				kind: "end", id: id, title: title, parentID: parent, err: err,
				ctxValue: ctx.Value(probeKey),
			})
			r.mu.Unlock()
		}),
	})
	return r, opts
}

func findEvent(events []hookEvent, kind, title string) (hookEvent, bool) {
	for _, e := range events {
		if e.kind == kind && e.title == title {
			return e, true
		}
	}
	return hookEvent{}, false
}

// TestStartHookFiresForRequestScopedRoot verifies that StartTask invokes the
// start hook with the root task's ID and an empty parentID.
func TestStartHookFiresForRequestScopedRoot(t *testing.T) {
	r, opts := makeRecorder()
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	taskCtx, task := StartTask[any](ctx, "import users")
	if task == nil {
		t.Fatal("StartTask returned nil")
	}

	startEv, ok := findEvent(r.snapshot(), "start", "import users")
	if !ok {
		t.Fatal("start hook was not fired")
	}
	if startEv.parentID != "" {
		t.Errorf("parentID for root: got %q, want empty", startEv.parentID)
	}
	if startEv.id == "" {
		t.Error("start hook id is empty")
	}
	// The decorated ctx should be returned to the caller.
	if got := taskCtx.Value(probeKey); got != "decorated:"+startEv.id {
		t.Errorf("returned ctx not decorated by start hook: got %v", got)
	}

	task.Close()

	endEv, ok := findEvent(r.snapshot(), "end", "import users")
	if !ok {
		t.Fatal("end hook was not fired on Close")
	}
	if endEv.err != nil {
		t.Errorf("end err: got %v, want nil", endEv.err)
	}
	if endEv.ctxValue != "decorated:"+startEv.id {
		t.Errorf("end hook did not receive decorated ctx: got %v", endEv.ctxValue)
	}
}

// TestEndHookFiresOnFail verifies that Task.Fail invokes the end hook with
// the user-supplied error message.
func TestEndHookFiresOnFail(t *testing.T) {
	r, opts := makeRecorder()
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	_, task := StartTask[any](ctx, "broken job")
	task.Fail("disk full")

	endEv, ok := findEvent(r.snapshot(), "end", "broken job")
	if !ok {
		t.Fatal("end hook was not fired on Fail")
	}
	if endEv.err == nil || endEv.err.Error() != "disk full" {
		t.Errorf("end err: got %v, want %q", endEv.err, "disk full")
	}
}

// TestSubTaskHookPropagation verifies the package-level SubTask fires both
// hooks with the parent's ID as parentID, and that the start hook's
// returned ctx reaches the user's fn.
func TestSubTaskHookPropagation(t *testing.T) {
	r, opts := makeRecorder()
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	parentCtx, parent := StartTask[any](ctx, "parent")
	if parent == nil {
		t.Fatal("StartTask returned nil")
	}
	parentID := parent.ID()

	var seenInsideFn any
	err := SubTask(parentCtx, "child", func(childCtx context.Context) error {
		seenInsideFn = childCtx.Value(probeKey)
		return nil
	})
	if err != nil {
		t.Fatalf("SubTask err: %v", err)
	}
	parent.Close()

	events := r.snapshot()

	startChild, ok := findEvent(events, "start", "child")
	if !ok {
		t.Fatal("start hook for child was not fired")
	}
	if startChild.parentID != parentID {
		t.Errorf("child parentID: got %q, want %q", startChild.parentID, parentID)
	}

	endChild, ok := findEvent(events, "end", "child")
	if !ok {
		t.Fatal("end hook for child was not fired")
	}
	if endChild.err != nil {
		t.Errorf("child end err: got %v, want nil", endChild.err)
	}

	// The start hook decorates ctx; fn should see the child's decoration.
	if got := seenInsideFn; got != "decorated:"+startChild.id {
		t.Errorf("fn ctx not decorated by child start hook: got %v", got)
	}
}

// TestSharedTaskHooks verifies hooks fire for shared root tasks created via
// taskManager.create + manual status flip (matches the StartTask shared
// path's sequence).
func TestSharedTaskHooks(t *testing.T) {
	r, opts := makeRecorder()
	_, tm := setupTestServer(t)
	tm.hooks = opts // inject after setup since setupTestServer uses nil

	ctx := context.Background()
	ctx = aprot.WithTestConnection(ctx, 1)
	ctx = withTaskManager(ctx, tm)

	taskCtx, task := StartTask[any](ctx, "shared op", Shared())
	if task == nil {
		t.Fatal("StartTask returned nil")
	}

	startEv, ok := findEvent(r.snapshot(), "start", "shared op")
	if !ok {
		t.Fatal("start hook was not fired for shared task")
	}
	if got := taskCtx.Value(probeKey); got != "decorated:"+startEv.id {
		t.Errorf("returned ctx not decorated: got %v", got)
	}

	task.Close()

	endEv, ok := findEvent(r.snapshot(), "end", "shared op")
	if !ok {
		t.Fatal("end hook was not fired for shared task")
	}
	if endEv.err != nil {
		t.Errorf("end err: got %v, want nil", endEv.err)
	}
}

// TestCascadeFailureHooks verifies that failTop on a parent fires the end
// hook for each running child, with the cascade error message.
func TestCascadeFailureHooks(t *testing.T) {
	r, opts := makeRecorder()
	_, tm := setupTestServer(t)
	tm.hooks = opts

	root := tm.create("root", 1, true, context.Background())
	root.mu.Lock()
	root.status = TaskNodeStatusRunning
	root.mu.Unlock()

	child1 := root.createChild("child-1")
	child2 := root.createChild("child-2")
	_ = child1
	_ = child2

	root.failTop("canceled")

	events := r.snapshot()

	for _, title := range []string{"root", "child-1", "child-2"} {
		ev, ok := findEvent(events, "end", title)
		if !ok {
			t.Errorf("end hook for %q was not fired", title)
			continue
		}
		if ev.err == nil || ev.err.Error() != "canceled" {
			t.Errorf("%q end err: got %v, want %q", title, ev.err, "canceled")
		}
	}
}

// TestHookIdempotency verifies that double-completing or double-failing a
// task does not fire the end hook twice.
func TestHookIdempotency(t *testing.T) {
	t.Run("completeTop double", func(t *testing.T) {
		r, opts := makeRecorder()
		_, tm := setupTestServer(t)
		tm.hooks = opts

		node := tm.create("once", 1, true, context.Background())
		node.mu.Lock()
		node.status = TaskNodeStatusRunning
		node.mu.Unlock()

		node.completeTop()
		node.completeTop() // no-op per existing idempotency

		ends := 0
		for _, e := range r.snapshot() {
			if e.kind == "end" && e.title == "once" {
				ends++
			}
		}
		if ends != 1 {
			t.Errorf("end fired %d times, want 1", ends)
		}
	})

	t.Run("setStatus then finalize", func(t *testing.T) {
		// Simulates Task.Close (non-shared) followed by middleware
		// finalizeTaskSlot — both call setStatus(Completed).
		r, opts := makeRecorder()
		tc := newTestPushConn()
		d := newRequestDelivery(tc.Conn, "req-1", opts)
		ctx := withDelivery(context.Background(), d)
		slot := &taskSlot{}
		ctx = withTaskSlot(ctx, slot)

		_, task := StartTask[any](ctx, "twice")
		task.Close()
		// Simulate middleware finalize.
		finalizeTaskSlot(ctx, slot, nil, d)

		ends := 0
		for _, e := range r.snapshot() {
			if e.kind == "end" && e.title == "twice" {
				ends++
			}
		}
		if ends != 1 {
			t.Errorf("end fired %d times, want 1", ends)
		}
	})
}

// TestNoHooksDoesNotBreak verifies that calling Enable without options leaves
// task creation working and avoids any hook invocation.
func TestNoHooksDoesNotBreak(t *testing.T) {
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", nil)
	ctx := withDelivery(context.Background(), d)

	_, task := StartTask[any](ctx, "no hooks")
	if task == nil {
		t.Fatal("StartTask returned nil")
	}
	task.Close()
	// No assertion needed — passing means no panic and no invariant broke.
}

// TestOnlyStartHookInstalled verifies that installing only WithTaskStartHook
// works (end hook is silently absent) and vice versa.
func TestOnlyStartHookInstalled(t *testing.T) {
	t.Run("start only", func(t *testing.T) {
		var startCalls int
		opts := buildEnableOptions([]EnableOption{
			WithTaskStartHook(func(ctx context.Context, id, title, parent string) context.Context {
				startCalls++
				return ctx
			}),
		})
		tc := newTestPushConn()
		d := newRequestDelivery(tc.Conn, "req-1", opts)
		ctx := withDelivery(context.Background(), d)

		_, task := StartTask[any](ctx, "start-only")
		task.Close()

		if startCalls != 1 {
			t.Errorf("start fired %d times, want 1", startCalls)
		}
	})

	t.Run("end only", func(t *testing.T) {
		// End-only configuration must still receive the original request ctx
		// (not context.Background()) so ctx-aware loggers / spans still work.
		type endOnlyKey struct{}

		var endErr error
		var endCalls int
		var endCtxValue any
		opts := buildEnableOptions([]EnableOption{
			WithTaskEndHook(func(ctx context.Context, id, title, parent string, err error) {
				endCalls++
				endErr = err
				endCtxValue = ctx.Value(endOnlyKey{})
			}),
		})
		tc := newTestPushConn()
		d := newRequestDelivery(tc.Conn, "req-1", opts)
		ctx := context.WithValue(context.Background(), endOnlyKey{}, "request-scoped")
		ctx = withDelivery(ctx, d)

		_, task := StartTask[any](ctx, "end-only")
		task.Fail("boom")

		if endCalls != 1 {
			t.Errorf("end fired %d times, want 1", endCalls)
		}
		if endErr == nil || endErr.Error() != "boom" {
			t.Errorf("end err: got %v, want %q", endErr, "boom")
		}
		if endCtxValue != "request-scoped" {
			t.Errorf("end hook did not receive request ctx: got %v", endCtxValue)
		}
	})
}

// TestOutputWriterHooks verifies OutputWriter creates a child task that fires
// both hooks.
func TestOutputWriterHooks(t *testing.T) {
	r, opts := makeRecorder()
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	w := OutputWriter(ctx, "build log")
	_, _ = w.Write([]byte("compiling..."))
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := r.snapshot()
	if _, ok := findEvent(events, "start", "build log"); !ok {
		t.Error("start hook for OutputWriter was not fired")
	}
	if _, ok := findEvent(events, "end", "build log"); !ok {
		t.Error("end hook for OutputWriter was not fired")
	}
}

// TestSubTaskFailedFnFiresEndWithError verifies that a SubTask whose fn
// returns an error fires the end hook with that error.
func TestSubTaskFailedFnFiresEndWithError(t *testing.T) {
	r, opts := makeRecorder()
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	sentinel := errors.New("validation failed")
	err := SubTask(ctx, "validate", func(ctx context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("SubTask err: got %v, want %v", err, sentinel)
	}

	endEv, ok := findEvent(r.snapshot(), "end", "validate")
	if !ok {
		t.Fatal("end hook was not fired")
	}
	if endEv.err == nil || endEv.err.Error() != "validation failed" {
		t.Errorf("end err: got %v, want %q", endEv.err, "validation failed")
	}
}

// TestTaskSubMethodSubTask verifies Task.SubTask and TaskSub.SubTask fire
// both hooks (with context.Background since no ctx is supplied).
func TestTaskSubMethodSubTask(t *testing.T) {
	r, opts := makeRecorder()
	tc := newTestPushConn()
	d := newRequestDelivery(tc.Conn, "req-1", opts)
	ctx := withDelivery(context.Background(), d)

	_, task := StartTask[any](ctx, "root")

	sub1 := task.SubTask("level-1")
	sub2 := sub1.SubTask("level-2")
	sub2.Close()
	sub1.Close()
	task.Close()

	events := r.snapshot()
	for _, title := range []string{"root", "level-1", "level-2"} {
		if _, ok := findEvent(events, "start", title); !ok {
			t.Errorf("start hook for %q was not fired", title)
		}
		if _, ok := findEvent(events, "end", title); !ok {
			t.Errorf("end hook for %q was not fired", title)
		}
	}
}
