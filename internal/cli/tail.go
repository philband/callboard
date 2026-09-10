package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/philband/callboard/internal/api"
)

// followWait is the long-poll window used by -follow.
const followWait = 60 * time.Second

func init() {
	register("tail", "print the event stream", runTail)
}

func runTail(args []string) int {
	fs := newFlags("tail", "[-scope S] [-since SEQ] [-follow]")
	var c common
	c.bind(fs, false)
	scopeFlag := fs.String("scope", "", "only events concerning this scope")
	since := fs.Uint64("since", 0, "start after this event sequence number")
	follow := fs.Bool("follow", false, "keep printing new events until interrupted")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	enc := json.NewEncoder(os.Stdout)

	next := *since
	for {
		wait := time.Duration(0)
		if *follow {
			wait = followWait
		}
		resp, err := cl.Events(ctx, next, wait, *scopeFlag)
		if err != nil {
			if ctx.Err() != nil {
				return ExitOK
			}
			return fail(err)
		}
		for _, ev := range resp.Events {
			if c.json {
				if err := enc.Encode(ev); err != nil {
					return fail(err)
				}
				continue
			}
			fmt.Printf("%d %s %s %s\n", ev.Seq, ev.Time.Local().Format("15:04:05"), ev.Type, eventSummary(ev))
		}
		if resp.Next > next {
			next = resp.Next
		}
		if !*follow {
			return ExitOK
		}
		if ctx.Err() != nil {
			return ExitOK
		}
	}
}

// eventSummary renders one event as a single readable phrase.
func eventSummary(ev api.Event) string {
	switch ev.Type {
	case api.EvCheckin:
		if ev.Session == nil {
			return ""
		}
		who := ev.Session.Callsign
		if ev.Session.Role != "" {
			who += " (" + ev.Session.Role + ")"
		}
		return fmt.Sprintf("%s joined scope %s", who, ev.Session.PrimaryScope())
	case api.EvCheckout:
		return fmt.Sprintf("%s left", ev.Callsign)
	case api.EvMessage:
		if ev.Message == nil {
			return ""
		}
		m := ev.Message
		return fmt.Sprintf("%s -> %s (%s) %s", m.From, m.To, m.Kind, truncate(firstLine(m.Body), 80))
	case api.EvDelivered:
		return fmt.Sprintf("%s read %s", ev.Callsign, strings.Join(ev.MessageIDs, ","))
	case api.EvJobPost:
		if ev.Job == nil {
			return ""
		}
		return fmt.Sprintf("%s posted %s: %s", ev.Job.PostedBy, ev.Job.ID, ev.Job.Title)
	case api.EvJobClaim:
		return fmt.Sprintf("%s claimed %s", ev.Callsign, jobID(ev))
	case api.EvJobDone:
		return fmt.Sprintf("%s finished %s", ev.Callsign, jobID(ev))
	case api.EvJobFail:
		return fmt.Sprintf("%s failed %s", ev.Callsign, jobID(ev))
	}
	return ""
}

func jobID(ev api.Event) string {
	if ev.Job == nil {
		return ""
	}
	return ev.Job.ID
}
