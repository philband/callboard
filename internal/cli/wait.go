package cli

import (
	"fmt"
	"os"
	"time"
)

// maxWait is the hub's cap on a long poll.
const maxWait = 10 * time.Minute

func init() {
	register("wait", "block until a message arrives", runWait)
}

func runWait(args []string) int {
	fs := newFlags("wait", "-as CS [-timeout 9m]")
	var c common
	c.bind(fs, true)
	// 9m by default: agent shell tools usually time out at 10 minutes.
	timeout := fs.Duration("timeout", 9*time.Minute, "how long to wait (capped at 10m by the hub)")
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
	msgs, err := cl.Inbox(ctx, cs, wait, false)
	if err != nil {
		if ctx.Err() != nil {
			return exitSignal
		}
		return fail(err)
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
