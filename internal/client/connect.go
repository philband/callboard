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

	"github.com/philband/callboard/internal/config"
)

// spawnWait bounds how long Connect waits for a freshly spawned hub.
const spawnWait = 3 * time.Second

// Connect returns a client for the hub socket, starting the hub in the
// background if nothing answers on it.
func Connect(ctx context.Context, socket string) (*Client, error) {
	c := New(socket)
	err := healthy(ctx, c)
	if err == nil {
		return c, nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return nil, err // a hub is there, it just said no
	}
	logPath := filepath.Join(config.StateDir(), "hub.log")
	if err := spawn(logPath); err != nil {
		return nil, fmt.Errorf("starting hub: %w", err)
	}
	deadline := time.Now().Add(spawnWait)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if err := healthy(ctx, c); err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("hub did not come up on %s within %s: see %s", socket, spawnWait, logPath)
		}
	}
}

// Ping reports whether a hub answers on the socket. It never starts one.
func Ping(ctx context.Context, socket string) error {
	return healthy(ctx, New(socket))
}

func healthy(ctx context.Context, c *Client) error {
	_, err := c.Health(ctx)
	return err
}

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
