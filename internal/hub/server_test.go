package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/client"
)

var sockN atomic.Int32

// tempSocket returns a short socket path; macOS caps them at ~104 bytes.
func tempSocket(t *testing.T) string {
	t.Helper()
	p := filepath.Join(os.TempDir(), fmt.Sprintf("cb-%d-%d.sock", os.Getpid(), sockN.Add(1)))
	os.Remove(p)
	t.Cleanup(func() { os.Remove(p) })
	return p
}

// serveHub runs Handler on a unix socket and returns a client for it.
func serveHub(t *testing.T, h *Hub) (*client.Client, string) {
	t.Helper()
	sock := tempSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: Handler(h)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return client.New(sock), sock
}

// get performs a raw request against the socket and returns the status code.
func get(t *testing.T, sock, path string) int {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := hc.Get("http://callboard" + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestServerRoundTrip(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now, Version: "test"})
	cl, _ := serveHub(t, h)
	ctx := context.Background()

	health, err := cl.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !health.OK || health.Version != "test" || health.PID != os.Getpid() {
		t.Fatalf("health = %+v", health)
	}

	a, err := cl.Checkin(ctx, api.CheckinRequest{Name: "alice", Scopes: []string{"proj"}, PlatformSession: "ps-1"})
	if err != nil {
		t.Fatalf("checkin: %v", err)
	}
	b, err := cl.Checkin(ctx, api.CheckinRequest{Name: "bob", Scopes: []string{"proj"}})
	if err != nil {
		t.Fatalf("checkin: %v", err)
	}
	as, bs := a.Session.Callsign, b.Session.Callsign

	resolved, err := cl.Resolve(ctx, api.ResolveRequest{PlatformSession: "ps-1"})
	if err != nil || resolved.Callsign != as {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}

	sessions, err := cl.Sessions(ctx, "proj")
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}

	sent, err := cl.Send(ctx, api.SendRequest{As: as, To: bs, Body: "hello"})
	if err != nil || len(sent.Recipients) != 1 {
		t.Fatalf("send = %+v, %v", sent, err)
	}

	peeked, err := cl.Inbox(ctx, bs, 0, true)
	if err != nil || len(peeked) != 1 {
		t.Fatalf("peek = %+v, %v", peeked, err)
	}
	read, err := cl.Inbox(ctx, bs, 0, false)
	if err != nil || len(read) != 1 || read[0].Body != "hello" {
		t.Fatalf("inbox = %+v, %v", read, err)
	}
	if again, err := cl.Inbox(ctx, bs, 0, false); err != nil || len(again) != 0 {
		t.Fatalf("second inbox = %+v, %v; want empty", again, err)
	}

	hist, err := cl.History(ctx, as)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %+v, %v", hist, err)
	}

	job, err := cl.PostJob(ctx, api.PostJobRequest{As: as, Title: "ship it", Body: "please"})
	if err != nil || job.Status != api.JobOpen {
		t.Fatalf("post job = %+v, %v", job, err)
	}
	jobs, err := cl.Jobs(ctx, "proj", api.JobOpen)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	one, err := cl.Job(ctx, job.ID)
	if err != nil || one.ID != job.ID {
		t.Fatalf("job = %+v, %v", one, err)
	}
	claimed, err := cl.ClaimJob(ctx, job.ID, bs)
	if err != nil || claimed.ClaimedBy != bs {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	done, err := cl.DoneJob(ctx, job.ID, bs, "shipped")
	if err != nil || done.Status != api.JobDone || done.Result != "shipped" {
		t.Fatalf("done = %+v, %v", done, err)
	}

	second, err := cl.PostJob(ctx, api.PostJobRequest{As: as, Title: "nope"})
	if err != nil {
		t.Fatalf("post job: %v", err)
	}
	if _, err := cl.ClaimJob(ctx, second.ID, bs); err != nil {
		t.Fatalf("claim: %v", err)
	}
	failed, err := cl.FailJob(ctx, second.ID, bs, "broken")
	if err != nil || failed.Status != api.JobFailed || failed.Result != "broken" {
		t.Fatalf("fail = %+v, %v", failed, err)
	}

	events, err := cl.Events(ctx, 0, 0, "")
	if err != nil || len(events.Events) == 0 || events.Next == 0 {
		t.Fatalf("events = %d events, next %d, %v", len(events.Events), events.Next, err)
	}

	if err := cl.Checkout(ctx, bs); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	sessions, err = cl.Sessions(ctx, "")
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	for _, s := range sessions {
		if s.Callsign == bs {
			t.Fatalf("%s still on the roster after checkout", bs)
		}
	}
}

func TestServerErrorMapping(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now, Version: "test"})
	cl, sock := serveHub(t, h)
	ctx := context.Background()

	_, err := cl.Job(ctx, "j404")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("job error = %v (%T), want *client.APIError", err, err)
	}
	if apiErr.Code != http.StatusNotFound || !client.IsNotFound(err) {
		t.Fatalf("job error code = %d, want 404", apiErr.Code)
	}
	if apiErr.Msg == "" {
		t.Fatal("error message was dropped")
	}

	if _, err := cl.Send(ctx, api.SendRequest{As: "ghost", To: "nobody", Body: "x"}); !client.IsNotFound(err) {
		t.Fatalf("send as unknown session = %v, want 404", err)
	}
	if code := get(t, sock, "/v1/inbox?as=x&wait=soon"); code != http.StatusBadRequest {
		t.Fatalf("bad wait = %d, want 400", code)
	}
	if code := get(t, sock, "/v1/events?since=abc"); code != http.StatusBadRequest {
		t.Fatalf("bad since = %d, want 400", code)
	}
	if code := get(t, sock, "/v1/nope"); code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", code)
	}
}

func TestServerWaitParam(t *testing.T) {
	c := newClock()
	h := newHub(t, Options{Now: c.now, Version: "test"})
	cl, _ := serveHub(t, h)
	ctx := context.Background()

	a, err := cl.Checkin(ctx, api.CheckinRequest{Name: "alice"})
	if err != nil {
		t.Fatalf("checkin: %v", err)
	}
	start := time.Now()
	msgs, err := cl.Inbox(ctx, a.Session.Callsign, 120*time.Millisecond, false)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("inbox = %+v, %v; want empty", msgs, err)
	}
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("returned after %s: the wait parameter did not reach the hub", d)
	}
}

func TestWaitParam(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		bad  bool
	}{
		{in: "", want: 0},
		{in: "540s", want: 540 * time.Second},
		{in: "1h", want: maxWait},
		{in: "-5s", want: 0},
		{in: "soon", bad: true},
	} {
		got, err := waitParam(tc.in)
		if tc.bad {
			if errCode(err) != 400 {
				t.Fatalf("waitParam(%q) = %v, want a 400", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("waitParam(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestServeLocksAndShutsDown(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "hub.lock")
	sock := tempSocket(t)
	c := newClock()
	h := newHub(t, Options{Now: c.now, Version: "test"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, h, sock, lock, "test") }()

	cl := client.New(sock)
	var health api.HealthResponse
	var err error
	for range 60 {
		if health, err = cl.Health(context.Background()); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("hub never answered: %v", err)
	}
	if health.Socket != sock || health.Version != "test" {
		t.Fatalf("health = %+v", health)
	}
	if fi, err := os.Stat(sock); err != nil {
		t.Fatalf("stat socket: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", fi.Mode().Perm())
	}

	second := Serve(context.Background(), h, tempSocket(t), lock, "test")
	if !errors.Is(second, ErrAlreadyRunning) {
		t.Fatalf("second Serve = %v, want ErrAlreadyRunning", second)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil on clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the context was cancelled")
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after shutdown: %v", err)
	}
}
