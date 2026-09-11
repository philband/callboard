package cli

import (
	"fmt"

	"github.com/philband/callboard/internal/client"
	"github.com/philband/callboard/internal/config"
)

func init() {
	register("restart", "stop the hub and start it from this executable", runRestart)
}

// runRestart replaces the running hub whatever its build is. Connect does
// this by itself when the client is newer; this is the explicit handle for
// the other cases, such as a hub that must pick up a changed journal path.
func runRestart(args []string) int {
	fs := newFlags("restart", "[-socket PATH]")
	var c common
	c.bind(fs, false)
	socket := fs.String("socket", config.SocketPath(), "hub socket")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()

	cl := client.New(*socket)
	old, herr := cl.Health(ctx)
	if herr != nil {
		// Nothing to replace: just bring one up.
		if _, err := client.Connect(ctx, *socket); err != nil {
			return fail(err)
		}
	} else if err := client.Restart(ctx, *socket, old.Build.ModTime); err != nil {
		return fail(err)
	}
	now, err := cl.Health(ctx)
	if err != nil {
		return fail(err)
	}
	if c.json {
		out := map[string]any{"hub": now}
		if herr == nil {
			out["previous"] = old
		}
		printJSON(out)
		return ExitOK
	}
	if herr == nil {
		fmt.Printf("stopped: %s\n", healthLine(old))
	}
	fmt.Printf("running: %s\n", healthLine(now))
	return ExitOK
}
