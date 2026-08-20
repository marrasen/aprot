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

// ListTasks returns the current shared-task snapshot for the calling caller
// (IsOwner is evaluated against them). Clients hydrate from it on mount and
// reconnect: full TaskStateEvent broadcasts fire only on lifecycle changes,
// so a consumer arriving mid-task would otherwise see nothing until the next
// lifecycle event.
//
// It answers on every transport. Request-scoped callers (REST, MCP) have no
// connection, so ownership is matched on their address alone — this is how a
// caller that started a shared task over MCP polls it, since the tool result
// carries the task ID but no delivery follows it.
func (h *tasksHandler) ListTasks(ctx context.Context) ([]SharedTaskState, error) {
	if h.tm == nil {
		return []SharedTaskState{}, nil
	}
	var connID uint64
	if conn := aprot.Connection(ctx); conn != nil {
		connID = conn.ID()
	}
	return h.tm.snapshotAllForConn(connID, aprot.UserID(ctx)), nil
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
	// The convenience hook below writes tasks.ts wholesale; reserve that file
	// so a shared type in the tasks package (e.g. a handler returning
	// *tasks.TaskRef) is emitted as tasks.types.ts instead of being clobbered.
	r.ReserveClientFile("tasks")
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
	// See Enable: reserve tasks.ts so a shared tasks-package type (e.g. a
	// handler returning *tasks.TaskRef) is not clobbered by the convenience hook.
	r.ReserveClientFile("tasks")
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
