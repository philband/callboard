package cli

import "fmt"

func init() {
	register("who", "list the sessions on the board", runWho)
}

func runWho(args []string) int {
	fs := newFlags("who", "[-scope S]")
	var c common
	c.bind(fs, false)
	scopeFlag := fs.String("scope", "", "only sessions in this scope")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	sessions, err := cl.Sessions(ctx, *scopeFlag)
	if err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(sessions)
		return ExitOK
	}
	if len(sessions) == 0 {
		if *scopeFlag != "" {
			fmt.Printf("no sessions in scope %s\n", *scopeFlag)
		} else {
			fmt.Println("no sessions")
		}
		return ExitOK
	}
	fmt.Println(sessionHeader)
	for _, s := range sessions {
		fmt.Println(formatSession(s))
	}
	return ExitOK
}
