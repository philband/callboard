package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/philband/callboard/internal/config"
	"github.com/philband/callboard/internal/hub"
)

func init() {
	register("hub", "run the hub (auto-spawned by other commands)", runHub)
}

func runHub(args []string) int {
	fs := newFlags("hub", "[-socket PATH] [-journal PATH]")
	socket := fs.String("socket", config.SocketPath(), "unix socket to listen on")
	journal := fs.String("journal", filepath.Join(config.StateDir(), "journal.jsonl"), "event journal")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
		return fail(err)
	}
	h, err := hub.New(hub.Options{JournalPath: *journal, Version: Version})
	if err != nil {
		return fail(err)
	}
	defer h.Close()

	ctx, stop := signalContext()
	defer stop()
	lock := filepath.Join(config.StateDir(), "hub.lock")
	errc := make(chan error, 1)
	go func() { errc <- hub.Serve(ctx, h, *socket, lock, Version) }()

	// Announce only once we know we hold the lock: a hub that lost the race
	// returns ErrAlreadyRunning at once and must stay quiet.
	select {
	case err = <-errc:
	case <-time.After(100 * time.Millisecond):
		fmt.Fprintf(os.Stderr, "callboard hub %s listening on %s, journal %s\n", Version, *socket, *journal)
		err = <-errc
		if err == nil {
			fmt.Fprintln(os.Stderr, "callboard hub stopped")
		}
	}
	if err != nil && !errors.Is(err, hub.ErrAlreadyRunning) {
		return fail(err)
	}
	return ExitOK
}
