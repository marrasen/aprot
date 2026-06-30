# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file was introduced at v0.44.0; for the history of earlier releases see the
[git tags](https://github.com/marrasen/aprot/tags) and GitHub releases.

## [Unreleased]

### Added

- Task lifecycle middleware: `tasks.Enable(registry, tasks.WithTaskMiddleware(mw))`,
  with `TaskMiddleware` / `TaskInfo` and ctx propagation through nested subtasks (#205, #211).
- Configurable request-param logging in the vanilla `LoggingMiddleware` (#212).
- Server hardening options on `ServerOptions` — `MaxMessageSize`, `WriteTimeout`,
  `PingInterval`, `PongTimeout` — plus WebSocket ping/pong keepalive (`-1` disables) (#208).
- Runtime JSON unwrapping for generic `sql.Null[T]` for the common `T`
  (`string`, `int`, `int64`, `int32`, `int16`, `float64`, `bool`, `time.Time`) (#213).
- CI: `govulncheck` and `gosec` jobs, and a Dependabot config for the Go modules,
  GitHub Actions, and the e2e / react-client npm packages (#207 P3).

### Changed

- TypeScript type mapping: a bare pointer `*T` (no `json:,omitempty`) now maps to
  `T | null` (required), `map[bool]V` to `Partial<Record<"true" | "false", V>>`,
  and `json.RawMessage` to `unknown` (#213).
- OpenAPI output is now valid 3.0.3: boolean `exclusiveMinimum`/`exclusiveMaximum`
  paired with `minimum`/`maximum`, `minItems`/`maxItems` for slice bounds, and
  `arg0`/`arg1` fallback path-param names matching the REST adapter (#213).
- `NewServer` clamps a misconfigured `PongTimeout` (≤ `PingInterval`) up to
  `2*PingInterval` so healthy connections aren't dropped (#207 P3).
- `SetCheckOrigin` (CSWSH mitigation) is now documented in README, doc.go, and APROT_AI.md (#208).

### Fixed

- Stalled-client deadlock (per-frame write timeout; sends snapshotted outside the
  server lock), handler panic recovery on request/subscribe/refresh paths, and
  inbound size limits for WebSocket and SSE (#208).
- REST scalar path params and `sql.Null`-aware REST response marshaling (#208).
- Detached shared tasks (`context.WithoutCancel`) no longer auto-finalized (#208).
- Runtime lifecycle correctness: late-register races, `Server.Stop` hang, `userConns`
  leak on connect-hook rejection, subscription re-register / stale params, and small
  data races (#209).
- `tasks/` subpackage: `lastProgress` leak + trailing-flush, `ensureRoot` race,
  nil `*Task` on the REST path, `SharedSubTask` duplicate node, and owner-only
  shared-task cancel (#210).
- Zod codegen: unescaped string injection (enum values, `contains`/`startswith`/`endswith`),
  kind-aware `gt`/`lt`/`min`/`max`, `oneof` honored, and recursive schemas via `z.lazy()` (#213).
- Codegen rejects `time.Duration` fields at generation time (no default JSON
  representation in the v2 encoder) unless a `format:` option is set (#213).
- Kebab/camel acronym digit handling (`API2Handlers`) (#213).
- Invalid TypeScript identifiers are sanitized: non-identifier field keys are quoted,
  reserved-word param names are suffixed, and instantiated generic type names
  (`Box[int]`) are folded into valid identifiers everywhere they are emitted (#213).

### Security

- Static analysis (`gosec`) and vulnerability scanning (`govulncheck`) added to CI (#207 P3).

[Unreleased]: https://github.com/marrasen/aprot/compare/v0.44.0...HEAD
