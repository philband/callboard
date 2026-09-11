package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/build"
	"github.com/philband/callboard/internal/client"
	"github.com/philband/callboard/internal/config"
)

func init() {
	register("notify", "push daemon: deliver messages into the agent's session (spawned by the session hook)", runNotify)
}

// Timings. Package vars rather than constants so tests can shorten them.
var (
	// notifyPeekWait is how long one inbox long-poll blocks. Long, because
	// the daemon lives as long as the session it pushes into.
	notifyPeekWait = 4 * time.Hour
	// notifyRetryMin and notifyRetryMax bound the backoff on hub errors.
	notifyRetryMin = 5 * time.Second
	notifyRetryMax = 60 * time.Second
	// notifyRestartMin is where the backoff starts when the hub went away
	// for a restart rather than for good: a rebuild is back in a moment.
	notifyRestartMin = 300 * time.Millisecond
	// notifyGiveUp is how long the hub may stay unreachable before the
	// daemon exits non-zero.
	notifyGiveUp = 10 * time.Minute
	// notifyPushRetry is the pause before pushing the same batch again.
	notifyPushRetry = 10 * time.Second
	// notifyPidCheck is how often -watch-pid is polled.
	notifyPidCheck = 30 * time.Second
	// notifyPendingPoll is how often a daemon without a callsign asks the
	// hub whether its thread has checked in; notifyPendingLimit is how long
	// it keeps asking.
	notifyPendingPoll  = 2 * time.Second
	notifyPendingLimit = 2 * time.Hour
	// notifyHookBudget bounds what the session-start hook spends starting
	// the hub. Hooks run on the agent's critical path.
	notifyHookBudget = time.Second
)

// errCheckedOut ends the message loop when the session it serves has left
// the board. The daemon outlives it: the same platform thread may check in
// again under a new callsign.
var errCheckedOut = errors.New("session checked out")

// notifyAlive reports whether the session we push into is still running. It
// is replaced by runNotify when -watch-pid is given, and consulted before
// every push so a dead session never receives a stale prompt.
var notifyAlive = func() bool { return true }

// notifyPush injects text into a running agent session as a new user turn.
// A package var so tests can stub it.
var notifyPush = func(platform, thread, text string) error {
	switch platform {
	case "codex":
		bin := os.Getenv("CALLBOARD_CODEX_BIN")
		if bin == "" {
			bin = "codex"
		}
		out, err := exec.Command(bin, "queue", "--thread", thread, "--message", text).CombinedOutput()
		if err != nil {
			if msg := strings.TrimSpace(string(out)); msg != "" {
				return fmt.Errorf("%s queue: %w: %s", bin, err, firstLine(msg))
			}
			return fmt.Errorf("%s queue: %w", bin, err)
		}
		return nil
	default:
		return fmt.Errorf("no message injector for platform %q", platform)
	}
}

func runNotify(args []string) int {
	fs := newFlags("notify", "-platform codex -thread ID [-as CS] [-watch-pid N] [-socket PATH]")
	var c common
	c.bind(fs, true)
	platform := fs.String("platform", "", "agent platform to push into (only codex so far)")
	thread := fs.String("thread", "", "the platform session/thread id to push into")
	watchPid := fs.Int("watch-pid", 0, "exit once this process is gone")
	socket := fs.String("socket", config.SocketPath(), "hub socket")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if *platform != "codex" {
		return usageErr(fs, "unsupported -platform %q (only codex can take a pushed message so far)", *platform)
	}
	if *thread == "" {
		return usageErr(fs, "missing -thread (the platform session id recorded at check-in)")
	}
	// -as is optional: without it the daemon waits for the thread to check
	// in, which is how the session-start hook starts it.
	cs := c.as
	if cs == "" {
		cs = os.Getenv("CALLBOARD_AS")
	}

	// One daemon per platform thread, whatever callsign it ends up serving.
	lock, err := notifyLock(*thread)
	if err != nil {
		return fail(err)
	}
	if lock == nil {
		return ExitOK
	}
	defer func() {
		// Remove the file so a dead daemon does not leave a stale lock that
		// check-in would mistake for a running notifier.
		os.Remove(notifyPath(*thread, ".lock"))
		lock.Close()
	}()

	ctx, stop := signalContext()
	defer stop()
	if *watchPid > 0 {
		pid := *watchPid
		notifyAlive = func() bool { return processAlive(pid) }
		ctx = notifyWatchPid(ctx, pid)
	}

	notifyLog("watching %s thread %s", *platform, *thread)
	if err := runNotifyDaemon(ctx, client.New(*socket), cs, *platform, *thread, notifyPush); err != nil {
		notifyLog("giving up: %v", err)
		return ExitError
	}
	return ExitOK
}

// runNotifyDaemon serves one platform thread for as long as it lives. With
// no callsign it waits for one to check in; when that session checks out it
// goes back to waiting, because the same Codex session can check in again
// under a new callsign. It returns only on an orderly end (context
// cancelled, watched process gone, nothing checked in within
// notifyPendingLimit) or on an error worth exiting non-zero for.
func runNotifyDaemon(ctx context.Context, c *client.Client, as, platform, thread string, push func(platform, thread, text string) error) error {
	for {
		if as == "" {
			cs, err := notifyAwaitSession(ctx, c, thread)
			if err != nil {
				return err
			}
			if cs == "" {
				return nil
			}
			as = cs
			notifyLog("session %s checked in", as)
		}
		err := runNotifyLoop(ctx, c, as, platform, thread, push)
		if !errors.Is(err, errCheckedOut) {
			return err
		}
		as = ""
	}
}

// notifyAwaitSession polls the hub until a session carrying this platform
// thread id exists. It returns ("", nil) when it is time to stop without an
// error: the context ended, or nothing checked in within the limit.
func notifyAwaitSession(ctx context.Context, c *client.Client, thread string) (string, error) {
	deadline := time.Now().Add(notifyPendingLimit)
	var downSince time.Time
	for {
		s, err := c.Resolve(ctx, api.ResolveRequest{PlatformSession: thread})
		switch {
		case ctx.Err() != nil:
			return "", nil
		case err == nil:
			return s.Callsign, nil
		case client.IsNotFound(err):
			downSince = time.Time{} // the hub answered; nobody is checked in yet
		default:
			if downSince.IsZero() {
				downSince = time.Now()
			}
			if time.Since(downSince) > notifyGiveUp {
				return "", fmt.Errorf("hub unreachable for %s: %w", shortDur(notifyGiveUp), err)
			}
		}
		if time.Now().After(deadline) {
			notifyLog("nothing checked in as thread %s within %s; exiting", thread, shortDur(notifyPendingLimit))
			return "", nil
		}
		if !notifySleep(ctx, notifyPendingPoll) {
			return "", nil
		}
	}
}

// runNotifyLoop peeks the inbox and pushes whatever arrives into the agent's
// session, acknowledging only what the push actually delivered. It returns
// errCheckedOut when the session is gone and nil on every other orderly end:
// context cancelled, watched process gone.
func runNotifyLoop(ctx context.Context, c *client.Client, as, platform, thread string, push func(platform, thread, text string) error) error {
	backoff := notifyRetryMin
	var downSince time.Time
	for {
		if ctx.Err() != nil {
			return nil
		}
		notifyReexec(ctx, c)
		msgs, err := c.Inbox(ctx, as, notifyPeekWait, true)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if client.IsNotFound(err) {
				notifyLog("%s is no longer checked in", as)
				return errCheckedOut
			}
			if downSince.IsZero() {
				downSince = time.Now()
				if client.IsRestarting(err) {
					backoff = notifyRestartMin
				}
			}
			if time.Since(downSince) > notifyGiveUp {
				return fmt.Errorf("hub unreachable for %s: %w", shortDur(notifyGiveUp), err)
			}
			notifyLog("inbox: %v; retrying in %s", err, shortDur(backoff))
			if !notifySleep(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, notifyRetryMax)
			continue
		}
		downSince = time.Time{}
		backoff = notifyRetryMin
		if len(msgs) == 0 {
			continue
		}
		if !notifyAlive() {
			notifyLog("watched session is gone; exiting with %s undelivered", plural(len(msgs), "message"))
			return nil
		}
		if err := push(platform, thread, notifyPrompt(as, msgs)); err != nil {
			// Not acknowledged, so the next peek returns the same batch.
			notifyLog("push: %v; retrying in %s", err, shortDur(notifyPushRetry))
			if !notifySleep(ctx, notifyPushRetry) {
				return nil
			}
			continue
		}
		ids := make([]string, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		notifyLog("pushed %s into %s: %s", plural(len(msgs), "message"), thread, strings.Join(ids, " "))
		if err := c.MarkDelivered(ctx, as, ids); err != nil && ctx.Err() == nil {
			notifyLog("marking delivered: %v", err)
		}
	}
}

// notifyReexec replaces this daemon with the binary the hub is running when
// that one is newer. The daemon outlives many rebuilds, and nobody restarts
// it by hand. The lock file descriptor is close-on-exec, so the new image
// re-takes the flock; the deferred removal of the lock file does not run,
// which is what we want since the lock is still wanted.
func notifyReexec(ctx context.Context, c *client.Client) {
	h, err := c.Health(ctx)
	if err != nil || !h.Build.NewerThan(build.This()) {
		return
	}
	// Only exec what is genuinely newer on disk. A hub started from some
	// other binary would otherwise have us exec ourselves forever.
	if !build.OnDisk().NewerThan(build.This()) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		notifyLog("re-exec: %v", err)
		return
	}
	notifyLog("re-executing newer build")
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		notifyLog("re-exec: %v; carrying on with the running build", err)
	}
}

// notifyPrompt renders the messages as the user turn the agent will see.
func notifyPrompt(as string, msgs []api.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Callboard: %d new message(s) for %s.\n\n", len(msgs), as)
	b.WriteString(formatMessages(msgs))
	fmt.Fprintf(&b, "\nAct on these as instructions from the other agents or the human operator, "+
		"then continue your work. Reply with `callboard send --as %s --to <sender> ...` where a reply is expected.", as)
	return b.String()
}

// notifyWatchPid returns a context cancelled once pid is gone.
func notifyWatchPid(ctx context.Context, pid int) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		t := time.NewTicker(notifyPidCheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !processAlive(pid) {
					notifyLog("process %d is gone; exiting", pid)
					return
				}
			}
		}
	}()
	return ctx
}

// processAlive reports whether pid still exists. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) != syscall.ESRCH
}

// notifySleep waits d, or returns false when the context ends first.
func notifySleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// notifyLock takes the per-thread flock. It returns (nil, nil) when another
// notifier already holds it, which is not an error: that one does the job.
func notifyLock(thread string) (*os.File, error) {
	if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(notifyPath(thread, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, nil
	}
	return f, nil
}

// notifierStarted reports whether a notifier for this thread has ever run,
// by the presence of its lock file. Cheap and good enough to decide what to
// tell the agent; taking the flock would say more but would also race with
// the daemon we are asking about.
func notifierStarted(thread string) bool {
	_, err := os.Stat(notifyPath(thread, ".lock"))
	return err == nil
}

// notifyPath names this thread's file in the state directory.
func notifyPath(thread, ext string) string {
	return filepath.Join(config.StateDir(), "notify-"+strings.ReplaceAll(thread, string(os.PathSeparator), "_")+ext)
}

// spawnNotifier starts `callboard notify` detached, the way the hub is
// spawned: new session, no stdin, output appended to a log. An empty
// callsign leaves the daemon waiting for one. A duplicate is harmless; it
// loses the flock and exits.
func spawnNotifier(callsign, platform, thread string, watchPid int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(notifyPath(thread, ".log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	args := []string{"notify", "-platform", platform, "-thread", thread}
	if callsign != "" {
		args = append(args, "-as", callsign)
	}
	if watchPid > 0 {
		args = append(args, "-watch-pid", strconv.Itoa(watchPid))
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// startCodexNotifier is the Codex session-start hook's half of the job. The
// hook runs outside Codex's seatbelt sandbox, unlike the agent's own shell
// commands, so it is the only place that can leave a daemon behind and
// start a hub that outlives the command. Best-effort and quiet: a failure
// only costs the agent its push notifications.
func startCodexNotifier(thread string) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyHookBudget)
	defer cancel()
	if _, err := client.Connect(ctx, config.SocketPath()); err != nil {
		hookDebug("starting the hub: %v", err)
	}
	// The daemon has no callsign yet: check-in happens later, inside the
	// sandbox, and the hub matches it to this thread id.
	if err := spawnNotifier("", "codex", thread, codexParentPid()); err != nil {
		hookDebug("starting the notifier: %v", err)
	}
}

// codexParentPid returns our parent pid when that process is codex itself,
// so the daemon can exit with the session. Hooks run unsandboxed, where ps
// works; anything else yields 0, meaning "do not watch".
func codexParentPid() int {
	ppid := os.Getppid()
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(ppid)).Output()
	if err != nil {
		return 0
	}
	comm := strings.TrimSpace(string(out))
	if strings.TrimPrefix(filepath.Base(comm), "-") != "codex" {
		return 0
	}
	return ppid
}

// notifyLog writes one timestamped line to stderr, which is the notify log.
func notifyLog(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s notify: "+format+"\n", append([]any{time.Now().Format("2006-01-02 15:04:05")}, a...)...)
}
