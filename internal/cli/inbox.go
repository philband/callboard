package cli

import (
	"fmt"

	"github.com/philband/callboard/internal/api"
)

func init() {
	register("inbox", "show waiting messages without blocking", runInbox)
}

func runInbox(args []string) int {
	fs := newFlags("inbox", "-as CS [-peek] [-all]")
	var c common
	c.bind(fs, true)
	peek := fs.Bool("peek", false, "leave the messages undelivered")
	all := fs.Bool("all", false, "show every message sent or received, not just the waiting ones")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	cs, err := c.callsign()
	if err != nil {
		return fail(err)
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	var msgs []api.Message
	if *all {
		msgs, err = cl.History(ctx, cs)
	} else {
		msgs, err = cl.Inbox(ctx, cs, 0, *peek)
	}
	if err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(msgs)
		return ExitOK
	}
	if len(msgs) == 0 {
		fmt.Println("no messages")
		return ExitOK
	}
	fmt.Print(formatMessages(msgs))
	return ExitOK
}
