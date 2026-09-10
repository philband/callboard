package cli

import "fmt"

func init() {
	register("claim", "take an open job", runClaim)
}

func runClaim(args []string) int {
	fs := newFlags("claim", "-as CS JOB")
	var c common
	c.bind(fs, true)
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageErr(fs, "need exactly one job id")
	}
	id := fs.Arg(0)
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
	job, err := cl.ClaimJob(ctx, id, cs)
	if err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(job)
		return ExitOK
	}
	fmt.Println(formatJob(job))
	fmt.Printf("claimed %s; finish with `callboard done --as %s %s --result '...'`\n", job.ID, cs, job.ID)
	return ExitOK
}
