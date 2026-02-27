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

// Enable registers the shared task system with the registry.
func Enable(r *aprot.Registry) {
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
		tm := newTaskManager(s)
		s.Use(taskMiddleware(tm))
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			states := tm.snapshotAllForConn(conn.ID())
			if len(states) > 0 {
				_ = conn.Push(TaskStateEvent{Tasks: states})
			}
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}

// EnableWithMeta registers the shared task system with typed metadata.
func EnableWithMeta[M any](r *aprot.Registry) {
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
		tm := newTaskManager(s)
		s.Use(taskMiddleware(tm))
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			states := tm.snapshotAllForConn(conn.ID())
			if len(states) > 0 {
				_ = conn.Push(TaskStateEvent{Tasks: states})
			}
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}
