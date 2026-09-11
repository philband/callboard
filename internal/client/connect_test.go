package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/philband/callboard/internal/build"
	"github.com/philband/callboard/internal/hub"
)

var sockN atomic.Int32

// tempSocket returns a short socket path; macOS caps them at ~104 bytes.
func tempSocket(t *testing.T) string {
	t.Helper()
	p := filepath.Join(os.TempDir(), fmt.Sprintf("cb-cl-%d-%d.sock", os.Getpid(), sockN.Add(1)))
	os.Remove(p)
	t.Cleanup(func() { os.Remove(p) })
	return p
}

// serveInProc runs a real hub on socket until the test ends or the hub is
// asked to shut down. It stands in for the `callboard hub` process: the test
// binary cannot exec itself as a hub.
func serveInProc(t *testing.T, socket, lock, version string) *hub.Hub {
	t.Helper()
	h, err := hub.New(hub.Options{Version: version})
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); h.Close() })
	go func() {
		if err := hub.Serve(ctx, h, socket, lock, version); err != nil && err != hub.ErrAlreadyRunning {
			t.Errorf("serve: %v", err)
		}
	}()
	return h
}

// stubBuild makes build.This report i for the duration of the test.
func stubBuild(t *testing.T, i build.Info) {
	t.Helper()
	prev := build.This
	build.This = func() build.Info { return i }
	t.Cleanup(func() { build.This = prev })
}

func TestConnectRestartsOlderHub(t *testing.T) {
	t.Setenv("CALLBOARD_STATE_DIR", t.TempDir())
	socket := tempSocket(t)
	lock := filepath.Join(t.TempDir(), "hub.lock")

	old := build.Info{Version: "old", ModTime: time.Now().Add(-time.Hour)}
	stubBuild(t, old)
	stale := serveInProc(t, socket, lock, "old")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := New(socket)
	waitHealthy(t, ctx, c)

	// From here on this executable is the newer build, and a spawn puts a
	// hub built from it on the socket.
	fresh := build.Info{Version: "new", ModTime: time.Now()}
	stubBuild(t, fresh)
	var spawns atomic.Int32
	prevSpawn := spawnHub
	spawnHub = func(string) error {
		spawns.Add(1)
		serveInProc(t, socket, lock, "new")
		return nil
	}
	t.Cleanup(func() { spawnHub = prevSpawn })

	var got [2]build.Info
	var called atomic.Bool
	prevHook := OnHubRestart
	OnHubRestart = func(o, n build.Info) { got = [2]build.Info{o, n}; called.Store(true) }
	t.Cleanup(func() { OnHubRestart = prevHook })

	if _, err := Connect(ctx, socket); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if spawns.Load() == 0 {
		t.Fatal("connect did not spawn a replacement hub")
	}
	if !called.Load() {
		t.Fatal("OnHubRestart was not called")
	}
	if !got[0].ModTime.Equal(old.ModTime) || !got[1].ModTime.Equal(fresh.ModTime) {
		t.Fatalf("OnHubRestart(%v, %v), want (%v, %v)", got[0], got[1], old, fresh)
	}
	select {
	case <-stale.Closing():
	default:
		t.Fatal("the old hub was not asked to shut down")
	}
	health, err := c.Health(ctx)
	if err != nil {
		t.Fatalf("health after restart: %v", err)
	}
	if !health.Build.ModTime.Equal(fresh.ModTime) {
		t.Fatalf("hub build after restart = %v, want %v", health.Build.ModTime, fresh.ModTime)
	}
}

func TestConnectLeavesNewerHubAlone(t *testing.T) {
	t.Setenv("CALLBOARD_STATE_DIR", t.TempDir())
	socket := tempSocket(t)
	lock := filepath.Join(t.TempDir(), "hub.lock")

	stubBuild(t, build.Info{Version: "new", ModTime: time.Now()})
	running := serveInProc(t, socket, lock, "new")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := New(socket)
	waitHealthy(t, ctx, c)

	// An older client must not drag the hub back to its own build.
	stubBuild(t, build.Info{Version: "old", ModTime: time.Now().Add(-time.Hour)})
	prevSpawn := spawnHub
	spawnHub = func(string) error { t.Error("older client spawned a hub"); return nil }
	t.Cleanup(func() { spawnHub = prevSpawn })

	if _, err := Connect(ctx, socket); err != nil {
		t.Fatalf("connect: %v", err)
	}
	select {
	case <-running.Closing():
		t.Fatal("an older client shut the hub down")
	default:
	}
}

func waitHealthy(t *testing.T, ctx context.Context, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if healthy(ctx, c) == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("hub did not become healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
