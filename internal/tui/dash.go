// Package tui renders the callboard dashboard: sessions, jobs and the event
// feed in one Bubble Tea program.
package tui

import (
	"context"
	"errors"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/client"
)

const (
	feedMax     = 500              // events kept in memory
	pollWait    = 30 * time.Second // event long-poll
	refreshTick = 5 * time.Second  // roster/job refresh
	retryDelay  = 2 * time.Second  // after a client error
)

// Run shows the dashboard until the user quits or ctx is cancelled.
func Run(ctx context.Context, c *client.Client, scope string) error {
	p := tea.NewProgram(newModel(ctx, c, scope), tea.WithContext(ctx))
	_, err := p.Run()
	if err == nil || ctx.Err() != nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, tea.ErrInterrupted) {
		return nil
	}
	return err
}

// Messages produced by the background loops.
type (
	rosterMsg struct {
		sessions []api.Session
		jobs     []api.Job
	}
	eventsMsg struct {
		events []api.Event
		next   uint64
	}
	rosterErrMsg struct{ err error }
	eventsErrMsg struct{ err error }
	tickMsg      struct{}
	repollMsg    struct{}
)

type model struct {
	ctx context.Context
	c   *client.Client

	width, height int

	sessions []api.Session
	jobs     []api.Job
	events   []api.Event
	next     uint64 // event seq to poll from

	scope  string   // current filter, "" = all
	scopes []string // scopes seen, sorted; "" is prepended when cycling

	offset int // feed scroll, lines above the bottom; 0 follows
	err    string
}

func newModel(ctx context.Context, c *client.Client, scope string) *model {
	m := &model{ctx: ctx, c: c, scope: scope, width: 80, height: 24}
	if scope != "" {
		m.scopes = []string{scope}
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.fetchRoster(), m.backfill(), tick())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyPressMsg:
		return m, m.key(msg.String())

	case rosterMsg:
		m.sessions, m.jobs = msg.sessions, msg.jobs
		m.learnScopes()
		m.err = ""

	case eventsMsg:
		if len(msg.events) > 0 {
			m.events = append(m.events, msg.events...)
			if len(m.events) > feedMax {
				m.events = m.events[len(m.events)-feedMax:]
			}
			if m.offset > 0 { // hold the view still while scrolled up
				m.offset += len(msg.events)
			}
		}
		m.next = msg.next
		m.err = ""
		return m, m.pollEvents()

	case rosterErrMsg:
		m.err = msg.err.Error()

	case eventsErrMsg:
		m.err = msg.err.Error()
		return m, tea.Tick(retryDelay, func(time.Time) tea.Msg { return repollMsg{} })

	case repollMsg:
		return m, m.pollEvents()

	case tickMsg:
		return m, tea.Batch(m.fetchRoster(), tick())
	}
	return m, nil
}

func (m *model) key(k string) tea.Cmd {
	switch k {
	case "q", "ctrl+c", "esc":
		return tea.Quit
	case "j", "down":
		if m.offset > 0 {
			m.offset--
		}
	case "k", "up":
		m.offset++
	case "pgdown":
		m.offset = max(0, m.offset-m.feedHeight())
	case "pgup":
		m.offset += m.feedHeight()
	case "r":
		m.err = ""
		return m.fetchRoster()
	case "s":
		m.cycleScope()
		m.offset = 0
	}
	return nil
}

// cycleScope steps through "all" and every scope seen in the roster.
func (m *model) cycleScope() {
	all := append([]string{""}, m.scopes...)
	idx := 0
	for i, s := range all {
		if s == m.scope {
			idx = i
			break
		}
	}
	m.scope = all[(idx+1)%len(all)]
}

// learnScopes records scopes from sessions and jobs.
func (m *model) learnScopes() {
	seen := map[string]bool{}
	for _, s := range m.scopes {
		seen[s] = true
	}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			m.scopes = append(m.scopes, s)
		}
	}
	for _, s := range m.sessions {
		for _, sc := range s.Scopes {
			add(sc)
		}
	}
	for _, j := range m.jobs {
		add(j.Scope)
	}
	sort.Strings(m.scopes)
}

// Commands.

func tick() tea.Cmd {
	return tea.Tick(refreshTick, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) fetchRoster() tea.Cmd {
	if m.c == nil {
		return nil
	}
	ctx, c := m.ctx, m.c
	return func() tea.Msg {
		sessions, err := c.Sessions(ctx, "")
		if err != nil {
			return rosterErrMsg{err}
		}
		jobs, err := c.Jobs(ctx, "", "")
		if err != nil {
			return rosterErrMsg{err}
		}
		return rosterMsg{sessions: sessions, jobs: jobs}
	}
}

// backfill loads the whole event history once, without waiting.
func (m *model) backfill() tea.Cmd {
	if m.c == nil {
		return nil
	}
	ctx, c := m.ctx, m.c
	return func() tea.Msg {
		resp, err := c.Events(ctx, 0, 0, "")
		if err != nil {
			return eventsErrMsg{err}
		}
		if len(resp.Events) > feedMax {
			resp.Events = resp.Events[len(resp.Events)-feedMax:]
		}
		return eventsMsg{events: resp.Events, next: resp.Next}
	}
}

// pollEvents long-polls the stream from the last seq seen.
func (m *model) pollEvents() tea.Cmd {
	if m.c == nil {
		return nil
	}
	ctx, c, since := m.ctx, m.c, m.next
	return func() tea.Msg {
		resp, err := c.Events(ctx, since, pollWait, "")
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return eventsErrMsg{err}
		}
		if resp.Next < since {
			resp.Next = since
		}
		return eventsMsg{events: resp.Events, next: resp.Next}
	}
}
