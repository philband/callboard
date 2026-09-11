package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/build"
	"github.com/philband/callboard/internal/client"
	"github.com/philband/callboard/internal/config"
)

// healthBudget bounds the look at the hub that version takes. It must never
// hold up the answer, and it must never start a hub.
const healthBudget = 500 * time.Millisecond

func init() {
	register("version", "print the callboard version", runVersion)
}

func runVersion(args []string) int {
	fs := newFlags("version", "")
	var c common
	c.bind(fs, false)
	if ok, code := parse(fs, args); !ok {
		return code
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthBudget)
	defer cancel()
	// New, not connect: asking for the version must not spawn or restart a hub.
	h, herr := client.New(config.SocketPath()).Health(ctx)
	if c.json {
		out := map[string]any{"version": Version, "build": build.This()}
		if herr == nil {
			out["hub"] = h
		}
		printJSON(out)
		return ExitOK
	}
	fmt.Printf("callboard %s (built %s)\n", Version, buildTime(build.This()))
	if herr == nil {
		fmt.Printf("hub: %s\n", healthLine(h))
	}
	return ExitOK
}

// healthLine renders a running hub's identity: version, build and pid.
func healthLine(h api.HealthResponse) string {
	return fmt.Sprintf("%s %s pid %d", h.Version, buildTime(h.Build), h.PID)
}

// buildTime renders a build's timestamp for a person.
func buildTime(b build.Info) string {
	if b.ModTime.IsZero() {
		return "unknown"
	}
	return b.ModTime.Local().Format("2006-01-02 15:04:05")
}
