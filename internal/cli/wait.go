package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/client"
)

// maxWait is the hub's cap on a long poll.
const maxWait = 24 * time.Hour

func init() {
	register("wait", "block until a message arrives", runWait)
}

func runWait(args []string) int {
	fs := newFlags("wait", "-as CS [-timeout 6h]")
	var c common
	c.bind(fs, true)
	// Meant to run as a background task, so wake-ups without messages are
	// rare by default. Foreground callers pass a shorter -timeout.
	timeout := fs.Duration("timeout", 6*time.Hour, "how long to wait (capped at 24h by the hub)")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	cs, err := c.callsign()
	if err != nil {
		return fail(err)
	}
	wait := *timeout
	if wait > maxWait {
		wait = maxWait
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	// The hub may be restarted under us (a rebuild, `callboard restart`).
	// That must cost nothing: reconnect and keep waiting on the same
	// deadline, so a message sent after the restart still arrives here.
	deadline := time.Now().Add(wait)
	var msgs []api.Message
	for remaining := wait; remaining > 0; remaining = time.Until(deadline) {
		msgs, err = cl.Inbox(ctx, cs, remaining, false)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return exitSignal
		}
		if !client.IsRestarting(err) {
			return fail(err)
		}
		if !sleepCtx(ctx, reconnectPause) {
			return exitSignal
		}
		if cl, err = connect(ctx); err != nil {
			if ctx.Err() != nil {
				return exitSignal
			}
			return fail(err)
		}
	}
	if ctx.Err() != nil {
		return exitSignal
	}
	if len(msgs) == 0 {
		if c.json {
			printJSON(msgs)
		}
		fmt.Fprintf(os.Stderr, "no messages within %s; run wait again\n", shortDur(wait))
		return ExitTimeout
	}
	if c.json {
		printJSON(msgs)
		return ExitOK
	}
	fmt.Print(formatMessages(msgs))
	return ExitOK
}
