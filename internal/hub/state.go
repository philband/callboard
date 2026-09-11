// Package hub implements the coordination hub: sessions, messages, jobs, the
// journal and the HTTP API.
package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/build"
)

// Error carries an HTTP status for the API layer.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func badRequest(format string, a ...any) error { return &Error{400, fmt.Sprintf(format, a...)} }
func notFound(format string, a ...any) error   { return &Error{404, fmt.Sprintf(format, a...)} }
func conflict(format string, a ...any) error   { return &Error{409, fmt.Sprintf(format, a...)} }

// restarting is what a long poll returns once the hub starts shutting down.
// Clients recognise it and reconnect instead of reporting a failure.
func restarting() error { return &Error{503, api.MsgHubRestarting} }

// Options configure a Hub.
type Options struct {
	// JournalPath is the append-only event log. Empty keeps state in memory.
	JournalPath string
	Version     string
	// Now is overridable for tests.
	Now func() time.Time
}

// Hub holds all state behind one mutex. Every mutation is an Event that is
// applied to memory and appended to the journal; restart replays the journal.
type Hub struct {
	mu       sync.Mutex
	seq      uint64
	nmsg     int
	njob     int
	sessions map[string]*api.Session
	inbox    map[string][]api.Message // undelivered, per callsign
	history  []api.Message
	jobs     map[string]*api.Job
	jobOrder []string
	events   []api.Event
	journal  *journal
	changed  chan struct{} // closed and replaced on every commit
	closing  chan struct{} // closed once a graceful shutdown starts
	stopOnce sync.Once
	now      func() time.Time
	version  string
	build    build.Info
}

// New creates a hub and replays the journal if one is configured.
func New(opts Options) (*Hub, error) {
	h := &Hub{
		sessions: map[string]*api.Session{},
		inbox:    map[string][]api.Message{},
		jobs:     map[string]*api.Job{},
		changed:  make(chan struct{}),
		closing:  make(chan struct{}),
		now:      opts.Now,
		version:  opts.Version,
		build:    build.This(),
	}
	if opts.Version != "" {
		h.build.Version = opts.Version
	}
	if h.now == nil {
		h.now = time.Now
	}
	h.sessions[api.Human] = &api.Session{Callsign: api.Human, Name: "human", Platform: "human"}
	if opts.JournalPath != "" {
		j, err := openJournal(opts.JournalPath)
		if err != nil {
			return nil, err
		}
		h.journal = j
		_, skipped, err := j.replay(h.apply)
		if err != nil {
			return nil, err
		}
		if skipped > 0 {
			fmt.Fprintf(os.Stderr, "journal: skipped %d unreadable line(s)\n", skipped)
		}
	}
	return h, nil
}

// Build reports the executable this hub is running, read once at start. The
// file on disk may be replaced while the hub runs; this stays what we are.
func (h *Hub) Build() build.Info { return h.build }

// Closing is closed once BeginShutdown has been called. Long polls select on
// it so a restart drains in milliseconds instead of holding clients for hours.
func (h *Hub) Closing() <-chan struct{} { return h.closing }

// BeginShutdown starts a graceful shutdown: in-flight long polls end with
// restarting() and Serve stops the listener. Calling it twice is harmless.
func (h *Hub) BeginShutdown() { h.stopOnce.Do(func() { close(h.closing) }) }

// Close flushes and closes the journal.
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.journal != nil {
		return h.journal.close()
	}
	return nil
}

// Seq returns the latest event sequence number.
func (h *Hub) Seq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// commit stamps, applies, journals and announces an event. Caller holds mu.
func (h *Hub) commit(ev api.Event) (api.Event, error) {
	ev.Seq = h.seq + 1
	ev.Time = h.now()
	if h.journal != nil {
		if err := h.journal.append(ev); err != nil {
			return ev, err
		}
	}
	h.apply(ev)
	close(h.changed)
	h.changed = make(chan struct{})
	return ev, nil
}

// apply mutates state for an event. It is used both for live commits and
// journal replay, so it must be deterministic and must not journal.
func (h *Hub) apply(ev api.Event) {
	if ev.Seq > h.seq {
		h.seq = ev.Seq
	}
	h.events = append(h.events, ev)
	switch ev.Type {
	case api.EvCheckin:
		s := *ev.Session
		h.sessions[s.Callsign] = &s
	case api.EvCheckout:
		delete(h.sessions, ev.Callsign)
		delete(h.inbox, ev.Callsign)
	case api.EvMessage:
		h.nmsg++
		m := *ev.Message
		h.history = append(h.history, m)
		for _, r := range ev.Recipients {
			h.inbox[r] = append(h.inbox[r], m)
		}
	case api.EvDelivered:
		drop := map[string]bool{}
		for _, id := range ev.MessageIDs {
			drop[id] = true
		}
		q := h.inbox[ev.Callsign][:0]
		for _, m := range h.inbox[ev.Callsign] {
			if !drop[m.ID] {
				q = append(q, m)
			}
		}
		h.inbox[ev.Callsign] = q
	case api.EvJobPost:
		h.njob++
		j := *ev.Job
		h.jobs[j.ID] = &j
		h.jobOrder = append(h.jobOrder, j.ID)
	case api.EvJobClaim, api.EvJobDone, api.EvJobFail:
		j := *ev.Job
		h.jobs[j.ID] = &j
	}
}

// status computes a session's status from last-seen. Caller holds mu.
func (h *Hub) status(s *api.Session) string {
	if s.Callsign == api.Human {
		return api.StatusHuman
	}
	age := h.now().Sub(s.LastSeen)
	switch {
	case age < api.ActiveWindow:
		return api.StatusActive
	case age < api.IdleWindow:
		return api.StatusIdle
	}
	return api.StatusGone
}

func (h *Hub) view(s *api.Session) api.Session {
	v := *s
	v.Status = h.status(s)
	return v
}

// session returns a live session or a not-found error. Caller holds mu.
func (h *Hub) session(callsign string) (*api.Session, error) {
	s, ok := h.sessions[callsign]
	if !ok {
		return nil, notFound("unknown callsign %q: check in first", callsign)
	}
	return s, nil
}

// touch refreshes last-seen for an acting session. Caller holds mu.
func (h *Hub) touch(callsign string) (*api.Session, error) {
	s, err := h.session(callsign)
	if err != nil {
		return nil, err
	}
	s.LastSeen = h.now()
	return s, nil
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "" {
		out = "agent"
	}
	return out
}

// Checkin registers a session and mints its callsign.
func (h *Hub) Checkin(req api.CheckinRequest) (api.CheckinResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	base := sanitizeName(req.Name)
	if base == api.Human {
		return api.CheckinResponse{}, badRequest("the name %q is reserved for the operator", api.Human)
	}
	callsign, reclaimed := base, false
	for n := 2; ; n++ {
		existing, ok := h.sessions[callsign]
		if !ok {
			break
		}
		if h.status(existing) == api.StatusGone {
			reclaimed = true
			break
		}
		callsign = base + "-" + strconv.Itoa(n)
	}
	now := h.now()
	s := api.Session{
		Callsign:        callsign,
		Name:            req.Name,
		Role:            req.Role,
		Platform:        req.Platform,
		Cwd:             req.Cwd,
		Scopes:          req.Scopes,
		Intro:           req.Intro,
		PlatformSession: req.PlatformSession,
		CheckedInAt:     now,
		LastSeen:        now,
	}
	if s.Scopes == nil {
		s.Scopes = []string{}
	}
	if _, err := h.commit(api.Event{Type: api.EvCheckin, Session: &s}); err != nil {
		return api.CheckinResponse{}, err
	}
	return api.CheckinResponse{
		Session:   h.view(&s),
		Reclaimed: reclaimed,
		Roster:    h.roster(""),
		Pending:   len(h.inbox[callsign]),
	}, nil
}

// Checkout removes a session and its undelivered inbox.
func (h *Hub) Checkout(callsign string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if callsign == api.Human {
		return badRequest("the operator cannot check out")
	}
	if _, err := h.session(callsign); err != nil {
		return err
	}
	_, err := h.commit(api.Event{Type: api.EvCheckout, Callsign: callsign})
	return err
}

// Resolve finds a session by platform session id, falling back to a unique
// working-directory match.
func (h *Hub) Resolve(req api.ResolveRequest) (api.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if req.PlatformSession != "" {
		for _, s := range h.sessions {
			if s.PlatformSession == req.PlatformSession {
				return h.view(s), nil
			}
		}
	}
	if req.Cwd != "" {
		var hits []*api.Session
		for _, s := range h.sessions {
			if s.Cwd == req.Cwd && s.PlatformSession == "" && h.status(s) != api.StatusGone {
				hits = append(hits, s)
			}
		}
		if len(hits) == 1 {
			return h.view(hits[0]), nil
		}
	}
	return api.Session{}, notFound("no checked-in session matches")
}

// Sessions lists the roster, optionally restricted to one scope.
func (h *Hub) Sessions(scope string) []api.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.roster(scope)
}

func (h *Hub) roster(scope string) []api.Session {
	out := make([]api.Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		if scope != "" && !s.InScope(scope) {
			continue
		}
		out = append(out, h.view(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Callsign < out[j].Callsign })
	return out
}

// recipients expands a target. Caller holds mu.
func (h *Hub) recipients(from, to string) (rcpts []string, scope string, err error) {
	sender := h.sessions[from]
	switch {
	case to == api.Broadcast:
		for cs := range h.sessions {
			if cs != from {
				rcpts = append(rcpts, cs)
			}
		}
	case strings.HasPrefix(to, api.ScopePrefix):
		scope = strings.TrimPrefix(to, api.ScopePrefix)
		if scope == "" {
			return nil, "", badRequest("empty scope in target %q", to)
		}
		for cs, s := range h.sessions {
			if cs != from && s.InScope(scope) {
				rcpts = append(rcpts, cs)
			}
		}
	default:
		if to == "" {
			return nil, "", badRequest("missing target")
		}
		if _, ok := h.sessions[to]; !ok {
			return nil, "", notFound("unknown recipient %q", to)
		}
		if to == from {
			return nil, "", badRequest("cannot send to yourself")
		}
		rcpts = []string{to}
		if sender != nil {
			scope = sender.PrimaryScope()
		}
		if scope == "" {
			scope = h.sessions[to].PrimaryScope()
		}
	}
	sort.Strings(rcpts)
	return rcpts, scope, nil
}

// Send routes a message and queues it for each recipient.
func (h *Hub) Send(req api.SendRequest) (api.SendResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.touch(req.As); err != nil {
		return api.SendResponse{}, err
	}
	if strings.TrimSpace(req.Body) == "" {
		return api.SendResponse{}, badRequest("empty message body")
	}
	kind := req.Kind
	if kind == "" {
		kind = api.KindChat
	}
	m, rcpts, err := h.post(req.As, req.To, kind, req.Body, req.Ref)
	if err != nil {
		return api.SendResponse{}, err
	}
	return api.SendResponse{Message: m, Recipients: rcpts}, nil
}

// post creates and commits a message. Caller holds mu.
func (h *Hub) post(from, to, kind, body, ref string) (api.Message, []string, error) {
	rcpts, scope, err := h.recipients(from, to)
	if err != nil {
		return api.Message{}, nil, err
	}
	if rcpts == nil {
		rcpts = []string{}
	}
	m := api.Message{
		ID:    "m" + strconv.Itoa(h.nmsg+1),
		Time:  h.now(),
		From:  from,
		To:    to,
		Scope: scope,
		Kind:  kind,
		Body:  body,
		Ref:   ref,
	}
	ev, err := h.commit(api.Event{Type: api.EvMessage, Message: &m, Recipients: rcpts})
	if err != nil {
		return api.Message{}, nil, err
	}
	m.Seq = ev.Seq
	return m, rcpts, nil
}

// Inbox returns the undelivered messages for a session, waiting up to wait
// for at least one to arrive. Unless peek is set the returned messages are
// marked delivered.
func (h *Hub) Inbox(ctx context.Context, as string, wait time.Duration, peek bool) ([]api.Message, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		if _, err := h.touch(as); err != nil {
			h.mu.Unlock()
			return nil, err
		}
		q := h.inbox[as]
		ch := h.changed
		if len(q) > 0 || wait <= 0 {
			out := append([]api.Message(nil), q...)
			if len(out) > 0 && !peek {
				ids := make([]string, len(out))
				for i, m := range out {
					ids[i] = m.ID
				}
				if _, err := h.commit(api.Event{Type: api.EvDelivered, Callsign: as, MessageIDs: ids}); err != nil {
					h.mu.Unlock()
					return nil, err
				}
			}
			h.mu.Unlock()
			return out, nil
		}
		h.mu.Unlock()
		select {
		case <-ch:
		case <-h.closing:
			return nil, restarting()
		case <-ctx.Done():
			return []api.Message{}, nil
		case <-deadline.C:
			return []api.Message{}, nil
		}
	}
}

// MarkDelivered removes messages from a session's undelivered queue. The
// server calls it once a response carrying them has reached the client.
func (h *Hub) MarkDelivered(as string, ids []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.session(as); err != nil {
		return err
	}
	pending := map[string]bool{}
	for _, m := range h.inbox[as] {
		pending[m.ID] = true
	}
	var confirm []string
	for _, id := range ids {
		if pending[id] {
			confirm = append(confirm, id)
		}
	}
	if len(confirm) == 0 {
		return nil
	}
	_, err := h.commit(api.Event{Type: api.EvDelivered, Callsign: as, MessageIDs: confirm})
	return err
}

// History returns every message a session sent or was addressed, oldest first.
func (h *Hub) History(as string) ([]api.Message, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.touch(as); err != nil {
		return nil, err
	}
	var out []api.Message
	for _, ev := range h.events {
		if ev.Type != api.EvMessage {
			continue
		}
		if ev.Message.From == as {
			out = append(out, *ev.Message)
			continue
		}
		for _, r := range ev.Recipients {
			if r == as {
				out = append(out, *ev.Message)
				break
			}
		}
	}
	if out == nil {
		out = []api.Message{}
	}
	return out, nil
}

// PostJob creates a job and notifies the assignee or the scope.
func (h *Hub) PostJob(req api.PostJobRequest) (api.Job, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	poster, err := h.touch(req.As)
	if err != nil {
		return api.Job{}, err
	}
	if strings.TrimSpace(req.Title) == "" {
		return api.Job{}, badRequest("job needs a title")
	}
	scope := req.Scope
	if scope == "" {
		scope = poster.PrimaryScope()
	}
	if scope == "" && req.To == "" {
		return api.Job{}, badRequest("no scope: pass --scope or --to")
	}
	if req.To != "" {
		if _, err := h.session(req.To); err != nil {
			return api.Job{}, err
		}
		if scope == "" {
			scope = h.sessions[req.To].PrimaryScope()
		}
	}
	j := api.Job{
		ID:       "j" + strconv.Itoa(h.njob+1),
		Scope:    scope,
		Title:    req.Title,
		Body:     req.Body,
		PostedBy: req.As,
		Assignee: req.To,
		Status:   api.JobOpen,
		PostedAt: h.now(),
	}
	if _, err := h.commit(api.Event{Type: api.EvJobPost, Job: &j, Callsign: req.As}); err != nil {
		return api.Job{}, err
	}
	target := api.ScopePrefix + scope
	if j.Assignee != "" {
		target = j.Assignee
	}
	body := fmt.Sprintf("New job %s from %s: %s", j.ID, j.PostedBy, j.Title)
	if j.Body != "" {
		body += "\n\n" + j.Body
	}
	body += fmt.Sprintf("\n\nTo take it: callboard claim --as <your callsign> %s", j.ID)
	if _, _, err := h.post(req.As, target, api.KindJob, body, j.ID); err != nil {
		return api.Job{}, err
	}
	return j, nil
}

// Jobs lists jobs, newest last, optionally filtered by scope and status.
func (h *Hub) Jobs(scope, status string) []api.Job {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []api.Job{}
	for _, id := range h.jobOrder {
		j := h.jobs[id]
		if scope != "" && j.Scope != scope {
			continue
		}
		if status != "" && j.Status != status {
			continue
		}
		out = append(out, *j)
	}
	return out
}

// Job returns one job.
func (h *Hub) Job(id string) (api.Job, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j, ok := h.jobs[id]
	if !ok {
		return api.Job{}, notFound("unknown job %q", id)
	}
	return *j, nil
}

// ClaimJob atomically assigns an open job to the caller.
func (h *Hub) ClaimJob(id, as string) (api.Job, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.touch(as); err != nil {
		return api.Job{}, err
	}
	j, ok := h.jobs[id]
	if !ok {
		return api.Job{}, notFound("unknown job %q", id)
	}
	if j.Status != api.JobOpen {
		return api.Job{}, conflict("job %s is %s (by %s)", id, j.Status, j.ClaimedBy)
	}
	if j.Assignee != "" && j.Assignee != as {
		return api.Job{}, conflict("job %s is assigned to %s", id, j.Assignee)
	}
	nj := *j
	nj.Status = api.JobClaimed
	nj.ClaimedBy = as
	nj.ClaimedAt = h.now()
	if _, err := h.commit(api.Event{Type: api.EvJobClaim, Job: &nj, Callsign: as}); err != nil {
		return api.Job{}, err
	}
	if nj.PostedBy != as {
		body := fmt.Sprintf("%s claimed job %s (%s)", as, nj.ID, nj.Title)
		if _, _, err := h.post(as, nj.PostedBy, api.KindSystem, body, nj.ID); err != nil && !isNotFound(err) {
			return api.Job{}, err
		}
	}
	return nj, nil
}

// CloseJob marks a claimed job done or failed. The claimer or the poster may
// do this.
func (h *Hub) CloseJob(id, as, status, result string) (api.Job, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.touch(as); err != nil {
		return api.Job{}, err
	}
	j, ok := h.jobs[id]
	if !ok {
		return api.Job{}, notFound("unknown job %q", id)
	}
	if j.Status == api.JobDone || j.Status == api.JobFailed {
		return api.Job{}, conflict("job %s is already %s", id, j.Status)
	}
	if j.Status == api.JobOpen && status == api.JobDone {
		return api.Job{}, conflict("job %s has not been claimed", id)
	}
	if as != j.ClaimedBy && as != j.PostedBy {
		if j.ClaimedBy == "" {
			return api.Job{}, conflict("job %s was posted by %s; only they can close it", id, j.PostedBy)
		}
		return api.Job{}, conflict("job %s is claimed by %s", id, j.ClaimedBy)
	}
	nj := *j
	nj.Status = status
	nj.Result = result
	nj.ClosedAt = h.now()
	evType := api.EvJobDone
	if status == api.JobFailed {
		evType = api.EvJobFail
	}
	if _, err := h.commit(api.Event{Type: evType, Job: &nj, Callsign: as}); err != nil {
		return api.Job{}, err
	}
	verb := "finished"
	if status == api.JobFailed {
		verb = "failed"
	}
	body := fmt.Sprintf("%s %s job %s (%s)", as, verb, nj.ID, nj.Title)
	if result != "" {
		body += ":\n" + result
	}
	for _, to := range []string{nj.PostedBy, nj.ClaimedBy} {
		if to == "" || to == as {
			continue
		}
		if _, _, err := h.post(as, to, api.KindSystem, body, nj.ID); err != nil && !isNotFound(err) {
			return api.Job{}, err
		}
	}
	return nj, nil
}

// Events returns events after since, waiting up to wait for new ones. With a
// scope only events concerning that scope (or hub-wide ones) are returned.
func (h *Hub) Events(ctx context.Context, since uint64, wait time.Duration, scope string) (api.EventsResponse, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		ch := h.changed
		var out []api.Event
		for _, ev := range h.events {
			if ev.Seq <= since {
				continue
			}
			if scope != "" {
				if es := ev.ScopeName(); es != "" && es != scope {
					continue
				}
			}
			out = append(out, ev)
		}
		next := h.seq
		h.mu.Unlock()
		if len(out) > 0 || wait <= 0 {
			if out == nil {
				out = []api.Event{}
			}
			return api.EventsResponse{Events: out, Next: next}, nil
		}
		select {
		case <-ch:
		case <-h.closing:
			return api.EventsResponse{}, restarting()
		case <-ctx.Done():
			return api.EventsResponse{Events: []api.Event{}, Next: next}, nil
		case <-deadline.C:
			return api.EventsResponse{Events: []api.Event{}, Next: next}, nil
		}
	}
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == 404
}
