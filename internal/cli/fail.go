package cli

import "fmt"

func init() {
	register("fail", "close a job you could not finish", runFailCmd)
}

func runFailCmd(args []string) int {
	fs := newFlags("fail", "-as CS [-reason TEXT] JOB")
	var c common
	c.bind(fs, true)
	reason := fs.String("reason", "", "why it could not be done; \"-\" reads stdin")
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
	text, err := dashStdin(*reason)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	job, err := cl.FailJob(ctx, fs.Arg(0), cs, text)
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
