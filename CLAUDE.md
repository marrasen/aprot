# CLAUDE.md

## Build and Test Commands

```bash
go test ./...
go test -run TestName ./...
```

### Generate & verify TypeScript clients after template changes

```bash
cd example/vanilla/tools/generate && go run main.go
cd example/react/tools/generate && go run main.go
cd example/react/client && npx tsc --noEmit
```

### Run examples

```bash
# Vanilla
cd example/vanilla/server && go run .

# React (both needed)
cd example/react/server && go run .
cd example/react/client && npm run dev
```

## Rules

- **Never commit directly to master.** Always use a branch + PR.
- **Never force push.** Always create new commits instead of amending.
- **Always update PRs and issues** when pushing changes — keep descriptions current.
- **Always update `README.md`** when adding or changing user-facing features.
