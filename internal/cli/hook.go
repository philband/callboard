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
	register("hook", "Claude Code hook entry points (used by `callboard init --hook`)", runHook)
}

// exitBlockStop is Claude Code's Stop-hook contract: exiting 2 blocks the
// stop and feeds stderr back to Claude as instructions. It happens to equal
// ExitUsage numerically; the meaning here is unrelated.
const exitBlockStop = 2

// hookTimeout bounds every hub call a hook makes. Hooks run on Claude Code's
// critical path and must never hang it.
const hookTimeout = 5 * time.Second

func runHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: callboard hook <stop|session-start>")
		return ExitUsage
	}
	switch args[0] {
	case "stop":
		return runHookStop()
	case "session-start":
		return runHookSessionStart()
	default:
		fmt.Fprintf(os.Stderr, "callboard: unknown hook %q\n", args[0])
		return ExitUsage
	}
}

// hookDebug writes a diagnostic line to stderr only when CALLBOARD_DEBUG is
// set. Hooks otherwise fail silently: a broken hub must never block Claude
// Code.
func hookDebug(format string, args ...any) {
	if os.Getenv("CALLBOARD_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "callboard: "+format+"\n", args...)
}

type stopHookInput struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	StopHookActive bool   `json:"stop_hook_active"`
}

func runHookStop() int {
	var in stopHookInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		hookDebug("stop: reading stdin: %v", err)
		return ExitOK
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	c := client.New(config.SocketPath())

	sess, err := c.Resolve(ctx, api.ResolveRequest{PlatformSession: in.SessionID, Cwd: in.Cwd})
	if err != nil {
		hookDebug("stop: resolve: %v", err)
		return ExitOK
	}

	msgs, err := c.Inbox(ctx, sess.Callsign, 0, false)
	if err != nil {
		hookDebug("stop: inbox: %v", err)
		return ExitOK
	}
	if len(msgs) == 0 {
		return ExitOK
	}

	fmt.Fprintf(os.Stderr, "callboard: %d new message(s) for %s. Handle them, then run `callboard wait --as %s` when done.\n\n",
		len(msgs), sess.Callsign, sess.Callsign)
	fmt.Fprint(os.Stderr, formatMessages(msgs))
	return exitBlockStop
}

type sessionStartHookInput struct {
	SessionID          string `json:"session_id"`
	Cwd                string `json:"cwd"`
	SessionStartReason string `json:"session_start_reason"`
}

func runHookSessionStart() int {
	var in sessionStartHookInput
	_ = json.NewDecoder(os.Stdin).Decode(&in) // best effort; zero value degrades gracefully

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	c := client.New(config.SocketPath())

	msg := "Callboard is available for coordinating with other agent sessions on this project; " +
		"check in with `callboard checkin --name <name> --role <coordinator|worker>` when you start working."

	sess, err := c.Resolve(ctx, api.ResolveRequest{PlatformSession: in.SessionID, Cwd: in.Cwd})
	if err != nil {
		hookDebug("session-start: resolve: %v", err)
	} else {
		n := 0
		if msgs, err := c.Inbox(ctx, sess.Callsign, 0, true); err != nil {
			hookDebug("session-start: inbox: %v", err)
		} else {
			n = len(msgs)
		}
		msg = fmt.Sprintf("Callboard: you are checked in as %s (%s) in scope %s. %d message(s) waiting: run `callboard inbox --as %s`.",
			sess.Callsign, sess.Role, sess.PrimaryScope(), n, sess.Callsign)
	}

	// Plain stdout on exit 0 is added to Claude's context for SessionStart
	// hooks; a top-level JSON additionalContext field would be ignored.
	fmt.Println(msg)

	if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" && in.SessionID != "" {
		f, err := os.OpenFile(envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			hookDebug("session-start: opening CLAUDE_ENV_FILE: %v", err)
		} else {
			fmt.Fprintln(f, "export CALLBOARD_PLATFORM_SESSION="+in.SessionID)
			f.Close()
		}
	}
	return ExitOK
}
