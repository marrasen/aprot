package tasks

import (
	"context"
	"reflect"

	"github.com/marrasen/aprot"
)

// tasksHandler is the handler struct for client-facing task operations.
type tasksHandler struct{}

func (h *tasksHandler) CancelTask(ctx context.Context, taskId string) error {
	return CancelSharedTask(ctx, taskId)
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
		s.Use(taskMiddleware(tm))
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			states := tm.snapshotAllForConn(conn.ID(), conn.UserID())
			if len(states) > 0 {
				_ = conn.Push(TaskStateEvent{Tasks: states})
			}
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}

// EnableWithMeta registers the shared task system with typed metadata. Pass
// options such as [WithTaskMiddleware] to wrap every task with custom logic
// (logging, tracing, ctx decoration).
func EnableWithMeta[M any](r *aprot.Registry, opts ...EnableOption) {
	o := buildEnableOptions(opts)
	metaType := reflect.TypeFor[M]()
	handler := &tasksHandler{}
	r.Register(handler)
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
		s.Use(taskMiddleware(tm))
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			states := tm.snapshotAllForConn(conn.ID(), conn.UserID())
			if len(states) > 0 {
				_ = conn.Push(TaskStateEvent{Tasks: states})
			}
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}
