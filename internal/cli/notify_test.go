package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/client"
	"github.com/philband/callboard/internal/hub"
)

var notifySockN atomic.Int32

// notifyHub serves an in-memory hub on a unix socket and returns a client
// for it. Socket paths are kept short; macOS caps them at ~104 bytes.
func notifyHub(t *testing.T) *client.Client {
	t.Helper()
	h, err := hub.New(hub.Options{Version: "test"})
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("cb-nf-%d-%d.sock", os.Getpid(), notifySockN.Add(1)))
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: hub.Handler(h)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); os.Remove(sock) })
	return client.New(sock)
}

// notifyFastTimings shortens the daemon's waits for the duration of a test.
func notifyFastTimings(t *testing.T) {
	t.Helper()
	peek, pushRetry, poll := notifyPeekWait, notifyPushRetry, notifyPendingPoll
	notifyPeekWait, notifyPushRetry, notifyPendingPoll = 200*time.Millisecond, 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { notifyPeekWait, notifyPushRetry, notifyPendingPoll = peek, pushRetry, poll })
}

// notifyCheckin registers a session under thread-1 and returns its callsign.
func notifyCheckin(t *testing.T, cl *client.Client, name string) string {
	t.Helper()
	resp, err := cl.Checkin(context.Background(), api.CheckinRequest{
		Name: name, Platform: "codex", Scopes: []string{"proj"}, PlatformSession: "thread-1",
	})
	if err != nil {
		t.Fatalf("checkin: %v", err)
	}
	return resp.Session.Callsign
}

type pushCall struct{ platform, thread, text string }

func TestNotifyLoopPushesAndAcknowledges(t *testing.T) {
	notifyFastTimings(t)
	cl := notifyHub(t)
	ctx := context.Background()
	cs := notifyCheckin(t, cl, "alice")

	calls := make(chan pushCall, 4)
	push := func(platform, thread, text string) error {
		calls <- pushCall{platform, thread, text}
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runNotifyLoop(loopCtx, cl, cs, "codex", "thread-1", push) }()

	if _, err := cl.Send(ctx, api.SendRequest{As: api.Human, To: cs, Body: "ship it"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case c := <-calls:
		if c.platform != "codex" || c.thread != "thread-1" {
			t.Errorf("pushed into %s/%s", c.platform, c.thread)
		}
		if !strings.Contains(c.text, "ship it") {
			t.Errorf("prompt lacks the body: %q", c.text)
		}
		if !strings.Contains(c.text, cs) || !strings.Contains(c.text, "1 new message(s)") {
			t.Errorf("prompt = %q", c.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no push within 5s")
	}

	// Acknowledged: the inbox is empty and the batch is not pushed again.
	waitFor(t, "inbox to drain", func() bool {
		msgs, err := cl.Inbox(ctx, cs, 0, true)
		return err == nil && len(msgs) == 0
	})
	select {
	case c := <-calls:
		t.Fatalf("pushed a second time: %q", c.text)
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("loop: %v", err)
	}
}

func TestNotifyLoopRetriesFailedPush(t *testing.T) {
	notifyFastTimings(t)
	cl := notifyHub(t)
	ctx := context.Background()
	cs := notifyCheckin(t, cl, "bob")

	var n atomic.Int32
	failed := make(chan struct{})
	ok := make(chan struct{}, 4)
	push := func(platform, thread, text string) error {
		if n.Add(1) == 1 {
			close(failed)
			return errors.New("codex is not listening")
		}
		ok <- struct{}{}
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runNotifyLoop(loopCtx, cl, cs, "codex", "thread-1", push) }()

	if _, err := cl.Send(ctx, api.SendRequest{As: api.Human, To: cs, Body: "try again"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("no push within 5s")
	}
	// A failed push acknowledges nothing, so the message is still pending.
	msgs, err := cl.Inbox(ctx, cs, 0, true)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("after a failed push: %d message(s), %v", len(msgs), err)
	}

	select {
	case <-ok:
	case <-time.After(5 * time.Second):
		t.Fatal("the batch was not retried")
	}
	waitFor(t, "inbox to drain", func() bool {
		msgs, err := cl.Inbox(ctx, cs, 0, true)
		return err == nil && len(msgs) == 0
	})

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("loop: %v", err)
	}
}

func TestNotifyLoopStopsOnCheckout(t *testing.T) {
	notifyFastTimings(t)
	cl := notifyHub(t)
	ctx := context.Background()
	cs := notifyCheckin(t, cl, "carol")

	push := func(platform, thread, text string) error {
		t.Errorf("unexpected push: %q", text)
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- runNotifyLoop(ctx, cl, cs, "codex", "thread-1", push) }()

	// Let the loop reach its first long poll, then pull the session away.
	time.Sleep(50 * time.Millisecond)
	if err := cl.Checkout(ctx, cs); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errCheckedOut) {
			t.Fatalf("loop returned %v, want errCheckedOut", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop after checkout")
	}
}

// The session-start hook starts the daemon before any check-in, so it must
// wait for its thread to appear, serve it, and go back to waiting when that
// session checks out: the same Codex session can check in again.
func TestNotifyDaemonWaitsForCheckin(t *testing.T) {
	notifyFastTimings(t)
	cl := notifyHub(t)
	ctx := context.Background()

	calls := make(chan pushCall, 4)
	push := func(platform, thread, text string) error {
		calls <- pushCall{platform, thread, text}
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runNotifyDaemon(loopCtx, cl, "", "codex", "thread-1", push) }()

	// Nothing to serve yet, so nothing is pushed.
	select {
	case c := <-calls:
		t.Fatalf("pushed before any check-in: %q", c.text)
	case <-time.After(200 * time.Millisecond):
	}

	cs := notifyCheckin(t, cl, "dora")
	if _, err := cl.Send(ctx, api.SendRequest{As: api.Human, To: cs, Body: "first"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if c := awaitPush(t, calls); !strings.Contains(c.text, "first") {
		t.Errorf("prompt = %q", c.text)
	}

	// Checking out sends the daemon back to waiting rather than ending it.
	if err := cl.Checkout(ctx, cs); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("daemon exited on checkout: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	cs2 := notifyCheckin(t, cl, "dora")
	if _, err := cl.Send(ctx, api.SendRequest{As: api.Human, To: cs2, Body: "second"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if c := awaitPush(t, calls); !strings.Contains(c.text, "second") {
		t.Errorf("prompt = %q", c.text)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("daemon: %v", err)
	}
}

func TestNotifyDaemonGivesUpWhenNobodyChecksIn(t *testing.T) {
	notifyFastTimings(t)
	limit := notifyPendingLimit
	notifyPendingLimit = 150 * time.Millisecond
	t.Cleanup(func() { notifyPendingLimit = limit })

	cl := notifyHub(t)
	push := func(platform, thread, text string) error {
		t.Errorf("unexpected push: %q", text)
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- runNotifyDaemon(context.Background(), cl, "", "codex", "nobody", push) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not give up")
	}
}

// awaitPush returns the next push, or fails the test.
func awaitPush(t *testing.T, calls chan pushCall) pushCall {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no push within 5s")
		return pushCall{}
	}
}

func TestNotifyPromptMentionsSenderAndReply(t *testing.T) {
	msgs := []api.Message{
		{ID: "m1", From: "human", To: "alice", Kind: api.KindChat, Body: "one", Time: time.Now()},
		{ID: "m2", From: "bob", To: "alice", Kind: api.KindChat, Body: "two", Time: time.Now()},
	}
	got := notifyPrompt("alice", msgs)
	for _, want := range []string{"2 new message(s) for alice", "one", "two", "callboard send --as alice --to <sender>"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

// waitFor polls cond for up to 5s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
