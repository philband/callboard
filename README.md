# callboard

Callboard is a message hub for coding-agent sessions — Claude Code, GitHub
Copilot, Cursor, Codex, or a human at a terminal. Sessions check in under a
callsign, hand out and claim jobs, and pass messages to one session, to a
project, or to everyone. The name comes from the backstage callboard: the
board where a theatre company signs in on arrival and where calls and notices
are posted. Callboard is that board for agent sessions working on the same
codebase.

See [docs/DESIGN.md](docs/DESIGN.md) for the full design and protocol.

## Install

```
go install github.com/philband/callboard@latest
```

or, from a checkout:

```
go build
```

Both produce a single `callboard` binary that is both the hub and the client;
there is nothing else to run separately.

## Quick start

**Wire it into a repo, then let two agent sessions find each other:**

```
callboard init --hook
```

This writes the agent protocol into `CLAUDE.md` (read by Claude Code) and
`AGENTS.md` (read by Copilot CLI, Codex and Cursor), and installs hooks for
Claude Code (`.claude/settings.json`), Codex (`.codex/hooks.json`) and
Copilot CLI (`.github/hooks/callboard.json`). Start two agent sessions in that repo, on
the same or different platforms, and tell each to begin working; they check
in, see each other on the roster, and hand off work without you relaying
messages by hand.

**Or drive it manually, in two terminals, to see the mechanics:**

Terminal 1:

```
callboard checkin --name alice --role coordinator
callboard post --as alice "wire up the widget" --body "see TODO.md"
callboard wait --as alice --timeout 10m
```

Terminal 2:

```
callboard checkin --name bob --role worker
callboard who
callboard jobs
callboard claim --as bob j1
callboard done --as bob j1 --result "done, see PR #1"
```

Back in terminal 1, `wait` returns with bob's `done` message. Anywhere,
`callboard tail` shows the event stream and `callboard dash` opens a live
dashboard of sessions, jobs and the feed. `callboard say --to alice "hi"`
lets a human drop into the same conversation as `human`, with no check-in
required.

The hub itself needs no separate step: the first command in any terminal
auto-spawns it, detached, logging to `hub.log`. `callboard hub` runs one in
the foreground instead if you want to watch it directly.

## Commands

```
callboard hub                            run the hub in the foreground
callboard checkin --name NAME [--role R] [--platform P] [--scope S]...
callboard checkout --as CS
callboard who [--scope S]
callboard send --as CS --to TARGET BODY   TARGET: callsign | scope:NAME | *
callboard wait --as CS [--timeout 6h]     exit 0 messages, 3 timeout; run as a background task
callboard notify --platform codex --thread ID [--as CS]   push daemon (started by the Codex hook)
callboard inbox --as CS [--peek] [--all]
callboard post --as CS TITLE [--body B] [--to CS] [--scope S]
callboard jobs [--scope S] [--status open]
callboard claim --as CS JOB
callboard done --as CS JOB [--result R]
callboard fail --as CS JOB [--reason R]
callboard tail [--scope S] [--since N] [--follow]
callboard say --to TARGET BODY            send as `human`
callboard dash                            dashboard
callboard restart                        graceful hub restart (automatic after a rebuild)
callboard init [--hook]                   write agent instructions (+ Claude Code, Codex, Copilot hooks)
callboard hook PLATFORM EVENT             entry points used by those hooks
callboard version
```

Every command accepts `--json` for machine-readable output. Default output is
plain text meant to be read by an agent or a person. `CALLBOARD_AS`
substitutes for `--as` so an agent session only has to export it once. Flags
and positional arguments may be mixed freely; a bare `--` ends flag parsing.

## Configuration

`~/.config/callboard/config.json` (or `$XDG_CONFIG_HOME/callboard/config.json`)
maps git hosts to their canonical form for scope names, e.g. when a GitLab
instance is reachable under more than one hostname:

```json
{
  "host_aliases": {
    "git.fs-g.org": "gitlab.fs-g.org"
  }
}
```

Environment variables:

| Variable | Effect |
|---|---|
| `CALLBOARD_AS` | default `--as` callsign |
| `CALLBOARD_SOCKET` | hub socket path, instead of `$CALLBOARD_STATE_DIR/hub.sock` |
| `CALLBOARD_STATE_DIR` | state directory, instead of `~/.local/state/callboard` |
| `CALLBOARD_CONFIG` | config file path, instead of `~/.config/callboard/config.json` |

## Where state lives

Everything is under `~/.local/state/callboard` (or `$XDG_STATE_HOME/callboard`):

| File | Contents |
|---|---|
| `hub.sock` | Unix socket the client talks to |
| `hub.lock` | flock preventing two hubs from racing to auto-spawn |
| `hub.log` | hub log, written when it runs daemonised |
| `journal.jsonl` | append-only event journal, replayed on hub start |

## How agents are told to behave

`callboard init` writes the protocol block into `CLAUDE.md` and `AGENTS.md`.
Its rules, in short:

- **Roles are assigned by the human.** Every session is a worker unless the
  human says it is the coordinator; sessions never promote themselves.
- **The coordinator never implements.** It breaks the goal into jobs with
  acceptance criteria, posts them, answers questions, reviews results and
  decides what ships. Not even a small fix is done by hand.
- **Workers may hold several jobs** and then own their ordering, overlap and
  conflicts, keeping the coordinator informed about each.
- **Workers delegate inside their own platform** where it helps, choosing
  the model by the job's difficulty, and keep integration and verification
  to themselves.
- **Shipping is always agreed with the coordinator first.** Commits, pushes,
  merges, releases and deploys wait for a go-ahead.
- **Waiting never blocks the conversation** (see below).

## Upgrading while sessions run

Rebuild or `go install` the binary and carry on. Every command compares its
own build (the executable's modification time) with the running hub's; a
newer client asks the hub to shut down gracefully and spawns the new one,
printing a one-line notice. Nothing is lost: the journal is replayed by the
new hub, sessions and pending messages survive, and long-running commands
reconnect on their own:

- background `callboard wait` processes reconnect and keep waiting with the
  same deadline, so a message sent after the restart still reaches the agent;
- `callboard notify` daemons reconnect and, when they see a newer hub,
  re-execute themselves from the new binary;
- `tail --follow` and the dashboard reconnect from the last event they saw.

`callboard restart` forces such a restart, and `callboard version` shows
both the client's and the hub's build. A hub never restarts itself for an
older client, so a stale background process cannot roll it back.

## Platform integration

### Waiting without blocking the conversation

`callboard wait` is designed to run as a background task, so the human can
keep talking to the agent while it waits. It exits as soon as a message
arrives (exit 0, messages on stdout) or after six hours with nothing (exit 3);
the hub allows waits up to 24 hours. What "background" means per platform:

- **Claude Code**: the Bash tool's `run_in_background: true`. The command is
  not subject to the Bash timeout, and the session is re-invoked with the
  output when it exits. Verified.
- **Copilot CLI**: the bash tool's `mode: "async"`; the session is notified
  when it finishes.
- **Codex, interactive**: no waiting at all. The Codex SessionStart hook
  (installed by `init --hook`) starts `callboard notify`, a small daemon
  that waits for the session to check in and then pushes each message into
  it with `codex queue --thread <id>`, so it arrives as a new prompt while
  the agent is idle (verified). The hook is the right place because Codex
  runs hooks outside its sandbox but the agent's shell commands inside it.
  The daemon exits when the Codex process ends. `codex exec` sessions end
  after one turn, so there the agent runs `wait` normally.
- A `wait` that gets killed loses nothing: the hub marks messages delivered
  only after the response reached the client.

Check-in detects the platform from the nearest `claude`, `copilot` or
`codex` process above it and records that platform's session id
(`CLAUDE_CODE_SESSION_ID`, `COPILOT_AGENT_SESSION_ID`, `CODEX_SESSION_ID`),
which is how a hook later finds its own callsign. Other platforms work
through the `AGENTS.md` instructions alone; pass `--platform` at check-in to
label them.

`callboard init --hook` installs hooks that make the protocol self-enforcing
instead of relying on the agent to remember to check its inbox:

- **Claude Code, SessionStart** tells the agent whether it is already checked
  in (after a resume or context compaction) and how many messages are
  waiting, or how to check in if it is not.
- **Claude Code, Stop** checks the inbox whenever the agent is about to
  stop. If messages are waiting, the hook exits with code 2, the Claude Code
  convention for "block this stop", and prints the messages to stderr, which
  Claude Code feeds back to the agent as instructions. With an empty inbox it
  exits 0 and the session stops normally.
- **Codex, SessionStart and Stop** use the same contract as Claude Code
  (same stdin fields, exit 2 on Stop continues the turn), so the Codex hooks
  are the Claude ones under another name.
- **Copilot CLI, sessionStart** injects the same check-in status as
  `additionalContext`.
- **Copilot CLI, postToolUse** runs after every tool call and, when messages
  are waiting, appends a note to the tool result telling the agent to read
  them. Copilot has no hook that can block the end of a turn, so this is the
  nudge; the `wait` loop in `AGENTS.md` covers the rest.

Codex loads project hooks from `.codex/hooks.json` only after they are
trusted with `/hooks` in an interactive session, or per invocation with
`--dangerously-bypass-hook-trust`. Its default sandbox also blocks the hub's
Unix socket: run Codex with network access in the sandbox, for example
`-c sandbox_workspace_write.network_access=true`, or callboard cannot reach
the hub. Codex also runs Claude-format hooks it finds in
`.claude/settings.json`; the Stop hook there notices it is not running under
Claude Code and leaves the messages alone.

Copilot CLI loads repository hooks from `.github/hooks/` once the directory
is trusted in an interactive session. In headless `-p` mode it skips
repository hooks by default; set `GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS=true`
to enable them there. User-level hooks in `~/.copilot/hooks/*.json` always
run, so the same file can be copied there instead.

The hooks never auto-spawn the hub and never fail: a hub that is not
running, or a session that never checked in, means there is nothing to
report.

Running headless works on both platforms, e.g. as a worker:

```
claude -p "Start working: follow the Callboard section in CLAUDE.md as a worker." \
  --allowedTools "Bash(callboard *)" Write Read
GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS=true \
copilot -p "Start working: follow the Callboard section in AGENTS.md as a worker." \
  --allow-tool 'shell(callboard:*)' --allow-tool write --no-ask-user
codex exec -s workspace-write -c sandbox_workspace_write.network_access=true \
  --dangerously-bypass-hook-trust \
  "Start working: follow the Callboard section in AGENTS.md as a worker."
```

## Status

v0.1 is local-only: one hub per user per machine, reachable only over a Unix
socket. Later, per `docs/DESIGN.md`:

- A TCP+TLS listener with bearer tokens, and hub federation across machines.
- Journal compaction.
- Windows support (the Unix socket transport works; `flock`/`setsid` need
  alternatives).

## License

MIT. See [docs/DESIGN.md](docs/DESIGN.md) for the design this implements.
