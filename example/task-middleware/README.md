# task-middleware example

A minimal aprot server that demonstrates [`tasks.WithTaskMiddleware`](https://pkg.go.dev/github.com/marrasen/aprot/tasks#WithTaskMiddleware).

The middleware wraps every task and subtask, logs lifecycle events with structured slog fields, and decorates the task ctx with a `request_id`. Both the wrapping (start/end logging) and the decoration (downstream ctx propagation) flow through a single function.

## Run it

```bash
go run .
```

In another terminal:

```bash
curl -sX POST http://localhost:8080/rpc \
    -H 'Content-Type: application/json' \
    -d '{"id":"1","method":"Demo.Import","params":["accounts.csv"]}'
```

## What you'll see

The server log shows the middleware firing for the root task and each subtask, with parent IDs threading the tree:

```
INFO task started task_id=t1 task_title="Import accounts.csv"
INFO task started task_id=t2 task_title=Validate parent_id=t1
INFO task completed task_id=t2 task_title=Validate parent_id=t1 duration_ms=51
INFO task started task_id=t3 task_title=Parse parent_id=t1
INFO task completed task_id=t3 task_title=Parse parent_id=t1 duration_ms=82
INFO task started task_id=t4 task_title="Write to DB" parent_id=t1
INFO task completed task_id=t4 task_title="Write to DB" parent_id=t1 duration_ms=121
INFO task completed task_id=t1 task_title="Import accounts.csv" duration_ms=256
```

## Try a failure

```bash
curl -sX POST http://localhost:8080/rpc \
    -H 'Content-Type: application/json' \
    -d '{"id":"2","method":"Demo.Import","params":[""]}'
```

The `Validate` subtask returns an error; the middleware logs `task failed` for both the subtask and the root.

## Swap the logger

The middleware in `main.go` uses `slog`. Replace it with `log.Printf`, `zerolog.Ctx(ctx)...`, `zap.L().With(...)`, or whatever your app uses — the middleware contract (`func(ctx, info, next) error`) is logger-agnostic.
