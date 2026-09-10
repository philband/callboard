package cli

import (
	"fmt"

	"github.com/philband/callboard/internal/api"
)

func init() {
	register("jobs", "list jobs on the board", runJobs)
}

func runJobs(args []string) int {
	fs := newFlags("jobs", "[-scope S] [-status open|claimed|done|failed] [-mine]")
	var c common
	c.bind(fs, true)
	scopeFlag := fs.String("scope", "", "only jobs in this scope")
	status := fs.String("status", "", "open, claimed, done or failed")
	mine := fs.Bool("mine", false, "only jobs you posted, were assigned or claimed (needs -as)")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	var cs string
	if *mine {
		var err error
		if cs, err = c.callsign(); err != nil {
			return fail(err)
		}
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	jobs, err := cl.Jobs(ctx, *scopeFlag, *status)
	if err != nil {
		return fail(err)
	}
	if cs != "" {
		kept := []api.Job{}
		for _, j := range jobs {
			if j.PostedBy == cs || j.Assignee == cs || j.ClaimedBy == cs {
				kept = append(kept, j)
			}
		}
		jobs = kept
	}
	if c.json {
		printJSON(jobs)
		return ExitOK
	}
	if len(jobs) == 0 {
		fmt.Println("no jobs")
		return ExitOK
	}
	for _, j := range jobs {
		fmt.Println(formatJob(j))
	}
	return ExitOK
}
