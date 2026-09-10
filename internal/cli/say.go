package cli

import "github.com/philband/callboard/internal/api"

func init() {
	register("say", "send a message as the human operator", runSay)
}

func runSay(args []string) int {
	fs := newFlags("say", "-to TARGET BODY...")
	var c common
	c.bind(fs, false)
	to := fs.String("to", "", "callsign, scope:NAME or * (required)")
	kind := fs.String("kind", api.KindChat, "chat, job or system")
	ref := fs.String("ref", "", "job id this message is about")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if *to == "" {
		return usageErr(fs, "missing -to")
	}
	return sendMessage(c, api.SendRequest{As: api.Human, To: *to, Kind: *kind, Ref: *ref}, fs.Args())
}
