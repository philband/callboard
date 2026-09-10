package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/philband/callboard/internal/api"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

func fixture(t *testing.T) *model {
	t.Helper()
	m := newModel(context.Background(), nil, "")
	now := time.Now()

	send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	send(m, rosterMsg{
		sessions: []api.Session{
			{Callsign: "alice", Role: "worker", Platform: "claude-code",
				Scopes: []string{"github.com/philband/callboard"}, LastSeen: now, Status: api.StatusActive},
			{Callsign: "coord", Role: "coordinator", Platform: "codex",
				Scopes: []string{"github.com/philband/callboard"}, LastSeen: now.Add(-5 * time.Minute), Status: api.StatusIdle},
			{Callsign: "ghost", Role: "worker", Platform: "cursor",
				Scopes: []string{"other/repo"}, LastSeen: now.Add(-time.Hour), Status: api.StatusGone},
			{Callsign: "human", Scopes: nil, LastSeen: now, Status: api.StatusHuman},
		},
		jobs: []api.Job{
			{ID: "j3", Scope: "github.com/philband/callboard", Title: "wire up the dashboard",
				PostedBy: "coord", Status: api.JobOpen, PostedAt: now},
			{ID: "j4", Scope: "other/repo", Title: "rebuild the hub",
				PostedBy: "coord", ClaimedBy: "alice", Status: api.JobClaimed, PostedAt: now},
		},
	})
	send(m, eventsMsg{next: 9, events: []api.Event{
		{Seq: 1, Time: now, Type: api.EvCheckin, Session: &api.Session{
			Callsign: "alice", Role: "worker", Scopes: []string{"github.com/philband/callboard"}}},
		{Seq: 2, Time: now, Type: api.EvMessage, Message: &api.Message{
			From: "alice", To: "bob", Kind: api.KindChat, Scope: "github.com/philband/callboard",
			Body: "first line of body\nsecond line"}},
		{Seq: 3, Time: now, Type: api.EvDelivered, Callsign: "bob", MessageIDs: []string{"m12", "m13"}},
		{Seq: 4, Time: now, Type: api.EvJobPost, Callsign: "coord", Job: &api.Job{
			ID: "j3", PostedBy: "coord", Title: "wire up the dashboard", Scope: "github.com/philband/callboard"}},
		{Seq: 5, Time: now, Type: api.EvJobClaim, Callsign: "alice", Job: &api.Job{ID: "j3"}},
		{Seq: 6, Time: now, Type: api.EvJobDone, Callsign: "alice", Job: &api.Job{ID: "j3"}},
		{Seq: 7, Time: now, Type: api.EvJobFail, Callsign: "alice", Job: &api.Job{ID: "j4"}},
		{Seq: 8, Time: now, Type: api.EvCheckout, Callsign: "alice",
			Session: &api.Session{Callsign: "alice"}},
	}})
	return m
}

func send(m *model, msg tea.Msg) {
	if _, cmd := m.Update(msg); cmd != nil {
		_ = cmd // commands need a hub; the model state is what we assert on
	}
}

func view(t *testing.T, m *model) string {
	t.Helper()
	v := m.View()
	if !v.AltScreen {
		t.Fatal("view does not request the alt screen")
	}
	return plain(v.Content)
}

func TestViewShowsPanes(t *testing.T) {
	m := fixture(t)
	out := view(t, m)

	for _, want := range []string{
		"SESSIONS", "JOBS", "FEED",
		"alice", "coord", "human",
		"j3", "wire up the dashboard",
		"checkin", "alice (worker) joined",
		"alice -> bob (chat) first line of body",
		"bob read m12,m13",
		"coord posted j3",
		"alice claimed j3",
		"alice finished j3",
		"alice failed j4",
		"alice left",
		"q quit", "scope all",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "second line") {
		t.Errorf("message summary must stop at the first line\n%s", out)
	}
}

func TestViewGeometry(t *testing.T) {
	for _, w := range []int{20, 40, 80, 100, 160} {
		for _, h := range []int{1, 3, 10, 24, 50} {
			m := fixture(t)
			send(m, tea.WindowSizeMsg{Width: w, Height: h})
			out := view(t, m)
			lines := strings.Split(out, "\n")
			if len(lines) != h {
				t.Fatalf("%dx%d: got %d lines, want %d", w, h, len(lines), h)
			}
			for i, l := range lines {
				if n := utf8.RuneCountInString(l); n != w {
					t.Fatalf("%dx%d: line %d is %d columns wide, want %d: %q", w, h, i, n, w, l)
				}
			}
			if h >= 10 && !strings.Contains(out, "FEED") {
				t.Errorf("%dx%d: no feed pane\n%s", w, h, out)
			}
		}
	}
}

func TestEmptyModelRenders(t *testing.T) {
	m := newModel(context.Background(), nil, "")
	out := view(t, m)
	if !strings.Contains(out, "SESSIONS (0)") || !strings.Contains(out, "FEED (0)") {
		t.Errorf("empty dashboard should still draw its panes\n%s", out)
	}
}

func TestScopeCycleFilters(t *testing.T) {
	m := fixture(t)
	m.learnScopes()

	m.key("s")
	if m.scope != "github.com/philband/callboard" {
		t.Fatalf("first cycle went to %q", m.scope)
	}
	out := view(t, m)
	if strings.Contains(out, "ghost") {
		t.Errorf("scope filter should hide sessions from other scopes\n%s", out)
	}
	if !strings.Contains(out, "human") {
		t.Errorf("the human session has no scope and must always show\n%s", out)
	}
	if !strings.Contains(out, "bob read m12,m13") {
		t.Errorf("hub-wide events must survive the scope filter\n%s", out)
	}

	m.key("s")
	if m.scope != "other/repo" {
		t.Fatalf("second cycle went to %q", m.scope)
	}
	m.key("s")
	if m.scope != "" {
		t.Fatalf("cycle should wrap back to all, got %q", m.scope)
	}
}

func TestFeedScroll(t *testing.T) {
	m := fixture(t)
	send(m, tea.WindowSizeMsg{Width: 80, Height: 12})

	m.key("k")
	if m.offset != 1 {
		t.Fatalf("k should scroll up, offset %d", m.offset)
	}
	out := view(t, m)
	if !strings.Contains(out, "scrolled up") {
		t.Errorf("a scrolled feed should say so\n%s", out)
	}
	if strings.Contains(out, "alice left") {
		t.Errorf("the newest line should be scrolled out of view\n%s", out)
	}

	// New events must not drag a scrolled-up view along.
	before := m.offset
	send(m, eventsMsg{next: 10, events: []api.Event{
		{Seq: 9, Time: time.Now(), Type: api.EvCheckout, Callsign: "coord"},
	}})
	if m.offset != before+1 {
		t.Fatalf("offset should follow the new line, got %d want %d", m.offset, before+1)
	}

	m.key("j")
	m.key("j")
	if m.offset != 0 {
		t.Fatalf("j should scroll back down to the bottom, offset %d", m.offset)
	}
	if out := view(t, m); !strings.Contains(out, "coord left") {
		t.Errorf("newest event should be visible at the bottom\n%s", out)
	}
}

func TestQuitAndErrors(t *testing.T) {
	m := fixture(t)
	if cmd := m.key("q"); cmd == nil {
		t.Fatal("q should quit")
	}
	send(m, rosterErrMsg{err: context.DeadlineExceeded})
	out := view(t, m)
	if !strings.Contains(out, context.DeadlineExceeded.Error()) {
		t.Errorf("client errors belong in the footer\n%s", out)
	}
}
