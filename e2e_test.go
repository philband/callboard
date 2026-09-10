package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/philband/callboard/internal/client"
)

// The end-to-end test builds the binary and drives a coordinator and a
// worker through check-in, job hand-off, messaging, wait, journal replay and
// hub auto-spawn, exactly as agent sessions would from a shell.

type env struct {
	t      *testing.T
	bin    string
	state  string
	socket string
	hub    *exec.Cmd
}

func newEnv(t *testing.T) *env {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "callboard")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	state := t.TempDir()
	// Unix socket paths are limited to ~104 bytes on macOS; t.TempDir is long.
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("cb-e2e-%d.sock", os.Getpid()))
	t.Cleanup(func() { os.Remove(socket) })
	return &env{t: t, bin: bin, state: state, socket: socket}
}

func (e *env) environ() []string {
	return append(os.Environ(),
		"CALLBOARD_STATE_DIR="+e.state,
		"CALLBOARD_SOCKET="+e.socket,
		"CALLBOARD_CONFIG="+filepath.Join(e.state, "no-config.json"),
		"CALLBOARD_AS=",
		"CLAUDE_CODE_SESSION_ID=",
	)
}

func (e *env) startHub() {
	e.t.Helper()
	e.hub = exec.Command(e.bin, "hub")
	e.hub.Env = e.environ()
	e.hub.Stderr = os.Stderr
	if err := e.hub.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.waitHealthy()
}

func (e *env) stopHub() {
	e.t.Helper()
	if e.hub == nil {
		return
	}
	_ = e.hub.Process.Signal(syscall.SIGTERM)
	_ = e.hub.Wait()
	e.hub = nil
	e.waitGone()
}

func (e *env) waitHealthy() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if err := client.Ping(ctx, e.socket); err == nil {
			return
		}
		if ctx.Err() != nil {
			e.t.Fatal("hub did not become healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) waitGone() {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := client.Ping(ctx, e.socket)
		cancel()
		if err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("hub still answering after stop")
}

// run executes a subcommand and returns stdout, stderr and the exit code.
func (e *env) run(args ...string) (string, string, int) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.environ()
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("%v: %v", args, err)
	}
	e.t.Logf("$ callboard %s\n%s%s(exit %d)", strings.Join(args, " "), out.String(), errb.String(), code)
	return out.String(), errb.String(), code
}

func (e *env) ok(args ...string) string {
	e.t.Helper()
	out, errb, code := e.run(args...)
	if code != 0 {
		e.t.Fatalf("callboard %v exited %d: %s", args, code, errb)
	}
	return out
}

func want(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
}

func TestTwoSessions(t *testing.T) {
	e := newEnv(t)
	e.startHub()
	defer e.stopHub()

	repo, _ := os.Getwd()

	out := e.ok("checkin", "-name", "coord", "-role", "coordinator", "-platform", "test", "-cwd", repo)
	want(t, out, "checked in as coord", "coordinator", "github.com/philband/callboard", "CALLSIGN")

	out = e.ok("checkin", "-name", "worker", "-role", "worker", "-platform", "test", "-cwd", repo)
	want(t, out, "checked in as worker", "coord")

	// Same name while the first is active gets a suffix.
	out = e.ok("checkin", "-name", "worker", "-platform", "test", "-cwd", repo)
	want(t, out, "checked in as worker-2")
	e.ok("checkout", "-as", "worker-2")

	out = e.ok("who")
	want(t, out, "coord", "worker", "human")
	if strings.Contains(out, "worker-2") {
		t.Errorf("worker-2 still in roster:\n%s", out)
	}

	// Coordinator hands out a pre-assigned job.
	out = e.ok("post", "-as", "coord", "-to", "worker", "-body", "Write the thing.", "Implement X")
	want(t, out, "j1", "Implement X")

	out = e.ok("inbox", "-as", "worker")
	want(t, out, "New job j1 from coord: Implement X", "Write the thing.", "[job j1]")

	// Only the assignee may claim it.
	_, errb, code := e.run("claim", "-as", "coord", "j1")
	if code == 0 || !strings.Contains(errb, "assigned to worker") {
		t.Errorf("claim by non-assignee: code %d, stderr %q", code, errb)
	}
	out = e.ok("claim", "-as", "worker", "j1")
	want(t, out, "j1", "claimed")

	e.ok("send", "-as", "worker", "-to", "coord", "starting on it")

	out = e.ok("wait", "-as", "coord", "-timeout", "2s")
	want(t, out, "worker claimed job j1", "starting on it", "from worker to coord")

	// Nothing new: wait times out with exit 3.
	_, _, code = e.run("wait", "-as", "coord", "-timeout", "500ms")
	if code != 3 {
		t.Errorf("wait with nothing pending: exit %d, want 3", code)
	}

	// Wait wakes up when a message arrives mid-poll.
	done := make(chan string, 1)
	go func() {
		out, _, _ := e.run("wait", "-as", "worker", "-timeout", "5s")
		done <- out
	}()
	time.Sleep(300 * time.Millisecond)
	e.ok("say", "-to", "worker", "how is it going?")
	select {
	case out = <-done:
		want(t, out, "from human to worker", "how is it going?")
	case <-time.After(6 * time.Second):
		t.Fatal("wait did not wake up on new message")
	}

	out = e.ok("done", "-as", "worker", "-result", "shipped in abc123", "j1")
	want(t, out, "j1", "done")
	out = e.ok("wait", "-as", "coord", "-timeout", "2s")
	want(t, out, "worker finished job j1", "shipped in abc123")

	out = e.ok("jobs")
	want(t, out, "j1", "done", "Implement X")

	// Open job in the scope: anyone can claim, second claim conflicts.
	e.ok("post", "-as", "coord", "Open task")
	out = e.ok("inbox", "-as", "worker")
	want(t, out, "New job j2")
	e.ok("claim", "-as", "worker", "j2")
	_, errb, code = e.run("claim", "-as", "human", "j2")
	if code == 0 || !strings.Contains(errb, "claimed") {
		t.Errorf("double claim: code %d, stderr %q", code, errb)
	}
	e.ok("fail", "-as", "worker", "-reason", "not possible", "j2")

	// Broadcast reaches everyone but the sender.
	e.ok("send", "-as", "coord", "-to", "*", "wrapping up")
	out = e.ok("inbox", "-as", "worker")
	want(t, out, "wrapping up")
	out = e.ok("inbox", "-as", "human")
	want(t, out, "wrapping up")
	out = e.ok("inbox", "-as", "coord")
	if strings.Contains(out, "wrapping up") {
		t.Errorf("sender received its own broadcast:\n%s", out)
	}

	out = e.ok("inbox", "-as", "worker", "-all")
	want(t, out, "New job j1", "starting on it", "how is it going?")

	out = e.ok("tail")
	want(t, out, "checkin", "job.post", "job.claim", "job.done", "job.fail", "message", "delivered")

	// Journal replay: restart the hub, state must survive.
	e.stopHub()
	e.startHub()
	out = e.ok("who")
	want(t, out, "coord", "worker")
	out = e.ok("jobs", "-status", "done")
	want(t, out, "j1")
	e.ok("say", "-to", "worker", "after restart")
	out = e.ok("inbox", "-as", "worker")
	want(t, out, "after restart")

	e.ok("checkout", "-as", "worker")
	out = e.ok("who")
	if strings.Contains(out, "worker") {
		t.Errorf("worker still in roster after checkout:\n%s", out)
	}
}

func TestAutoSpawn(t *testing.T) {
	e := newEnv(t)
	// No hub running: the first command must spawn one.
	out := e.ok("who")
	want(t, out, "human")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := client.New(e.socket).Health(ctx)
	if err != nil {
		t.Fatalf("health after auto-spawn: %v", err)
	}
	if h.PID <= 0 {
		t.Fatalf("health has no pid: %+v", h)
	}
	p, _ := os.FindProcess(h.PID)
	_ = p.Signal(syscall.SIGTERM)
	e.waitGone()
	if _, err := os.Stat(filepath.Join(e.state, "hub.log")); err != nil {
		t.Errorf("hub.log missing: %v", err)
	}
}
