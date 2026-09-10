package cli

import (
	"fmt"
	"strings"

	"github.com/philband/callboard/internal/api"
)

func init() {
	register("post", "post a job to the board", runPost)
}

func runPost(args []string) int {
	fs := newFlags("post", "-as CS [-scope S] [-to CS] [-body TEXT] TITLE...")
	var c common
	c.bind(fs, true)
	scopeFlag := fs.String("scope", "", "scope to post in (default: your primary scope)")
	to := fs.String("to", "", "pre-assign the job to this callsign")
	body := fs.String("body", "", "job detail; \"-\" reads stdin")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	title := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(title) == "" {
		return usageErr(fs, "missing job title")
	}
	cs, err := c.callsign()
	if err != nil {
		return fail(err)
	}
	text, err := dashStdin(*body)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signalContext()
	defer stop()
	cl, err := connect(ctx)
	if err != nil {
		return fail(err)
	}
	job, err := cl.PostJob(ctx, api.PostJobRequest{As: cs, Scope: *scopeFlag, Title: title, Body: text, To: *to})
	if err != nil {
		return fail(err)
	}
	if c.json {
		printJSON(job)
		return ExitOK
	}
	fmt.Println(formatJob(job))
	if job.Assignee != "" {
		fmt.Printf("posted %s for %s; they claim it with `callboard claim --as %s %s`\n", job.ID, job.Assignee, job.Assignee, job.ID)
	} else {
		fmt.Printf("posted %s; workers claim it with `callboard claim --as <cs> %s`\n", job.ID, job.ID)
	}
	return ExitOK
}
