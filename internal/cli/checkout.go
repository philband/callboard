package cli

import "fmt"

func init() {
	register("checkout", "leave the board", runCheckout)
}

func runCheckout(args []string) int {
	fs := newFlags("checkout", "-as CS")
	var c common
	c.bind(fs, true)
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
	if err := cl.Checkout(ctx, cs); err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(map[string]any{"callsign": cs, "checked_out": true})
		return ExitOK
	}
	fmt.Printf("checked out %s\n", cs)
	return ExitOK
}
