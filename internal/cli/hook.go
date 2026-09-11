package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/client"
	"github.com/philband/callboard/internal/config"
)

func init() {
	register("hook", "agent-platform hook entry points (installed by `callboard init --hook`)", runHook)
}

// exitBlockStop is Claude Code's Stop-hook contract: exiting 2 blocks the
// stop and feeds stderr back to Claude as instructions. It happens to equal
// ExitUsage numerically; the meaning here is unrelated.
const exitBlockStop = 2

// hookTimeout bounds every hub call a hook makes. Hooks run on the agent's
// critical path and must never hang it.
const hookTimeout = 5 * time.Second

// Hooks never auto-spawn the hub and never exit non-zero because of an
// error: a hub that is not running, or a session that never checked in,
// means there is nothing to report.
func runHook(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: callboard hook claude-code|codex stop|session-start")
		fmt.Fprintln(os.Stderr, "       callboard hook copilot session-start|post-tool")
		return ExitUsage
	}
	// Codex uses Claude Code's hook contract: same stdin fields, plain
	// stdout as context, exit 2 on Stop to continue the turn.
	switch args[0] + " " + args[1] {
	case "claude-code stop", "codex stop":
		return hookClaudeStop(args[0])
	case "claude-code session-start", "codex session-start":
		return hookClaudeSessionStart()
	case "copilot session-start":
		return hookCopilotSessionStart()
	case "copilot post-tool":
		return hookCopilotPostTool()
	default:
		fmt.Fprintf(os.Stderr, "callboard: unknown hook %q %q\n", args[0], args[1])
		return ExitUsage
	}
}

// hookDebug writes a diagnostic line to stderr only when CALLBOARD_DEBUG is
// set. Hooks otherwise fail silently.
func hookDebug(format string, args ...any) {
	if os.Getenv("CALLBOARD_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "callboard: "+format+"\n", args...)
}

// hookInput is the union of the fields callboard reads from any platform's
// hook JSON. Claude Code uses snake_case, Copilot CLI camelCase.
type hookInput struct {
	SessionID      string `json:"session_id"`
	SessionIDCamel string `json:"sessionId"`
	Cwd            string `json:"cwd"`
	StopHookActive bool   `json:"stop_hook_active"`
}

func (in hookInput) id() string {
	if in.SessionID != "" {
		return in.SessionID
	}
	return in.SessionIDCamel
}

// readHookInput decodes stdin; a failure yields the zero value, which still
// resolves by cwd where that is unambiguous.
func readHookInput() hookInput {
	var in hookInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		hookDebug("reading hook input: %v", err)
	}
	return in
}

// hookSession resolves the calling session, or returns ok=false when there
// is nothing to report (hub down, not checked in).
func hookSession(ctx context.Context, c *client.Client, in hookInput) (api.Session, bool) {
	s, err := c.Resolve(ctx, api.ResolveRequest{PlatformSession: in.id(), Cwd: in.Cwd})
	if err != nil {
		hookDebug("resolve: %v", err)
		return api.Session{}, false
	}
	return s, true
}

const hookNotCheckedIn = "Callboard is available for coordinating with other agent sessions on this project; " +
	"check in with `callboard checkin --name <name> --role <coordinator|worker>` when you start working."

// hookStartContext is the session-start text for both platforms.
func hookStartContext(ctx context.Context, c *client.Client, in hookInput) string {
	s, ok := hookSession(ctx, c, in)
	if !ok {
		return hookNotCheckedIn
	}
	n := 0
	if msgs, err := c.Inbox(ctx, s.Callsign, 0, true); err != nil {
		hookDebug("inbox: %v", err)
	} else {
		n = len(msgs)
	}
	return fmt.Sprintf("Callboard: you are checked in as %s (%s) in scope %s. %d message(s) waiting: run `callboard inbox --as %s`.",
		s.Callsign, s.Role, s.PrimaryScope(), n, s.Callsign)
}

// hookClaudeStop delivers pending messages on stderr with exit 2, which
// blocks the stop and hands the text to the agent (Claude Code and Codex).
// Copilot CLI also runs Claude-format hooks but ignores exit 2, so under a
// platform other than the one the hook was installed for it must not
// consume messages the agent would never see.
func hookClaudeStop(platform string) int {
	in := readHookInput()
	if p := nearestAgentAncestor(); p != "" && p != platform {
		hookDebug("stop: running under %s, leaving messages for its own hooks", p)
		return ExitOK
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	c := client.New(config.SocketPath())
	s, ok := hookSession(ctx, c, in)
	if !ok {
		return ExitOK
	}
	msgs, err := c.Inbox(ctx, s.Callsign, 0, false)
	if err != nil {
		hookDebug("inbox: %v", err)
		return ExitOK
	}
	if len(msgs) == 0 {
		return ExitOK
	}
	fmt.Fprintf(os.Stderr, "callboard: %d new message(s) for %s. Handle them, then run `callboard wait --as %s` when done.\n\n",
		len(msgs), s.Callsign, s.Callsign)
	fmt.Fprint(os.Stderr, formatMessages(msgs))
	return exitBlockStop
}

// hookClaudeSessionStart prints plain text, which Claude Code adds to the
// agent's context (a top-level JSON additionalContext field is ignored).
func hookClaudeSessionStart() int {
	in := readHookInput()
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	fmt.Println(hookStartContext(ctx, client.New(config.SocketPath()), in))
	if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" && in.id() != "" {
		f, err := os.OpenFile(envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			hookDebug("opening CLAUDE_ENV_FILE: %v", err)
		} else {
			fmt.Fprintln(f, "export CALLBOARD_PLATFORM_SESSION="+in.id())
			f.Close()
		}
	}
	return ExitOK
}

// copilotOutput is the single-line JSON Copilot CLI hooks return.
func copilotOutput(additionalContext string) int {
	out := map[string]string{}
	if additionalContext != "" {
		out["additionalContext"] = additionalContext
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	return ExitOK
}

// hookCopilotSessionStart injects the session-start text as additionalContext.
func hookCopilotSessionStart() int {
	in := readHookInput()
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	return copilotOutput(hookStartContext(ctx, client.New(config.SocketPath()), in))
}

// hookCopilotPostTool runs after every tool call. Copilot CLI has no hook
// that can block the end of a turn, so this is the nudge: when messages are
// waiting it appends a note to the tool result telling the agent to read
// them. It only peeks; reading is the agent's job.
func hookCopilotPostTool() int {
	in := readHookInput()
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	c := client.New(config.SocketPath())
	s, ok := hookSession(ctx, c, in)
	if !ok {
		return copilotOutput("")
	}
	msgs, err := c.Inbox(ctx, s.Callsign, 0, true)
	if err != nil {
		hookDebug("inbox: %v", err)
		return copilotOutput("")
	}
	if len(msgs) == 0 {
		return copilotOutput("")
	}
	return copilotOutput(fmt.Sprintf("Callboard: %d message(s) waiting for %s. Run `callboard inbox --as %s` and act on them before continuing.",
		len(msgs), s.Callsign, s.Callsign))
}
