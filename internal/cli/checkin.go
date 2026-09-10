package cli

import (
	"fmt"
	"os"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/config"
	"github.com/philband/callboard/internal/scope"
)

func init() {
	register("checkin", "join the board and get a callsign", runCheckin)
}

func runCheckin(args []string) int {
	fs := newFlags("checkin", "-name NAME [-role R] [-platform P] [-scope S]... [-intro TEXT] [-cwd DIR]")
	var c common
	c.bind(fs, false)
	name := fs.String("name", "", "what to call you, e.g. reviewer (required)")
	role := fs.String("role", "", "coordinator, worker, or free text")
	platform := fs.String("platform", "", "agent platform (default from the environment)")
	intro := fs.String("intro", "", "one line about what you are here to do")
	cwd := fs.String("cwd", "", "working directory (default: the current one)")
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

	plat, session := *platform, os.Getenv("CLAUDE_CODE_SESSION_ID")
	if plat == "" {
		plat = "unknown"
		if session != "" {
			plat = "claude-code"
		}
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
	return ExitOK
}
