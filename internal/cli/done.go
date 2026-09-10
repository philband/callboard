package cli

import "fmt"

func init() {
	register("done", "close a job you finished", runDone)
}

func runDone(args []string) int {
	fs := newFlags("done", "-as CS [-result TEXT] JOB")
	var c common
	c.bind(fs, true)
	result := fs.String("result", "", "what came of it; \"-\" reads stdin")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageErr(fs, "need exactly one job id")
	}
	cs, err := c.callsign()
	if err != nil {
		return fail(err)
	}
	text, err := dashStdin(*result)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	job, err := cl.DoneJob(ctx, fs.Arg(0), cs, text)
	if err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(job)
		return ExitOK
	}
	fmt.Println(formatJob(job))
	return ExitOK
}
