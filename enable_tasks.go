package aprot

import (
	"context"
	"reflect"

	"github.com/marrasen/aprot/tasks"
)

// taskCancelHandler is the handler struct for task cancellation.
type taskCancelHandler struct{}

func (h *taskCancelHandler) CancelTask(ctx context.Context, req *tasks.CancelTaskRequest) error {
	return CancelSharedTask(ctx, req.TaskID)
}

// EnableTasks registers the shared task system with the registry.
func EnableTasks(r *Registry) {
	r.SetTasksEnabled(true)
	r.RegisterEnum(tasks.TaskNodeStatusValues())
	handler := &taskCancelHandler{}
	r.Register(handler)
	r.RegisterPushEventFor(handler, tasks.TaskStateEvent{})
	r.RegisterPushEventFor(handler, tasks.TaskUpdateEvent{})
	r.OnGenerate(func(results map[string]string, mode OutputMode) {
		tasks.AppendConvenienceCode(results, mode == OutputReact, nil)
	})
}

// EnableTasksWithMeta registers the shared task system with typed metadata.
func EnableTasksWithMeta[M any](r *Registry) {
	metaType := reflect.TypeFor[M]()
	r.SetTaskMetaType(metaType)
	r.SetTasksEnabled(true)
	r.RegisterEnum(tasks.TaskNodeStatusValues())
	handler := &taskCancelHandler{}
	r.Register(handler)
	r.RegisterPushEventFor(handler, tasks.TaskStateEvent{})
	r.RegisterPushEventFor(handler, tasks.TaskUpdateEvent{})
	r.OnGenerate(func(results map[string]string, mode OutputMode) {
		tasks.AppendConvenienceCode(results, mode == OutputReact, metaType)
	})
}
