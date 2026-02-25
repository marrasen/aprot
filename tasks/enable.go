package tasks

import (
	"context"
	"reflect"

	"github.com/marrasen/aprot"
)

// taskCancelHandler is the handler struct for task cancellation.
type taskCancelHandler struct{}

func (h *taskCancelHandler) CancelTask(ctx context.Context, req *CancelTaskRequest) error {
	return CancelSharedTask(ctx, req.TaskID)
}

// Enable registers the shared task system with the registry.
func Enable(r *aprot.Registry) {
	r.RegisterEnum(TaskNodeStatusValues())
	handler := &taskCancelHandler{}
	r.Register(handler)
	r.RegisterPushEventFor(handler, TaskStateEvent{})
	r.RegisterPushEventFor(handler, TaskUpdateEvent{})
	r.OnGenerate(func(results map[string]string, mode aprot.OutputMode) {
		appendTaskConvenienceCode(results, mode, nil)
	})
	r.OnServerInit(func(s *aprot.Server) {
		tm := newTaskManager(s)
		s.AddInterceptor(&taskInterceptor{tm: tm})
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			states := tm.snapshotAllForConn(conn.ID())
			if len(states) > 0 {
				conn.Push(TaskStateEvent{Tasks: states})
			}
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}

// EnableWithMeta registers the shared task system with typed metadata.
func EnableWithMeta[M any](r *aprot.Registry) {
	metaType := reflect.TypeFor[M]()
	r.RegisterEnum(TaskNodeStatusValues())
	handler := &taskCancelHandler{}
	r.Register(handler)
	r.RegisterPushEventFor(handler, TaskStateEvent{})
	r.RegisterPushEventFor(handler, TaskUpdateEvent{})
	r.OnGenerate(func(results map[string]string, mode aprot.OutputMode) {
		appendTaskConvenienceCode(results, mode, metaType)
	})
	r.OnServerInit(func(s *aprot.Server) {
		tm := newTaskManager(s)
		s.AddInterceptor(&taskInterceptor{tm: tm})
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			states := tm.snapshotAllForConn(conn.ID())
			if len(states) > 0 {
				conn.Push(TaskStateEvent{Tasks: states})
			}
			return nil
		})
		s.OnStop(func() { tm.stop() })
	})
}
