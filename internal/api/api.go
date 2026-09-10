// Package api holds the wire types shared by hub and client.
package api

import "time"

// Session statuses derived from last-seen.
const (
	StatusActive = "active" // seen within ActiveWindow
	StatusIdle   = "idle"   // seen within IdleWindow
	StatusGone   = "gone"   // not seen for longer than IdleWindow
	StatusHuman  = "human"  // the always-present human pseudo-session
)

const (
	ActiveWindow = 2 * time.Minute
	IdleWindow   = 15 * time.Minute
)

// Human is the callsign of the human operator. It needs no check-in.
const Human = "human"

// Broadcast is the message target that reaches every session.
const Broadcast = "*"

// ScopePrefix marks a message target that names a scope.
const ScopePrefix = "scope:"

// Message kinds.
const (
	KindChat   = "chat"
	KindJob    = "job"
	KindSystem = "system"
)

// Job statuses.
const (
	JobOpen    = "open"
	JobClaimed = "claimed"
	JobDone    = "done"
	JobFailed  = "failed"
)

// Event types.
const (
	EvCheckin   = "checkin"
	EvCheckout  = "checkout"
	EvMessage   = "message"
	EvDelivered = "delivered"
	EvJobPost   = "job.post"
	EvJobClaim  = "job.claim"
	EvJobDone   = "job.done"
	EvJobFail   = "job.fail"
)

type Session struct {
	Callsign string   `json:"callsign"`
	Name     string   `json:"name"`
	Role     string   `json:"role,omitempty"`
	Platform string   `json:"platform,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Scopes   []string `json:"scopes"`
	Intro    string   `json:"intro,omitempty"`
	// PlatformSession is the platform's own session id (e.g. Claude Code's
	// CLAUDE_CODE_SESSION_ID). Hooks use it to find their callsign.
	PlatformSession string    `json:"platform_session,omitempty"`
	CheckedInAt     time.Time `json:"checked_in_at"`
	LastSeen        time.Time `json:"last_seen"`
	Status          string    `json:"status,omitempty"` // computed, never stored
}

// PrimaryScope is the first scope, normally derived from the git remote.
func (s Session) PrimaryScope() string {
	if len(s.Scopes) == 0 {
		return ""
	}
	return s.Scopes[0]
}

// InScope reports whether the session belongs to scope.
func (s Session) InScope(scope string) bool {
	for _, x := range s.Scopes {
		if x == scope {
			return true
		}
	}
	return false
}

type Message struct {
	ID    string    `json:"id"`
	Seq   uint64    `json:"seq"`
	Time  time.Time `json:"time"`
	From  string    `json:"from"`
	To    string    `json:"to"` // callsign, "scope:<name>" or "*"
	Scope string    `json:"scope,omitempty"`
	Kind  string    `json:"kind"`
	Body  string    `json:"body"`
	Ref   string    `json:"ref,omitempty"` // job id this message is about
}

type Job struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	PostedBy  string    `json:"posted_by"`
	Assignee  string    `json:"assignee,omitempty"` // pre-assigned; only they may claim
	ClaimedBy string    `json:"claimed_by,omitempty"`
	Status    string    `json:"status"`
	Result    string    `json:"result,omitempty"`
	PostedAt  time.Time `json:"posted_at"`
	ClaimedAt time.Time `json:"claimed_at,omitempty"`
	ClosedAt  time.Time `json:"closed_at,omitempty"`
}

// Event is the journal and stream unit. Exactly one payload field is set
// depending on Type.
type Event struct {
	Seq        uint64    `json:"seq"`
	Time       time.Time `json:"time"`
	Type       string    `json:"type"`
	Session    *Session  `json:"session,omitempty"`     // checkin, checkout
	Message    *Message  `json:"message,omitempty"`     // message
	Recipients []string  `json:"recipients,omitempty"`  // message
	Job        *Job      `json:"job,omitempty"`         // job.*
	Callsign   string    `json:"callsign,omitempty"`    // delivered, checkout, job.* actor
	MessageIDs []string  `json:"message_ids,omitempty"` // delivered
}

// Scope returns the scope an event concerns, or "" for hub-wide events.
func (e Event) ScopeName() string {
	switch {
	case e.Message != nil:
		return e.Message.Scope
	case e.Job != nil:
		return e.Job.Scope
	case e.Session != nil:
		return e.Session.PrimaryScope()
	}
	return ""
}

// Requests and responses.

type CheckinRequest struct {
	Name            string   `json:"name"`
	Role            string   `json:"role,omitempty"`
	Platform        string   `json:"platform,omitempty"`
	Cwd             string   `json:"cwd,omitempty"`
	Scopes          []string `json:"scopes"`
	Intro           string   `json:"intro,omitempty"`
	PlatformSession string   `json:"platform_session,omitempty"`
}

// ResolveRequest finds the callsign of a session by platform session id, or
// by working directory when that is unambiguous.
type ResolveRequest struct {
	PlatformSession string `json:"platform_session,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
}

type ResolveResponse struct {
	Session Session `json:"session"`
}

type CheckinResponse struct {
	Session   Session   `json:"session"`
	Reclaimed bool      `json:"reclaimed"` // an existing gone session with this callsign was resumed
	Roster    []Session `json:"roster"`
	Pending   int       `json:"pending"` // undelivered messages waiting
}

type CheckoutRequest struct {
	As string `json:"as"`
}

type SessionsResponse struct {
	Sessions []Session `json:"sessions"`
}

type SendRequest struct {
	As   string `json:"as"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"`
	Body string `json:"body"`
	Ref  string `json:"ref,omitempty"`
}

type SendResponse struct {
	Message    Message  `json:"message"`
	Recipients []string `json:"recipients"`
}

type InboxResponse struct {
	Messages []Message `json:"messages"`
}

type PostJobRequest struct {
	As    string `json:"as"`
	Scope string `json:"scope,omitempty"` // defaults to the poster's primary scope
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	To    string `json:"to,omitempty"` // pre-assign to a callsign
}

type JobActionRequest struct {
	As     string `json:"as"`
	Result string `json:"result,omitempty"` // done: result, fail: reason
}

type JobResponse struct {
	Job Job `json:"job"`
}

type JobsResponse struct {
	Jobs []Job `json:"jobs"`
}

type EventsResponse struct {
	Events []Event `json:"events"`
	Next   uint64  `json:"next"` // pass as since= for the next call
}

type HealthResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Socket  string `json:"socket,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
