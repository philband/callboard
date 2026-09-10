package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/philband/callboard/internal/api"
)

// clock is the injectable time source the tests use for status windows.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newHub(t *testing.T, opts Options) *Hub {
	t.Helper()
	h, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func checkin(t *testing.T, h *Hub, name string, scopes ...string) string {
	t.Helper()
	res, err := h.Checkin(api.CheckinRequest{Name: name, Scopes: scopes})
	if err != nil {
		t.Fatalf("checkin %q: %v", name, err)
	}
	return res.Session.Callsign
}

func send(t *testing.T, h *Hub, from, to, body string) api.SendResponse {
	t.Helper()
	res, err := h.Send(api.SendRequest{As: from, To: to, Body: body})
	if err != nil {
		t.Fatalf("send %s->%s: %v", from, to, err)
	}
	return res
}

// errCode is the HTTP status the hub attached to err, or 0.
func errCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	if errCode(err) != code {
		t.Fatalf("got error %v (code %d), want code %d", err, errCode(err), code)
	}
}

func inbox(t *testing.T, h *Hub, as string, peek bool) []api.Message {
	t.Helper()
	msgs, err := h.Inbox(context.Background(), as, 0, peek)
	if err != nil {
		t.Fatalf("inbox %s: %v", as, err)
	}
	return msgs
}

func TestCheckinMintsCallsigns(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})

	if got := checkin(t, h, "Alice B"); got != "alice-b" {
		t.Fatalf("first callsign = %q, want alice-b", got)
	}
	if got := checkin(t, h, "Alice B"); got != "alice-b-2" {
		t.Fatalf("second callsign = %q, want alice-b-2", got)
	}
	if _, err := h.Checkin(api.CheckinRequest{Name: "human"}); err == nil {
		t.Fatal("checking in as human should be rejected")
	} else {
		wantCode(t, err, 400)
	}
}

func TestCheckinReclaimsGoneSessionWithInbox(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	alice := checkin(t, h, "alice")
	bob := checkin(t, h, "bob")
	send(t, h, bob, alice, "still there?")

	c.advance(api.IdleWindow + time.Minute)
	res, err := h.Checkin(api.CheckinRequest{Name: "alice"})
	if err != nil {
		t.Fatalf("re-checkin: %v", err)
	}
	if res.Session.Callsign != alice || !res.Reclaimed {
		t.Fatalf("got %q reclaimed=%v, want %q reclaimed=true", res.Session.Callsign, res.Reclaimed, alice)
	}
	if res.Pending != 1 {
		t.Fatalf("pending = %d, want 1", res.Pending)
	}
	msgs := inbox(t, h, alice, false)
	if len(msgs) != 1 || msgs[0].Body != "still there?" {
		t.Fatalf("inbox after reclaim = %+v", msgs)
	}
}

func TestSendRouting(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	a := checkin(t, h, "a", "proj")
	b := checkin(t, h, "b", "proj")
	d := checkin(t, h, "d", "other")

	if got := send(t, h, a, b, "hi").Recipients; len(got) != 1 || got[0] != b {
		t.Fatalf("direct recipients = %v, want [%s]", got, b)
	}
	if got := send(t, h, a, api.ScopePrefix+"proj", "team").Recipients; len(got) != 1 || got[0] != b {
		t.Fatalf("scope recipients = %v, want [%s] (sender excluded, other scope excluded)", got, b)
	}
	got := send(t, h, a, api.Broadcast, "all").Recipients
	want := map[string]bool{b: true, d: true, api.Human: true}
	if len(got) != len(want) {
		t.Fatalf("broadcast recipients = %v, want %v", got, want)
	}
	for _, r := range got {
		if !want[r] {
			t.Fatalf("broadcast reached %q; recipients = %v", r, got)
		}
	}

	_, err := h.Send(api.SendRequest{As: a, To: "nobody", Body: "x"})
	wantCode(t, err, 404)
	_, err = h.Send(api.SendRequest{As: a, To: a, Body: "x"})
	wantCode(t, err, 400)
}

func TestInboxDeliverAndPeek(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	a := checkin(t, h, "a")
	b := checkin(t, h, "b")
	send(t, h, a, b, "one")

	if msgs := inbox(t, h, b, true); len(msgs) != 1 {
		t.Fatalf("peek = %d messages, want 1", len(msgs))
	}
	if msgs := inbox(t, h, b, true); len(msgs) != 1 {
		t.Fatalf("second peek = %d messages, want 1 (peek must not deliver)", len(msgs))
	}
	if msgs := inbox(t, h, b, false); len(msgs) != 1 {
		t.Fatalf("read = %d messages, want 1", len(msgs))
	}
	if msgs := inbox(t, h, b, false); len(msgs) != 0 {
		t.Fatalf("second read = %d messages, want 0", len(msgs))
	}
}

func TestInboxLongPollWakes(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	a := checkin(t, h, "a")
	b := checkin(t, h, "b")

	go func() {
		time.Sleep(30 * time.Millisecond)
		h.Send(api.SendRequest{As: a, To: b, Body: "late"})
	}()
	start := time.Now()
	msgs, err := h.Inbox(context.Background(), b, 5*time.Second, false)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "late" {
		t.Fatalf("long-poll returned %+v", msgs)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("long-poll took %s, should have woken on the send", time.Since(start))
	}
}

func TestInboxLongPollTimeoutAndCancel(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	a := checkin(t, h, "a")

	start := time.Now()
	msgs, err := h.Inbox(context.Background(), a, 40*time.Millisecond, false)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("timeout poll = %+v, %v; want empty, nil", msgs, err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatalf("returned after %s, want at least the wait", time.Since(start))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	msgs, err = h.Inbox(ctx, a, 10*time.Second, false)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("cancelled poll = %+v, %v; want empty, nil", msgs, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancelled poll ignored the context")
	}
}

func TestPostJobNotifiesScope(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	a := checkin(t, h, "a", "proj")
	b := checkin(t, h, "b", "proj")
	d := checkin(t, h, "d", "other")

	j, err := h.PostJob(api.PostJobRequest{As: a, Title: "fix the thing"})
	if err != nil {
		t.Fatalf("post job: %v", err)
	}
	if j.Scope != "proj" || j.Status != api.JobOpen {
		t.Fatalf("job = %+v", j)
	}
	msgs := inbox(t, h, b, true)
	if len(msgs) != 1 || msgs[0].Kind != api.KindJob || msgs[0].Ref != j.ID {
		t.Fatalf("scope member inbox = %+v, want one job message referencing %s", msgs, j.ID)
	}
	if msgs := inbox(t, h, d, true); len(msgs) != 0 {
		t.Fatalf("out-of-scope session got %+v", msgs)
	}
	if msgs := inbox(t, h, a, true); len(msgs) != 0 {
		t.Fatalf("poster got its own job message: %+v", msgs)
	}
}

func TestClaimIsAtomic(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	poster := checkin(t, h, "poster", "proj")
	j, err := h.PostJob(api.PostJobRequest{As: poster, Title: "race me"})
	if err != nil {
		t.Fatalf("post job: %v", err)
	}

	const n = 20
	workers := make([]string, n)
	for i := range workers {
		workers[i] = checkin(t, h, "w", "proj")
	}
	var wg sync.WaitGroup
	results := make([]error, n)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, results[i] = h.ClaimJob(j.ID, workers[i])
		}()
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range results {
		if err == nil {
			won++
			continue
		}
		if errCode(err) != 409 {
			t.Fatalf("worker %d: got %v (code %d), want 409", i, err, errCode(err))
		}
	}
	if won != 1 {
		t.Fatalf("%d workers claimed the job, want exactly 1", won)
	}
	got, err := h.Job(j.ID)
	if err != nil || got.Status != api.JobClaimed || got.ClaimedBy == "" {
		t.Fatalf("job after race = %+v, %v", got, err)
	}
}

func TestJobAssigneeAndLifecycle(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	a := checkin(t, h, "a", "proj")
	b := checkin(t, h, "b", "proj")
	d := checkin(t, h, "d", "proj")

	assigned, err := h.PostJob(api.PostJobRequest{As: a, Title: "for b", To: b})
	if err != nil {
		t.Fatalf("post assigned job: %v", err)
	}
	if _, err := h.ClaimJob(assigned.ID, d); err == nil {
		t.Fatal("non-assignee claimed a pre-assigned job")
	} else {
		wantCode(t, err, 409)
	}
	if _, err := h.ClaimJob(assigned.ID, b); err != nil {
		t.Fatalf("assignee claim: %v", err)
	}

	// done on an unclaimed job is a conflict
	open, err := h.PostJob(api.PostJobRequest{As: a, Title: "untouched"})
	if err != nil {
		t.Fatalf("post job: %v", err)
	}
	_, err = h.CloseJob(open.ID, a, api.JobDone, "")
	wantCode(t, err, 409)

	// the claimer finishing notifies the poster
	inbox(t, h, a, false) // drain
	done, err := h.CloseJob(assigned.ID, b, api.JobDone, "shipped")
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if done.Status != api.JobDone || done.Result != "shipped" {
		t.Fatalf("done job = %+v", done)
	}
	msgs := inbox(t, h, a, false)
	if len(msgs) != 1 || msgs[0].Ref != assigned.ID || msgs[0].Kind != api.KindSystem {
		t.Fatalf("poster inbox after done = %+v", msgs)
	}

	// the poster may fail a job someone else claimed
	other, err := h.PostJob(api.PostJobRequest{As: a, Title: "abandon"})
	if err != nil {
		t.Fatalf("post job: %v", err)
	}
	if _, err := h.ClaimJob(other.ID, d); err != nil {
		t.Fatalf("claim: %v", err)
	}
	failed, err := h.CloseJob(other.ID, a, api.JobFailed, "no longer needed")
	if err != nil {
		t.Fatalf("fail by poster: %v", err)
	}
	if failed.Status != api.JobFailed {
		t.Fatalf("failed job = %+v", failed)
	}
	if _, err := h.CloseJob(other.ID, a, api.JobDone, ""); err == nil {
		t.Fatal("closing an already closed job should conflict")
	} else {
		wantCode(t, err, 409)
	}
}

func TestEventsSinceAndScope(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now})
	a := checkin(t, h, "a", "proj")
	b := checkin(t, h, "b", "proj")
	checkin(t, h, "d", "other")
	send(t, h, a, b, "hi")

	all, err := h.Events(context.Background(), 0, 0, "")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(all.Events) == 0 || all.Next != h.Seq() {
		t.Fatalf("events = %d, next = %d, seq = %d", len(all.Events), all.Next, h.Seq())
	}
	for i, ev := range all.Events {
		if i > 0 && ev.Seq <= all.Events[i-1].Seq {
			t.Fatalf("events out of order at %d: %v", i, all.Events)
		}
	}

	rest, err := h.Events(context.Background(), all.Next, 0, "")
	if err != nil || len(rest.Events) != 0 {
		t.Fatalf("events since next = %+v, %v; want empty", rest.Events, err)
	}

	scoped, err := h.Events(context.Background(), 0, 0, "proj")
	if err != nil {
		t.Fatalf("scoped events: %v", err)
	}
	for _, ev := range scoped.Events {
		if s := ev.ScopeName(); s != "" && s != "proj" {
			t.Fatalf("scope filter leaked %s event for scope %q", ev.Type, s)
		}
	}
	if len(scoped.Events) >= len(all.Events) {
		t.Fatalf("scope filter kept %d of %d events", len(scoped.Events), len(all.Events))
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		h.Send(api.SendRequest{As: a, To: b, Body: "later"})
	}()
	start := time.Now()
	woke, err := h.Events(context.Background(), all.Next, 5*time.Second, "")
	if err != nil {
		t.Fatalf("long-poll events: %v", err)
	}
	if len(woke.Events) == 0 {
		t.Fatal("long-poll returned no events after a commit")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("long-poll took %s, should have woken on the commit", time.Since(start))
	}
}

// snapshot is the state the journal must reproduce, as stable JSON.
func snapshot(t *testing.T, h *Hub) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	sessions := map[string]api.Session{}
	for cs, s := range h.sessions {
		sessions[cs] = *s
	}
	jobs := map[string]api.Job{}
	for id, j := range h.jobs {
		jobs[id] = *j
	}
	b, err := json.Marshal(struct {
		Sessions map[string]api.Session
		Inbox    map[string][]api.Message
		Jobs     map[string]api.Job
		Order    []string
		Seq      uint64
	}{sessions, h.inbox, jobs, h.jobOrder, h.seq})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return string(b)
}

func TestJournalReplayRestoresState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "journal.jsonl")
	c := newClock()
	h, err := New(Options{JournalPath: path, Now: c.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := checkin(t, h, "alice", "proj")
	b := checkin(t, h, "bob", "proj")
	gone := checkin(t, h, "carol", "proj")
	send(t, h, a, b, "delivered later")
	send(t, h, a, api.ScopePrefix+"proj", "team message")
	inbox(t, h, b, false) // b's queue is emptied, a's job messages stay put
	j, err := h.PostJob(api.PostJobRequest{As: a, Title: "do it"})
	if err != nil {
		t.Fatalf("post job: %v", err)
	}
	if _, err := h.ClaimJob(j.ID, b); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := h.CloseJob(j.ID, b, api.JobDone, "done"); err != nil {
		t.Fatalf("done: %v", err)
	}
	if _, err := h.PostJob(api.PostJobRequest{As: a, Title: "still open"}); err != nil {
		t.Fatalf("post job: %v", err)
	}
	if err := h.Checkout(gone); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	want := snapshot(t, h)
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	h2, err := New(Options{JournalPath: path, Now: c.now})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer h2.Close()
	if got := snapshot(t, h2); got != want {
		t.Fatalf("replayed state differs\n got: %s\nwant: %s", got, want)
	}
	if pending := inbox(t, h2, a, true); len(pending) == 0 {
		t.Fatal("undelivered inbox was lost on replay")
	}
}

func TestJournalSkipsGarbageLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	c := newClock()
	h, err := New(Options{JournalPath: path, Now: c.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := checkin(t, h, "alice", "proj")
	b := checkin(t, h, "bob", "proj")
	send(t, h, a, b, "keep me")
	h.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","mes`); err != nil { // truncated write
		t.Fatal(err)
	}
	f.Close()

	j, err := openJournal(path)
	if err != nil {
		t.Fatalf("openJournal: %v", err)
	}
	defer j.close()
	n, skipped, err := j.replay(func(api.Event) {})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if n != 3 {
		t.Fatalf("replayed %d events, want 3", n)
	}

	h2, err := New(Options{JournalPath: path, Now: c.now})
	if err != nil {
		t.Fatalf("New over a damaged journal: %v", err)
	}
	defer h2.Close()
	if msgs := inbox(t, h2, b, true); len(msgs) != 1 {
		t.Fatalf("inbox after replay = %+v, want the one good message", msgs)
	}
}
