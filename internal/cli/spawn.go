package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/config"
	"github.com/philband/callboard/internal/scope"
)

func init() {
	register("spawn", "start an agent session on one of your accounts", runSpawn)
	register("spawns", "list spawned sessions and account load", runSpawns)
	register("despawn", "stop a spawned session", runDespawn)
}

func runSpawn(args []string) int {
	fs := newFlags("spawn", "[-job J] [-platform claude|codex] [-account NAME] [-model M] [-cwd DIR] [PROMPT...]")
	var c common
	c.bind(fs, true)
	job := fs.String("job", "", "job the new session works on (reserved for it)")
	platform := fs.String("platform", "", "claude or codex (default: the job's, else any)")
	account := fs.String("account", "", "account to use (default: the least loaded one in the pool)")
	model := fs.String("model", "", "model for the session")
	cwd := fs.String("cwd", "", "working directory (default: the current one)")
	if ok, code := parse(fs, args); !ok {
		return code
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
	as, _ := c.callsign()
	if as == "" {
		as = api.Human
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	sp, err := cl.Spawn(ctx, api.SpawnRequest{
		As:       as,
		Account:  *account,
		Platform: *platform,
		Job:      *job,
		Cwd:      dir,
		Scope:    scope.Detect(dir, cfg.HostAliases),
		Model:    *model,
		Prompt:   strings.Join(fs.Args(), " "),
	})
	if err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(sp)
		return ExitOK
	}
	fmt.Println(spawnHeader)
	fmt.Println(formatSpawn(sp))
	fmt.Printf("watch it with `tmux -L callboard attach -t callboard:%s`\n", sp.ID)
	return ExitOK
}

func runSpawns(args []string) int {
	fs := newFlags("spawns", "[-all]")
	var c common
	c.bind(fs, false)
	all := fs.Bool("all", false, "include ended spawns")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	res, err := cl.Spawns(ctx, *all)
	if err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(res)
		return ExitOK
	}
	if !res.Enabled {
		fmt.Printf("spawning is off: add spawn.accounts to %s\n", config.ConfigPath())
		return ExitOK
	}
	fmt.Println("ACCOUNT        PLATFORM     POOL  RUNNING")
	for _, a := range res.Accounts {
		limit := "-"
		if a.Max > 0 {
			limit = fmt.Sprint(a.Max)
		}
		line := fmt.Sprintf("%-14s %-12s %-5v %d/%s", a.Name, a.Platform, a.Pool, a.Running, limit)
		if !a.Limited.IsZero() {
			line += fmt.Sprintf("  limited until %s: %s", stamp(a.Limited), a.Reason)
		}
		fmt.Println(line)
	}
	if len(res.Spawns) == 0 {
		fmt.Println("\nno spawned sessions")
		return ExitOK
	}
	fmt.Println()
	fmt.Println(spawnHeader)
	for _, sp := range res.Spawns {
		fmt.Println(formatSpawn(sp))
	}
	fmt.Println("\nattach with `tmux -L callboard attach -t callboard:<id>`")
	return ExitOK
}

func runDespawn(args []string) int {
	fs := newFlags("despawn", "[-reason TEXT] SPAWN")
	var c common
	c.bind(fs, true)
	reason := fs.String("reason", "", "why (shown to the job's poster)")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageErr(fs, "need exactly one spawn id")
	}
	as, _ := c.callsign()
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	if err := cl.Despawn(ctx, fs.Arg(0), api.DespawnRequest{As: as, Reason: *reason}); err != nil {
		return fail(err)
	}
	if !c.json {
		fmt.Printf("stopped %s\n", fs.Arg(0))
	}
	return ExitOK
}

const spawnHeader = "SPAWN  STATUS    ACCOUNT        CALLSIGN       JOB    STARTED   NOTE"

func formatSpawn(sp api.Spawn) string {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	line := fmt.Sprintf("%-6s %-9s %-14s %-14s %-6s %-9s", sp.ID, sp.Status, sp.Account, dash(sp.Callsign), dash(sp.Job), stamp(sp.StartedAt))
	if sp.Reason != "" {
		line += " " + sp.Reason
	} else if sp.Scope != "" {
		line += " " + sp.Scope
	}
	return line
}
