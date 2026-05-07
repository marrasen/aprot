# Migration: Removal of `useXxxMutation()` hooks

This document is intended as a prompt for AI coding agents tasked with upgrading a codebase that uses aprot's old per-handler mutation hooks. A human reader can also follow it directly.

## What changed

aprot no longer generates per-handler mutation hooks. The runtime helpers that backed them have been removed too:

| Removed (old API) | Replacement |
| --- | --- |
| `useCreateUserMutation()`, `useDeleteTodoMutation()`, … any `useXxxMutation()` | Either the raw async function (`createUser(client, …)`) or the query hook's `mutate(action)` helper |
| `useMutation()` (generic runtime hook) | None — call the raw async function with `useApiClient()` |
| `UseMutationResult<TParams, TRes>`, `UseMutationOptions` (TS types) | None — these no longer exist |

The query hook's `mutate(action)` helper has been **extended**: it now accepts a `(client: ApiClient) => Promise<unknown>` thunk in addition to the existing `Promise<unknown>` form. The previous `() => Promise<unknown>` form remains type-compatible (TypeScript parameter bivariance), so existing call sites that wrote `mutate(() => addTodo(client, …))` still typecheck.

## Why

`useXxxMutation()`'s `mutate()` swallowed errors and returned `undefined as TRes` on failure. The `Promise<TRes>` type lied at runtime; `void` mutations could not distinguish success from "not yet called"; and the only correct after-success pattern (`useEffect([data])`) was non-obvious. The two replacement patterns cover the same ground without those foot-guns.

---

## Agent prompt — drop-in

> You are upgrading a TypeScript / React codebase that depends on `github.com/marrasen/aprot` from a version that generated per-handler mutation hooks (`useXxxMutation()`) to a version that does not. The aprot Go module has already been bumped and the generated TypeScript has been regenerated, so all references to `useXxxMutation()`, `useMutation`, `UseMutationResult`, and `UseMutationOptions` are now unresolved imports. Your job is to rewrite every call site to use one of the two supported patterns.
>
> **Before you start**, confirm regeneration has happened by reading the project's TypeScript generator entry point and running it (typically `cd tools/generate && go run main.go` or similar — check the project's CLAUDE.md / README for the exact command). If `useXxxMutation` still appears in any generated file, regeneration did not run; stop and ask the human.
>
> **Find every call site** with:
> ```sh
> rg -n "use\w+Mutation\(" --type=ts --type=tsx
> rg -n "from .*\butil/\b.*\bAPI\b|UseMutationResult|UseMutationOptions|useMutation\b" --type=ts --type=tsx
> ```
>
> For each call site, choose **Pattern 1** or **Pattern 2** using the rules below. Do not skip the rules — picking the wrong pattern leads to subtle bugs (e.g., losing the auto-refetch when one is needed).
>
> ### Choosing a pattern
>
> - If the same component (or a sibling that shares a `useXxx()` query for the data this mutation modifies) renders a list / detail that should reflect the mutation's effect, **use Pattern 1** — call `mutate(action)` on that query hook.
> - If there is no surrounding query, or the mutation is invoked from a non-render context (event handler outside React, async helper), **use Pattern 2** — call the raw async function with `useApiClient()`.
> - If the old code reacted to `data` from the mutation hook for after-success side effects (`useEffect([data])`), the after-success work should move into the imperative call site directly (Pattern 2's `try { … }` block, or a `.then()` chained on Pattern 1's `mutate()` return).
>
> ### Pattern 1 — query-scoped `mutate(action)`
>
> Old:
> ```tsx
> const { mutate, isLoading, error } = useCreateTodoMutation();
> const onAdd = () => mutate({ title: 'Buy milk' });
> ```
>
> New (sharing state with `useListTodos`):
> ```tsx
> const { data, mutate, isLoading, error } = useListTodos();
> const onAdd = () => mutate((client) => createTodo(client, { title: 'Buy milk' }));
> ```
>
> Notes:
> - The thunk receives the `ApiClient` the hook is bound to. **Do not** call `useApiClient()` separately at the same site.
> - `mutate(...)` returns `Promise<void>`. Errors thrown by the action are captured in the hook's `error` field. Do not wrap in try/catch.
> - For an "after-success" side effect, chain `.then()` on the returned promise. The promise resolves whether or not the action succeeded; check the hook's `error` field if you need to branch:
>   ```tsx
>   const onAdd = async () => {
>       await mutate((client) => createTodo(client, { title }));
>       if (!error) clearForm();
>   };
>   ```
>
> ### Pattern 2 — raw async function
>
> Old:
> ```tsx
> const { mutate, isLoading, error, data } = useCreateUserMutation();
> useEffect(() => { if (data) toast(`Created ${data.id}`); }, [data]);
> ```
>
> New:
> ```tsx
> const client = useApiClient();
> const [isLoading, setIsLoading] = useState(false);
> const [error, setError] = useState<Error | null>(null);
>
> const onCreate = async (name: string, email: string) => {
>     setIsLoading(true);
>     setError(null);
>     try {
>         const user = await createUser(client, name, email);
>         toast(`Created ${user.id}`);
>     } catch (err) {
>         setError(err as Error);
>     } finally {
>         setIsLoading(false);
>     }
> };
> ```
>
> Notes:
> - Generated functions throw `ApiError` (protocol error) and `ConnectionError` (transport error). Inspect them with `err instanceof ApiError && err.isValidationFailed()` etc.
> - For cancellation, create your own `AbortController` and pass `{ signal }` as the last argument: `await createUser(client, name, email, { signal: controller.signal })`.
> - For progress reporting (handlers that call `aprot.Progress(ctx).Update(...)`), pass `{ onProgress }` in the same options object.
>
> ### Anti-pattern to fix on the way through
>
> If the old code wrapped a mutation hook's `mutate()` in `try / catch`, the catch was dead code. Carry the *intent* (toast / log / state-set on error) into the new code, but do not preserve the try/catch shape verbatim — under Pattern 1 the catch belongs around the chained `.then()` (or just read the hook's `error`), and under Pattern 2 it now actually fires.
>
> ### Verification
>
> After all rewrites:
> 1. `rg -n "use\w+Mutation\(|UseMutationResult|UseMutationOptions|\buseMutation\b" --type=ts --type=tsx` should return zero matches.
> 2. `npx tsc --noEmit` (or the project's type-check command) must pass.
> 3. The project's lint / test suite must pass.
> 4. **You cannot infer correctness from compilation alone.** Open the running app and exercise each rewritten flow once: trigger the mutation, confirm it succeeds in the happy path, then deliberately fail it (disconnect network / send invalid input) and confirm the error is surfaced to the user.

---

## Quick reference (for humans skimming)

```tsx
// BEFORE (removed)
const { mutate, isLoading, error } = useCreateTodoMutation();
mutate({ title: 'Buy milk' });

// AFTER, Pattern 1 (shares state with the list it modifies)
const { data, mutate, isLoading, error } = useListTodos();
mutate((client) => createTodo(client, { title: 'Buy milk' }));

// AFTER, Pattern 2 (no surrounding query)
const client = useApiClient();
await createTodo(client, { title: 'Buy milk' }); // throws ApiError on failure
```
