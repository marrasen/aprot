# Contributing to aprot

Most of this file is about **writing** — issues, pull requests, commit
messages. Code rules live in [`CLAUDE.md`](CLAUDE.md) (build and test
commands) and [`docs/scope.md`](docs/scope.md) (what belongs in aprot at
all).

The reason this file exists: issues in this repo drifted into a register
nobody wants to read. Titles announced a category instead of a problem,
bodies argued with earlier bodies, and the actual defect was three
paragraphs in. That wastes the reader's time and hides the point.

## The one rule

**Write for a tired colleague who knows Go, has not read the linked
issues, and wants to know what is broken.**

They should reach the point in the first two sentences. Everything after
that is support.

## Issues

Four things, in this order:

1. **What is wrong, or what you want.** One or two plain sentences.
2. **Where.** `file.go:line`. Name the version or commit if it matters.
3. **How to see it.** A repro, a snippet, or the observed output. Skip if
   it is a proposal rather than a bug.
4. **What should happen instead**, or — if it is a judgment call — the
   options and what you would pick.

If an issue asks for a decision rather than a fix, say so and list the
choices. "Decide: add it, or write down why not" is a good ending.

### Titles

Say the problem. Not its category.

| Don't | Do |
| --- | --- |
| `Detached connection hygiene: the dropped accessor and the unconditional flag` | `Two unanswered questions about detached connections` |
| `Observability gaps in the dispatch path` | `A stuck handler is invisible — no way to list in-flight requests` |
| `Identity has no home in aprot` | `REST and MCP disagree about where "who is asking" lives` |

Aim for something a person could repeat out loud. Under ~70 characters.
No `Category: subcategory — detail` prefixes.

### Words to cut

The left column shows up when someone is reaching for gravity. The right
column is what they meant.

| Don't write | Write |
| --- | --- |
| hygiene | say the actual problem |
| amnesia, institutional memory | "we forgot", "nobody wrote it down" |
| supersedes | replaces |
| surface (as a verb) | show, report, log |
| leverage | use |
| non-trivial | hard — or say how hard |
| the thesis, the charter, the arbiter | delete |
| holistic, robust, seamless, elegant | delete |
| it is worth noting that | delete |
| fundamentally, critically, importantly | delete |

Two habits behind most of it:

- **Metaphor instead of behavior.** Code does a specific thing. Say the
  thing. "The flag goes stale" → "the flag stays `true` after logout".
- **Arguing with the last issue.** Sections like "What #328 got wrong"
  and "The accurate version is a better argument" are a debate transcript.
  Make the current argument; link the old issue in one line if the reader
  needs it.

Our own vocabulary — principal, detached connection, subscription hook,
push fan-out — is fine. It names real things in this codebase. Imported
vocabulary that exists to sound serious is not.

### Background

Assume the reader has not opened your links. One sentence of background
per concept is usually enough:

> `Server.NewDetachedConn()` returns a `*Conn` with no socket behind it.
> It carries a user ID and per-connection values, so middleware written
> against real connections keeps working, but anything you try to send to
> it fails with `ErrDetachedConn`.

Only tell the history of a discussion when the history *is* the point —
for example, when you are recording that something was dropped without a
decision.

## Pull requests

Title: imperative, says what changes. `Report in-flight requests so a
stuck handler is visible`, not `Observability improvements`.

Body, in this order:

- **What changed.** Bullets. Group by behavior, not by file — the
  reviewer already has the diff.
- **Why.** Link the issue (`Fixes #342`). If there is no issue, put the
  reason here.
- **Breaking changes.** Call them out under their own heading, with the
  before/after a caller has to write.
- **How to verify.** The commands you actually ran, and what they said.

Also:

- **Say what you did not do.** Deferred items, known gaps, things you
  chose to leave. A reviewer finding these on their own is worse.
- **Never claim a check you did not run.** If `go test ./...` fails, say
  which test and paste the failure. A PR body that says "all tests pass"
  when they were never run is the one thing here that is actually
  expensive.
- **Keep it current.** When you push changes, update the description.

## Commits

- Subject: imperative, under 72 characters.
- Body: why, not what. The diff covers what.
- **Never commit directly to `master`.** Branch, then PR.
- **Never force push.** Add a new commit instead.

## Before you open a PR

```bash
gofmt -w .           # CI fails on unformatted Go
go test ./...
```

If you touched `templates/` or the generator, regenerate and commit the
example clients — CI diffs them:

```bash
cd example/vanilla/tools/generate && go run main.go
cd example/react/tools/generate && go run main.go
cd example/react/client && npx tsc --noEmit
```

For a user-facing change, update all three: `README.md`, `doc.go`, and
`APROT_AI.md`. For new public API, check `docs/scope.md` first and record
the ruling there if it was a close call.

## If you are an AI agent

You are the main reason this file exists. Specifically:

- Write the finding, not the case for the finding. No opening thesis, no
  restating the problem in three registers, no closing summary of what
  you just said.
- Do not add structure the content does not need. Three headed sections
  for two bullet points is noise.
- Do not soften or inflate. "This is a correctness bug" or "this is a
  style preference" — whichever it is, say it once, plainly.
- Report what you ran and what it printed. Never describe a test run that
  did not happen.
