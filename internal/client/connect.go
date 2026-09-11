package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/build"
	"github.com/philband/callboard/internal/config"
)

const (
	// spawnWait bounds how long we wait for a freshly spawned hub.
	spawnWait = 3 * time.Second
	// spawnPoll is the health-check interval while it comes up.
	spawnPoll = 50 * time.Millisecond
	// spawnRetry is how long one spawn gets before another is attempted. A
	// spawn racing a hub that is still shutting down loses the lock file and
	// exits silently, so a single attempt is not enough.
	spawnRetry = 500 * time.Millisecond
	// stopWait bounds how long Restart waits for the old hub to let go.
	stopWait = 5 * time.Second
)

// OnHubRestart is called after Connect has replaced an older hub with this
// build. The CLI sets it to tell the user on stderr. Nil means stay quiet.
var OnHubRestart func(old, new build.Info)

// Connect returns a client for the hub socket, starting the hub in the
// background if nothing answers on it. When the hub is running an older
// build than this executable it is restarted first, so a rebuild takes
// effect without anyone restarting anything by hand.
func Connect(ctx context.Context, socket string) (*Client, error) {
	c := New(socket)
	health, err := c.Health(ctx)
	if err == nil {
		return c, upgrade(ctx, c, health)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return nil, err // a hub is there, it just said no
	}
	if err := start(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Ping reports whether a hub answers on the socket. It never starts one.
func Ping(ctx context.Context, socket string) error {
	return healthy(ctx, New(socket))
}

// Restart stops the hub on socket and brings up a replacement from this
// executable. A non-zero ifModTime must match the build the caller saw, so
// a stale request cannot take down a newer hub that has replaced it.
func Restart(ctx context.Context, socket string, ifModTime time.Time) error {
	c := New(socket)
	if err := c.Shutdown(ctx, ifModTime); err != nil {
		return fmt.Errorf("asking the hub to stop: %w", err)
	}
	deadline := time.Now().Add(stopWait)
	for healthy(ctx, c) == nil {
		if time.Now().After(deadline) {
			return fmt.Errorf("hub on %s did not stop within %s", socket, stopWait)
		}
		if err := pause(ctx, spawnPoll); err != nil {
			return err
		}
	}
	return start(ctx, c)
}

// upgrade restarts the hub when this executable is the newer build. A hub
// that predates build reporting is left alone: it has no shutdown endpoint.
// A failed upgrade is only an error when it also left no hub serving.
func upgrade(ctx context.Context, c *Client, health api.HealthResponse) error {
	mine := build.This()
	if health.Build.ModTime.IsZero() || !mine.NewerThan(health.Build) {
		return nil
	}
	if err := Restart(ctx, c.socket, health.Build.ModTime); err != nil {
		if healthy(ctx, c) == nil {
			return nil // the old hub is still serving; carry on with it
		}
		return err
	}
	if OnHubRestart != nil {
		OnHubRestart(health.Build, mine)
	}
	return nil
}

// start spawns a hub and waits for it to answer on the socket.
func start(ctx context.Context, c *Client) error {
	logPath := filepath.Join(config.StateDir(), "hub.log")
	deadline := time.Now().Add(spawnWait)
	for {
		if err := spawnHub(logPath); err != nil {
			return fmt.Errorf("starting hub: %w", err)
		}
		for waited := time.Duration(0); waited < spawnRetry; waited += spawnPoll {
			if err := pause(ctx, spawnPoll); err != nil {
				return err
			}
			if healthy(ctx, c) == nil {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hub did not come up on %s within %s: see %s", c.socket, spawnWait, logPath)
		}
	}
}

func healthy(ctx context.Context, c *Client) error {
	_, err := c.Health(ctx)
	return err
}

// pause sleeps d unless the context ends first.
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// spawnHub starts a detached hub. A package var so tests can put an
// in-process hub on the socket instead of exec'ing the test binary.
var spawnHub = spawn

// spawn starts `callboard hub` detached, with its output appended to logPath.
// Racing spawns are harmless: the hub takes a flock and the losers exit 0.
func spawn(logPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, "hub")
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
