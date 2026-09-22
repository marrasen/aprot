# Contributing to aprot

This file is about **writing**: issues, pull requests, commit messages,
and the text the library itself says. Code rules live in
[`CLAUDE.md`](CLAUDE.md) (build and test commands) and
[`docs/scope.md`](docs/scope.md) (what belongs in aprot at all).

## The one rule

**Write for a tired colleague who knows Go, has not read the linked
issues, and wants to know what is broken.**

They reach the point in the first two sentences. Everything after that
is support.

## Six habits

Everything below follows from these.

1. **Say what it is, not what it is not — when describing behavior.**
   "Returns nil when anonymous", not "does not return a user when nobody
   is signed in". "Rather than", "instead of" and "not X but Y" nearly
   always mark a sentence that can be halved. Two exceptions: a rule is
   clearest in the negative ("don't gate auth on it"), and a changelog
   entry is a statement about what stopped being true, where "no longer"
   is exactly the right words.
2. **Say the behavior, not a metaphor for it.** Code does a specific
   thing. "The flag goes stale" → "the flag stays `true` after logout".
3. **Use the word everyone else uses.** Invalid, failed, not found,
   closed, cancelled, removed, replaced. Reaching for a rarer word adds
   gravity and removes meaning.
4. **The title is the message.** The body adds only what the title
   cannot hold: where, how to see it, what should happen.
5. **State the finding, not the search.** "I first thought X, then
   checked Y" is a diary. Say what you found; the trail goes in a link
   or a footnote if anyone will need it.
6. **Do not argue with the last issue.** Sections like "What #328 got
   wrong" are a debate transcript. Make the current case; link the old
   issue in one line if the reader needs it.

## Issues

Four things, in this order:

1. **What is wrong, or what you want.** One or two plain sentences.
2. **Where.** `file.go:line`. The version or commit if it matters.
3. **How to see it.** A repro, a snippet, or the observed output. Skip
   for a proposal.
4. **What should happen instead.** For a judgment call, the options and
   which you would pick.

An issue that asks for a decision says so and lists the choices.
"Decide: add it, or write down why not" is a good ending.

No headers in an issue under a screen long. No bold except on the
sentence that says what is broken.

### Titles

Say the problem. Not its category.

| Don't | Do |
| --- | --- |
| `Detached connection hygiene: the dropped accessor and the unconditional flag` | two issues: `A caller can't tell a detached Conn from a real one` and `NewDetachedConn always marks the connection authenticated` |
| `Observability gaps in the dispatch path` | `A stuck handler is invisible — no way to list in-flight requests` |
| `Identity has no home in aprot` | `REST and MCP disagree about where "who is asking" lives` |

Something a person could repeat out loud, under ~70 characters, in the
present tense. No `Category: subcategory — detail` prefixes.

One problem per issue. A title that counts ("two questions about",
"a handful of") is that many issues; each gets its own number so each
can be closed with its own decision. Where the questions came from is
one line at the bottom, not the opening paragraph.

## Pull requests

- **Title:** the commit subject, when there is one commit. Otherwise
  the one change the reviewer will remember it by.
- **First line of the body:** what changed, as a fact. "REST requests
  now run through `Server.Invoke`." Not "This PR refactors…".
- **Then:** what to look at first, and how it was tested. Three lines
  is usually enough.
- **Fixes #n** on its own line when it closes an issue. The issue holds
  the problem; the PR does not repeat it.

## Commit messages

- **Subject:** imperative, ≤ 60 characters, says the change.
  `Cancel in-flight requests on DisconnectUser`, not `Fix bug` and not
  `Connection hygiene`.
- **Body:** why, in two or three sentences, when the subject cannot say
  it. What the diff shows is not repeated.
- One change per commit where practical. A subject that says `and`
  twice, or joins two changes with a semicolon, is two commits.

## Before you open a PR

```bash
gofmt -w .        # CI fails on unformatted Go
go test ./...
```

Touched `templates/` or the generator? Regenerate and commit the example
clients — CI diffs them and fails on any difference:

```bash
cd example/vanilla/tools/generate && go run main.go
cd example/react/tools/generate && go run main.go
cd example/react/client && npx tsc --noEmit
```

A user-facing change updates all three of `README.md`, `doc.go` and
`APROT_AI.md`. New public API is checked against `docs/scope.md` first,
and a close call is recorded there.

## Text the library says

Error messages, log lines, doc comments and generated-code comments are
read more often than any issue. The six habits apply, plus:

- **Error strings** follow Go: lowercase, no trailing punctuation, the
  fact and nothing else. `title is required`, `invalid token`,
  `handler panicked`. Never a hint about what the caller should have
  done; that is the doc comment's job.
- **Doc comments** open with the identifier and what it does, in one
  sentence. Constraints and edge cases come after, each in its own
  sentence. "Worth knowing", "note that" and "deliberately" are cut;
  the sentence that follows them is the content.
- **A warning states the consequence** in one sentence, only where an
  action exposes something or cannot be undone. Everything else is a
  fact, not a warning.
- **User-facing strings never explain the library's reasoning.** The
  reader needs the fact, not why aprot had to choose it.

## Words to cut

The left column shows up when someone is reaching for gravity. The
right column is what they meant.

| Don't write | Write |
| --- | --- |
| hygiene | the actual problem |
| amnesia, institutional memory | "we forgot", "nobody wrote it down" |
| supersedes | replaces |
| surface (as a verb) | show, report, log, return |
| leverage | use |
| non-trivial | hard — or say how hard |
| footgun, sharp edge | what breaks, and when |
| seam | the function or type |
| the trade-off is honest | delete |
| deliberately, on purpose | delete, or one clause of why |
| the thesis, the charter, the arbiter | delete |
| holistic, robust, seamless, elegant | delete |
| it is worth noting that, worth knowing | delete |
| fundamentally, critically, importantly | delete |
| rather than, instead of, not X but Y | say what it is |

## Our own vocabulary

aprot has names for its things. Use them exactly, one name per thing,
and do not coin a synonym for variety.

| Term | Means | Not |
| --- | --- | --- |
| principal | who is asking; the authorization input | identity, user, caller |
| address, `UserID` | where a user is reachable for push | user, identity |
| connection | a live socket, or a detached one | session, client |
| detached connection | a `Conn` bound to no socket, for REST and MCP | fake, virtual, synthetic connection |
| subscription | a query a client is kept current on | listener, watcher |
| trigger key | the name a query registers and a mutation fires | topic, channel, event |
| refresh | re-run a subscribed query and send the result | update, invalidate |
| patch | a partial update pushed to a subscription | delta, diff |
| push event | a broadcast that is not a query result | message, notification |
| handler | a Go method the registry serves | endpoint, action, RPC |
| group | the struct a handler hangs on | service, controller |
| transport | WebSocket, SSE+HTTP, REST, MCP | protocol, channel |

When a new thing needs a name, name it once in `docs/scope.md`, then
use that name everywhere, including in the generated TypeScript.

## If you are an AI agent

This file exists because AI-written issues in this repo stopped being
readable. The six habits cover most of it. Three failures are yours
specifically:

- **Never describe a check you did not run.** Report the command and
  what it printed. A PR body claiming the tests pass when they were
  never run is the one thing here that costs real money to discover.
- **Do not add structure the content does not need.** Three headed
  sections for two bullet points is noise. A short issue is prose.
- **State the finding once.** Not an opening thesis, the finding, and a
  closing summary of what you just said. The reader got it the first
  time.
