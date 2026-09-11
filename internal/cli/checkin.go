package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/config"
	"github.com/philband/callboard/internal/scope"
)

func init() {
	register("checkin", "join the board and get a callsign", runCheckin)
}

func runCheckin(args []string) int {
	fs := newFlags("checkin", "-name NAME [-role R] [-platform P] [-scope S]... [-intro TEXT] [-cwd DIR] [-no-notify]")
	var c common
	c.bind(fs, false)
	name := fs.String("name", "", "what to call you, e.g. reviewer (required)")
	role := fs.String("role", "", "coordinator, worker, or free text")
	platform := fs.String("platform", "", "agent platform (default from the environment)")
	intro := fs.String("intro", "", "one line about what you are here to do")
	cwd := fs.String("cwd", "", "working directory (default: the current one)")
	noNotify := fs.Bool("no-notify", false, "do not start the push notifier for this session")
	var extra stringList
	fs.Var(&extra, "scope", "extra scope on top of the detected one (repeatable)")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if *name == "" {
		return usageErr(fs, "missing -name")
	}

	dir := *cwd
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fail(err)
		}
		dir = wd
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	scopes := []string{scope.Detect(dir, cfg.HostAliases)}
	for _, s := range extra {
		if s != scopes[0] {
			scopes = append(scopes, s)
		}
	}

	detected, session, agentPid := detectPlatform()
	plat := detected
	if *platform != "" {
		plat = *platform
	}

	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	resp, err := cl.Checkin(ctx, api.CheckinRequest{
		Name:            *name,
		Role:            *role,
		Platform:        plat,
		Cwd:             dir,
		Scopes:          scopes,
		Intro:           *intro,
		PlatformSession: session,
	})
	if err != nil {
		return fail(err)
	}
	// The detected platform decides, not -platform: the session id we would
	// push into belongs to whatever detection found.
	note := notifierNote(resp.Session.Callsign, detected, session, agentPid, *noNotify)
	if c.json {
		printJSON(resp)
		return ExitOK
	}

	s := resp.Session
	role2 := s.Role
	if role2 == "" {
		role2 = "no role"
	}
	fmt.Printf("checked in as %s (%s, %s)\n", s.Callsign, role2, s.Platform)
	fmt.Printf("scope: %s\n", s.PrimaryScope())
	if len(s.Scopes) > 1 {
		for _, x := range s.Scopes[1:] {
			fmt.Printf("also:  %s\n", x)
		}
	}
	switch {
	case resp.Reclaimed && resp.Pending > 0:
		fmt.Printf("reclaimed earlier session; %s waiting: run `callboard inbox --as %s`\n", plural(resp.Pending, "message"), s.Callsign)
	case resp.Reclaimed:
		fmt.Println("reclaimed earlier session")
	case resp.Pending > 0:
		fmt.Printf("%s waiting: run `callboard inbox --as %s`\n", plural(resp.Pending, "message"), s.Callsign)
	}
	fmt.Printf("\nroster:\n%s\n", sessionHeader)
	for _, r := range resp.Roster {
		fmt.Println(formatSession(r))
	}
	fmt.Printf("\nnext: pass --as %s (or export CALLBOARD_AS=%s) on every command; run `callboard wait --as %s` when you are done with your current task.\n",
		s.Callsign, s.Callsign, s.Callsign)
	if note != "" {
		fmt.Println(note)
	}
	return ExitOK
}

// notifierNote starts the push daemon where check-in is in a position to do
// so, and returns the line to tell the agent about it, or "". Only Codex can
// take an injected prompt so far, and only in an interactive session:
// `codex exec` (marked by CODEX_CI) ends after one turn, so a queued message
// would never be read.
func notifierNote(callsign, platform, platformSession string, agentPid int, disabled bool) string {
	if disabled || platform != "codex" || platformSession == "" || os.Getenv("CODEX_CI") != "" {
		return ""
	}
	if os.Getenv("CODEX_SANDBOX") != "" {
		// The agent's shell runs inside Codex's seatbelt sandbox, which no
		// detached daemon survives. The session-start hook runs outside it
		// and has already started one; say so only if it did.
		if notifierStarted(platformSession) {
			return "messages will arrive in this session as new prompts (notifier started by the session hook); you do not need to run `callboard wait`"
		}
		return ""
	}
	if err := spawnNotifier(callsign, platform, platformSession, agentPid); err != nil {
		warn("could not start the notifier: %v", err)
		return ""
	}
	return "notifier started: messages will arrive in this session as new prompts; you do not need to run `callboard wait`"
}

// detectPlatform identifies the agent platform running this command, that
// platform's own session id (so hooks can find their callsign later) and the
// pid of the agent process (0 when it could not be found), which the
// notifier watches. The nearest agent process among our ancestors decides,
// because a session started from inside another agent's shell inherits both
// environments.
func detectPlatform() (platform, session string, pid int) {
	sessionEnv := map[string]string{
		"claude-code": "CLAUDE_CODE_SESSION_ID",
		"copilot":     "COPILOT_AGENT_SESSION_ID",
		"codex":       "CODEX_SESSION_ID",
	}
	platform, pid = nearestAgentAncestor()
	if platform == "" && os.Getenv("CODEX_SANDBOX") != "" {
		// Codex's sandbox forbids ps, so ancestry is unavailable there; the
		// sandbox marker itself is set per command and is unambiguous.
		platform = "codex"
	}
	if platform == "" {
		for _, p := range []string{"claude-code", "copilot", "codex"} {
			if os.Getenv(sessionEnv[p]) != "" {
				platform = p
				break
			}
		}
	}
	if platform == "" && os.Getenv("COPILOT_CLI") != "" {
		platform = "copilot"
	}
	if platform == "" {
		return "unknown", "", pid
	}
	return platform, os.Getenv(sessionEnv[platform]), pid
}

// nearestAgentAncestor walks up the process tree and names the first agent
// CLI it finds ("claude", "copilot" or "codex" executables) together with
// its pid, or ("", 0).
func nearestAgentAncestor() (platform string, pid int) {
	pid = os.Getppid()
	for depth := 0; depth < 20 && pid > 1; depth++ {
		out, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return "", 0
		}
		fields := strings.Fields(string(out))
		if len(fields) < 2 {
			return "", 0
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil {
			return "", 0
		}
		comm := strings.Join(fields[1:], " ")
		switch base := strings.TrimPrefix(filepath.Base(comm), "-"); {
		case base == "claude":
			return "claude-code", pid
		case base == "copilot", strings.Contains(comm, "/@github/copilot/"):
			return "copilot", pid
		case base == "codex":
			return "codex", pid
		}
		pid = ppid
	}
	return "", 0
}
