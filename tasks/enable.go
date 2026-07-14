package tasks

import (
	"context"
	"reflect"

	"github.com/marrasen/aprot"
)

// tasksHandler is the handler struct for client-facing task operations.
type tasksHandler struct {
	// tm is assigned on server init; ListTasks answers from the live manager.
	tm *taskManager
}

func (h *tasksHandler) CancelTask(ctx context.Context, taskId string) error {
	return CancelSharedTask(ctx, taskId)
}

// ListTasks returns the current shared-task snapshot for the calling
// connection (IsOwner is evaluated against the caller). Clients hydrate from
// it on mount and reconnect: full TaskStateEvent broadcasts fire only on
// lifecycle changes, so a consumer arriving mid-task would otherwise see
// nothing until the next lifecycle event.
func (h *tasksHandler) ListTasks(ctx context.Context) ([]SharedTaskState, error) {
	conn := aprot.Connection(ctx)
	if h.tm == nil || conn == nil {
		return []SharedTaskState{}, nil
	}
	return h.tm.snapshotAllForConn(conn.ID(), conn.UserID()), nil
}

// Enable registers the shared task system with the registry. Pass options
// such as [WithTaskMiddleware] to wrap every task with custom logic
// (logging, tracing, ctx decoration).
func Enable(r *aprot.Registry, opts ...EnableOption) {
	o := buildEnableOptions(opts)
	handler := &tasksHandler{}
	r.Register(handler)
	r.RegisterEnumFor(handler, TaskNodeStatusValues())
	r.RegisterPushEventFor(handler, TaskStateEvent{})
	r.RegisterPushEventFor(handler, TaskUpdateEvent{})
	r.RegisterPushEventFor(handler, RequestTaskTreeEvent{})
	r.RegisterPushEventFor(handler, RequestTaskOutputEvent{})
	r.RegisterPushEventFor(handler, RequestTaskProgressEvent{})
	r.OnGenerate(func(results map[string]string, mode aprot.OutputMode) {
		appendTaskConvenienceCode(results, mode, nil)
	})
	r.OnServerInit(func(s *aprot.Server) {
		tm := newTaskManager(s, o)
		handler.tm = tm
		s.Use(taskMiddleware(tm))
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			// Push even an empty list: a reconnecting client whose task
			// finished while it was away must clear its stale state.
			states := tm.snapshotAllForConn(conn.ID(), conn.UserID())
			_ = conn.Push(TaskStateEvent{Tasks: states})
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}

// EnableWithMeta registers the shared task system with typed metadata. The
// generated client types TaskNode.meta and SharedTaskState.meta as M's
// TypeScript interface instead of `unknown`, and declares that interface
// alongside the other task types. Pass options such as [WithTaskMiddleware]
// to wrap every task with custom logic (logging, tracing, ctx decoration).
func EnableWithMeta[M any](r *aprot.Registry, opts ...EnableOption) {
	o := buildEnableOptions(opts)
	metaType := reflect.TypeFor[M]()
	handler := &tasksHandler{}
	r.Register(handler)
	// Codegen-only: emits meta fields as M's TS type instead of `unknown`.
	r.OverrideFieldType(TaskNode{}, "Meta", metaType)
	r.OverrideFieldType(SharedTaskState{}, "Meta", metaType)
	r.RegisterEnumFor(handler, TaskNodeStatusValues())
	r.RegisterPushEventFor(handler, TaskStateEvent{})
	r.RegisterPushEventFor(handler, TaskUpdateEvent{})
	r.RegisterPushEventFor(handler, RequestTaskTreeEvent{})
	r.RegisterPushEventFor(handler, RequestTaskOutputEvent{})
	r.RegisterPushEventFor(handler, RequestTaskProgressEvent{})
	r.OnGenerate(func(results map[string]string, mode aprot.OutputMode) {
		appendTaskConvenienceCode(results, mode, metaType)
	})
	r.OnServerInit(func(s *aprot.Server) {
		tm := newTaskManager(s, o)
		handler.tm = tm
		s.Use(taskMiddleware(tm))
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			// Push even an empty list: a reconnecting client whose task
			// finished while it was away must clear its stale state.
			states := tm.snapshotAllForConn(conn.ID(), conn.UserID())
			_ = conn.Push(TaskStateEvent{Tasks: states})
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}
