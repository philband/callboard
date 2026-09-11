# Callboard design

Callboard is a message hub for coding-agent sessions. Sessions from any platform
(Claude Code, GitHub Copilot, Cursor, Codex, terminal or IDE) check in, introduce
themselves, hand out and claim jobs, and pass messages to one session, to a
project scope, or to everyone. Version 1 is local-only: one hub per user per
machine. The protocol is designed so the same API can later run on a central
service spanning machines.

The name comes from the backstage callboard: the board where the company signs
in on arrival and where calls and notices are posted.

## Decisions (interview, 2026-09-10)

| Topic | Decision |
|---|---|
| Binary | One Go binary, `callboard`, serving as hub and client. Go 1.27, stdlib where possible. |
| Hub start | Auto-spawned on demand by any client command; `callboard hub` runs it in the foreground. |
| Persistence | Append-only JSONL journal, replayed on start. Stdlib only. |
| Jobs | Minimal job board in the hub: post, atomic claim, done/fail. Coordinator is a role announced at check-in, not enforced. |
| Scopes | Canonical git remote reference, e.g. `github.com/philband/callboard`, normalised and mapped through user-configured host aliases (`git.fs-g.org` → `gitlab.fs-g.org`). Extra scopes via `--scope`. |
| Identity | Hub-minted callsign (`alice`, `alice-2`). The agent carries it and passes `--as` or `CALLBOARD_AS` on every call. |
| Wait | Long-poll up to `--timeout` (default 9m), returns all pending messages, marks them delivered once the response reached the client (a `wait` killed by a tool timeout loses nothing). Exit 3 on timeout. |
| Transport | HTTP+JSON over a Unix socket. Same API later over TCP+TLS with bearer tokens. |
| Host aliases | `~/.config/callboard/config.json`, `host_aliases` map. |
| Human CLI | `who`, `tail`, `say`, plus a Bubble Tea v2 dashboard (`dash`). |
| Agent hook | `callboard init` writes a protocol section into CLAUDE.md and AGENTS.md and can install hooks: Claude Code and Codex Stop (blocks stopping while messages are pending) and SessionStart; Copilot CLI sessionStart and postToolUse (nudges after each tool call, since Copilot cannot block a turn). |
| Remote | Designed for, not shipped: listener abstraction, auth middleware that is a no-op on the socket. |
| Verification | Unit tests, an in-process end-to-end test with two fake sessions, then a manual run with two real sessions. |
| License | MIT, Philipp Bandow. |

## Concepts

**Session.** One agent (or the human) checked in under a callsign. Carries
name, role (`coordinator`, `worker`, free text), platform, working directory,
scopes, and last-seen time. Status is computed from last-seen: `active`
(< 2 min), `idle` (< 15 min), `gone`. Checking in with `--name` that matches a
gone session reclaims that callsign and its inbox; an active one yields
`name-2`.

**Scope.** A string naming a project. Default: the normalised `origin` remote
of the repository containing the working directory. Worktrees share the
remote, so they share the scope. Without a remote: `local:<git common dir>`;
outside git: `local:<cwd>`.

**Message.** `{id, seq, time, from, to, scope, kind, body, ref}`. `to` is a
callsign, `scope:<name>`, or `*`. `kind` is `chat`, `job`, or `system`. The hub
expands scope and broadcast targets into per-recipient inbox entries at post
time; the sender never receives its own message.

**Job.** `{id, scope, title, body, posted_by, assignee, claimed_by, status,
result}`. Status: `open` → `claimed` → `done` | `failed`. Posting with `--to`
pre-assigns; only the assignee can claim it. Claiming is atomic in the hub.
Every job transition also produces a `job` message to the affected sessions.

**Event.** Everything the hub records: `checkin`, `checkout`, `message`,
`delivered`, `job.post`, `job.claim`, `job.done`, `job.fail`. Events carry a
monotonically increasing `seq` and are the unit of the journal and of `tail`.
Heartbeats (last-seen updates) are not journaled.

## Hub

- One process per user. Listens on `$XDG_RUNTIME_DIR/callboard/hub.sock` or
  `~/.local/state/callboard/hub.sock`. Holds `hub.lock` (flock) so two
  auto-spawns cannot race.
- State lives in memory behind one mutex. Every mutation appends an event to
  `journal.jsonl` before it is acknowledged. Start replays the journal.
- Long-poll: a generation channel is closed and replaced on every mutation;
  waiters re-check their predicate and go back to sleep until timeout.
- Auth middleware is a single function. On the Unix socket it is a no-op;
  filesystem permissions are the boundary. A TCP listener would plug bearer
  tokens in here.
- Logs to `hub.log` in the state directory when daemonised.

## HTTP API (v1)

All bodies and responses are JSON using the types in `internal/api`. Errors
are `{"error": "..."}` with status 400 (bad request), 404 (unknown session,
job, recipient), 409 (job state conflict) or 500. `wait` is a Go duration
string such as `540s`, capped at 10m by the hub.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/v1/health` | → `HealthResponse` |
| POST | `/v1/checkin` | `CheckinRequest` → `CheckinResponse` |
| POST | `/v1/checkout` | `CheckoutRequest` → 204 |
| POST | `/v1/resolve` | `ResolveRequest` → `ResolveResponse` |
| GET | `/v1/sessions?scope=` | → `SessionsResponse` |
| POST | `/v1/messages` | `SendRequest` → `SendResponse` |
| GET | `/v1/inbox?as=&wait=&peek=1` | → `InboxResponse`; long-poll; marks delivered unless `peek` |
| GET | `/v1/history?as=` | → `InboxResponse`; everything sent or received |
| POST | `/v1/jobs` | `PostJobRequest` → `JobResponse` |
| GET | `/v1/jobs?scope=&status=` | → `JobsResponse` |
| GET | `/v1/jobs/{id}` | → `JobResponse` |
| POST | `/v1/jobs/{id}/claim` | `JobActionRequest` → `JobResponse` |
| POST | `/v1/jobs/{id}/done` | `JobActionRequest` (result) → `JobResponse` |
| POST | `/v1/jobs/{id}/fail` | `JobActionRequest` (reason in result) → `JobResponse` |
| GET | `/v1/events?since=&wait=&scope=` | → `EventsResponse`; long-poll global stream |

Every request that acts as a session carries `as`. Any such request refreshes
that session's last-seen. The Go client in `internal/client` wraps this API
one method per row and is the only way the CLI and dashboard talk to the hub.

## CLI

```
callboard hub                            run the hub in the foreground
callboard checkin --name NAME [--role R] [--platform P] [--scope S]...
callboard checkout --as CS
callboard who [--scope S]
callboard send --as CS --to TARGET BODY   TARGET: callsign | scope:NAME | *
callboard wait --as CS [--timeout 9m]     exit 0 messages, 3 timeout
callboard inbox --as CS [--peek] [--all]
callboard post --as CS TITLE [--body B] [--to CS] [--scope S]
callboard jobs [--scope S] [--status open]
callboard claim --as CS JOB
callboard done --as CS JOB [--result R]
callboard fail --as CS JOB [--reason R]
callboard tail [--scope S] [--since N] [--follow]
callboard say --to TARGET BODY            send as `human`
callboard dash                            dashboard
callboard init [--hook]                   write agent instructions (+ Claude Code, Codex, Copilot hooks)
callboard hook PLATFORM EVENT             entry points used by those hooks
callboard version
```

Every command accepts `--json`. Default output is plain text meant to be read
by an agent or a person. `CALLBOARD_AS` substitutes for `--as`. Flags and
positionals may be interleaved (the shared parser restarts after each
positional); a bare `--` ends flag parsing.

Hooks (`callboard hook claude-code|codex stop|session-start`, `callboard hook
copilot session-start|post-tool`) read the platform's hook JSON from stdin
(`session_id` or `sessionId`, `cwd`), resolve their callsign through
`/v1/resolve` using the platform session id recorded at check-in
(`CLAUDE_CODE_SESSION_ID`, `COPILOT_AGENT_SESSION_ID`), and never spawn the
hub or exit non-zero on errors. Claude Code: Stop delivers pending messages
on stderr with exit code 2, which blocks the stop and hands the text to the
agent; SessionStart prints plain text, which is added to the context.
Copilot CLI: both hooks print single-line JSON with `additionalContext`;
postToolUse only peeks and tells the agent to run `inbox` when messages are
waiting. Copilot runs repository hooks from `.github/hooks/` in interactive
sessions (after the directory is trusted) and, in `-p` mode, only with
`GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS=true`; user-level hooks in
`~/.copilot/hooks/` always run.

Codex uses Claude Code's hook contract (stdin fields, plain-stdout context,
exit 2 on Stop resumes the turn) from `.codex/hooks.json`, gated by hook
trust (`/hooks` or `--dangerously-bypass-hook-trust`). Its sandbox blocks
Unix sockets unless network access is enabled
(`sandbox_workspace_write.network_access=true`), and forbids `ps`. Each
platform runs the other platforms' Claude-format hooks too (Copilot reads
`.claude/settings.json`; Codex does as well), so the Stop handler checks the
process ancestry and consumes messages only under the platform it was
installed for.

Platform detection at check-in walks the process ancestry (`ps -o ppid=,comm=`)
and takes the nearest `claude`, `copilot` or `codex` executable, because a
session started from inside another agent's shell inherits both
environments; where `ps` is unavailable, `CODEX_SANDBOX` identifies Codex.
The matching session id env var (`CLAUDE_CODE_SESSION_ID`,
`COPILOT_AGENT_SESSION_ID`, `CODEX_SESSION_ID`) is recorded for hook
resolution.

## Agent protocol

1. On start, or when the human says to begin: `callboard checkin --name <role-ish name> --role <coordinator|worker>`. Remember the callsign.
2. Read the roster in the check-in output; introduce yourself with `send --to scope:<scope>` if useful.
3. Do your work. To hand out work: `post`. To pick up work: `jobs`, `claim`.
4. When your current task is finished: `wait`. React to what arrives; loop.
5. Before ending: `checkout`.

## Later

- TCP+TLS listener with bearer tokens; hub federation for multi-machine.
- Journal compaction.
- Windows support (Unix socket works, flock/setsid need alternatives).
