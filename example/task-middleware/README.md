# task-middleware example

A self-contained aprot server that demonstrates [`tasks.WithTaskMiddleware`](https://pkg.go.dev/github.com/marrasen/aprot/tasks#WithTaskMiddleware).

The middleware wraps every task and subtask, logs lifecycle events with structured slog fields, and decorates the task ctx with task identity. Both the wrapping (start/end logging) and the decoration (downstream ctx propagation) flow through a single function.

## Run it

```bash
go run .
```

That's all — the program starts the server and then drives one successful and
one failing request against itself, so the demo needs no second terminal.

## What you'll see

The server log shows the middleware firing for the root task and each subtask,
with parent IDs threading the tree (durations vary slightly per run):

```
INFO server started; driving demo requests against it addr=http://127.0.0.1:8080
INFO task started   task_id=t1 task_title="Import accounts.csv" parent_id=root
INFO task started   task_id=t2 task_title=Validate    parent_id=t1
INFO task completed task_id=t2 task_title=Validate    parent_id=t1 duration_ms=50
INFO task started   task_id=t3 task_title=Parse       parent_id=t1
INFO task completed task_id=t3 task_title=Parse       parent_id=t1 duration_ms=80
INFO task started   task_id=t4 task_title="Write to DB" parent_id=t1
INFO task completed task_id=t4 task_title="Write to DB" parent_id=t1 duration_ms=120
INFO task completed task_id=t1 task_title="Import accounts.csv" parent_id=root duration_ms=251
```

The second (failing) request passes an empty filename, so the `Validate`
subtask returns an error and the middleware logs `task failed` for both the
subtask and the root:

```
ERROR task failed task_id=t2 task_title=Validate parent_id=t1 err="empty filename" duration_ms=50
ERROR task failed task_id=t1 task_title="Import " parent_id=root err="empty filename" duration_ms=51
```

## Driving it by hand

The server exposes the WebSocket transport at `/ws`. Connect any WebSocket
client, discard the config frame the server sends on connect, then send one
request frame:

```json
{ "type": "request", "id": "1", "method": "Demo.Import", "params": ["accounts.csv"] }
```

Results, progress, and task frames all arrive on the same socket. `driveDemo`
in `main.go` does exactly this in about 30 lines. For a real client, the
generated TypeScript client is friendlier — see the `vanilla` and `react`
examples.

## Swap the logger

The middleware in `main.go` uses `slog`. Replace it with `log.Printf`, `zerolog.Ctx(ctx)...`, `zap.L().With(...)`, or whatever your app uses — the middleware contract (`func(ctx, info, next) error`) is logger-agnostic.
