package cli

import (
	"fmt"
	"strings"

	"github.com/philband/callboard/internal/api"
)

func init() {
	register("send", "send a message to a session, a scope or everyone", runSend)
}

func runSend(args []string) int {
	fs := newFlags("send", "-as CS -to TARGET [-kind chat] [-ref JOB] BODY...")
	var c common
	c.bind(fs, true)
	to := fs.String("to", "", "callsign, scope:NAME or * (required)")
	kind := fs.String("kind", api.KindChat, "chat, job or system")
	ref := fs.String("ref", "", "job id this message is about")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if *to == "" {
		return usageErr(fs, "missing -to")
	}
	cs, err := c.callsign()
	if err != nil {
		return fail(err)
	}
	return sendMessage(c, api.SendRequest{As: cs, To: *to, Kind: *kind, Ref: *ref}, fs.Args())
}

// sendMessage completes req with a body read from args or stdin, posts it and
// reports the result. Shared by send and say.
func sendMessage(c common, req api.SendRequest, args []string) int {
	body, err := readBodyArg(args)
	if err != nil {
		return fail(err)
	}
	req.Body = body
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	resp, err := cl.Send(ctx, req)
	if err != nil {
		return fail(err)
	}
	if len(resp.Recipients) == 0 {
		if scope := strings.TrimPrefix(req.To, api.ScopePrefix); scope != req.To {
			warn("no one is in scope %s right now", scope)
		} else {
			warn("no one else is checked in right now")
		}
	}
	if c.json {
		printJSON(resp)
		return ExitOK
	}
	if strings.HasPrefix(req.To, api.ScopePrefix) || req.To == api.Broadcast {
		fmt.Printf("sent %s to %s (%s)\n", resp.Message.ID, req.To, plural(len(resp.Recipients), "recipient"))
	} else {
		fmt.Printf("sent %s to %s\n", resp.Message.ID, req.To)
	}
	return ExitOK
}
