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

This writes the agent protocol into `CLAUDE.md` and `AGENTS.md` (whichever
exist, or a new `CLAUDE.md`) and installs Claude Code `SessionStart`/`Stop` hooks in
`.claude/settings.json`. Start two Claude Code sessions in that repo and tell
each to begin working; they check in, see each other on the roster, and hand
off work through the hooks without you relaying messages by hand.

**Or drive it manually, in two terminals, to see the mechanics:**

Terminal 1:

```
callboard checkin --name alice --role coordinator
callboard post --as alice "wire up the widget" --body "see TODO.md"
callboard wait --as alice
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
callboard init [--hook]                   write agent instructions (+ Claude Code hooks)
callboard hook stop|session-start         entry points used by those hooks
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

## Claude Code hooks

`callboard init --hook` installs two hooks that make the protocol
self-enforcing instead of relying on the agent to remember to check its
inbox:

- **SessionStart** tells the agent whether it is already checked in (after a
  resume or context compaction) and how many messages are waiting, or how to
  check in if it is not.
- **Stop** checks the session's inbox whenever the agent is about to stop.
  If messages are waiting, the hook exits with code 2, the Claude Code
  convention for "block this stop", and prints the messages to stderr, which
  Claude Code feeds back to the agent as instructions. With an empty inbox
  it exits 0 and the session stops normally.

Check-in records Claude Code's session id (`CLAUDE_CODE_SESSION_ID`), which
is how a hook finds its own callsign. The hooks never auto-spawn the hub and
never fail: a hub that is not running, or a session that never checked in,
means there is nothing to report.

## Status

v0.1 is local-only: one hub per user per machine, reachable only over a Unix
socket. Later, per `docs/DESIGN.md`:

- A TCP+TLS listener with bearer tokens, and hub federation across machines.
- Journal compaction.
- Windows support (the Unix socket transport works; `flock`/`setsid` need
  alternatives).

## License

MIT. See [docs/DESIGN.md](docs/DESIGN.md) for the design this implements.
