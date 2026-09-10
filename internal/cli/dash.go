package cli

import (
	"github.com/philband/callboard/internal/tui"
)

func init() {
	register("dash", "live dashboard of sessions, jobs and the feed", runDash)
}

func runDash(args []string) int {
	fs := newFlags("dash", "[-scope SCOPE]")
	scope := fs.String("scope", "", "start filtered on this scope (default: all)")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()

	c, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	if err := tui.Run(ctx, c, *scope); err != nil {
		return fail(err)
	}
	return ExitOK
}
