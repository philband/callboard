// Package api holds the wire types shared by hub and client.
package api

import (
	"time"

	"github.com/philband/callboard/internal/build"
)

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
	// EvJobAssign carries a job snapshot whose assignee changed outside a
	// claim: reserved for a spawned worker, or released after its account
	// hit a limit.
	EvJobAssign = "job.assign"
	// EvSpawn carries a Spawn snapshot: started, linked to its session, ended.
	EvSpawn = "spawn"
	// EvAccountLimit records that an account hit a usage or login limit.
	EvAccountLimit = "account.limit"
)

// Spawn statuses.
const (
	SpawnStarting = "starting" // launched, not checked in yet
	SpawnRunning  = "running"  // checked in
	SpawnEnded    = "ended"
)

// Agent platforms that callboard can spawn.
const (
	PlatformClaude = "claude-code"
	PlatformCodex  = "codex"
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
	PlatformSession string `json:"platform_session,omitempty"`
	// Spawn is the id of the spawn that started this session, if any.
	Spawn       string    `json:"spawn,omitempty"`
	CheckedInAt time.Time `json:"checked_in_at"`
	LastSeen    time.Time `json:"last_seen"`
	Status      string    `json:"status,omitempty"` // computed, never stored
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
	Platform  string    `json:"platform,omitempty"` // platform a spawned worker should run on
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
	Spawn      *Spawn    `json:"spawn,omitempty"`       // spawn
	Limit      *Limit    `json:"limit,omitempty"`       // account.limit
}

// Spawn is an agent session callboard started in a tmux window on one of the
// configured accounts.
type Spawn struct {
	ID       string `json:"id"` // also the name the session checks in with
	Account  string `json:"account"`
	Platform string `json:"platform"`
	Scope    string `json:"scope,omitempty"`
	Cwd      string `json:"cwd"`
	Job      string `json:"job,omitempty"` // job it was started for
	By       string `json:"by,omitempty"`  // who asked; "autoscale" for the hub
	// Window is the tmux window id, e.g. "@3".
	Window string `json:"window,omitempty"`
	// Callsign and PlatformSession are filled in when the session checks in.
	// Claude sessions get their session id at launch.
	Callsign        string    `json:"callsign,omitempty"`
	PlatformSession string    `json:"platform_session,omitempty"`
	Status          string    `json:"status"`
	Reason          string    `json:"reason,omitempty"` // why it ended
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at,omitzero"`
}

// Limit marks an account unusable until Until.
type Limit struct {
	Account string    `json:"account"`
	Until   time.Time `json:"until"`
	Reason  string    `json:"reason"`
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
	Spawn           string   `json:"spawn,omitempty"`
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

// DeliveredRequest acknowledges messages a notifier has pushed into the
// agent's session by another channel than the inbox response.
type DeliveredRequest struct {
	As  string   `json:"as"`
	IDs []string `json:"ids"`
}

type PostJobRequest struct {
	As    string `json:"as"`
	Scope string `json:"scope,omitempty"` // defaults to the poster's primary scope
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	To    string `json:"to,omitempty"` // pre-assign to a callsign
	// Platform asks for spawned workers on this platform.
	Platform string `json:"platform,omitempty"`
}

// SpawnRequest starts an agent session. Without Account the hub picks the
// pooled account with the most free capacity, of Platform if given (or of
// the job's platform).
type SpawnRequest struct {
	As       string `json:"as,omitempty"`
	Account  string `json:"account,omitempty"`
	Platform string `json:"platform,omitempty"`
	Job      string `json:"job,omitempty"`
	Cwd      string `json:"cwd"`
	Scope    string `json:"scope,omitempty"`
	Model    string `json:"model,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

type SpawnResponse struct {
	Spawn Spawn `json:"spawn"`
}

// Account is a configured account with its current load.
type Account struct {
	Name     string    `json:"name"`
	Platform string    `json:"platform"`
	Pool     bool      `json:"pool"`
	Max      int       `json:"max"` // 0: no limit
	Running  int       `json:"running"`
	Limited  time.Time `json:"limited_until,omitzero"`
	Reason   string    `json:"limit_reason,omitempty"`
}

type SpawnsResponse struct {
	Spawns   []Spawn   `json:"spawns"`
	Accounts []Account `json:"accounts"`
	MaxTotal int       `json:"max_total"` // 0: no limit
	Enabled  bool      `json:"enabled"`
}

type DespawnRequest struct {
	As     string `json:"as,omitempty"`
	Reason string `json:"reason,omitempty"`
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
	OK      bool       `json:"ok"`
	Version string     `json:"version"`
	PID     int        `json:"pid"`
	Socket  string     `json:"socket,omitempty"`
	Build   build.Info `json:"build"`
}

// ShutdownRequest asks the hub to stop gracefully. A non-zero IfModTime must
// match the hub's own build, so a restart meant for a stale hub cannot take
// down the newer one that replaced it in the meantime.
type ShutdownRequest struct {
	IfModTime time.Time `json:"if_mod_time,omitzero"`
}

// MsgHubRestarting is the error a long poll gets while the hub drains for a
// restart. Clients reconnect on it instead of failing.
const MsgHubRestarting = "hub restarting"

type ErrorResponse struct {
	Error string `json:"error"`
}

// SpawnSummary renders job.assign, spawn and account.limit events as one
// phrase for `tail` and the dashboard; "" for other events.
func (e Event) SpawnSummary() string {
	switch {
	case e.Type == EvJobAssign && e.Job != nil:
		switch {
		case e.Job.Assignee != "":
			return e.Job.ID + " reserved for " + e.Job.Assignee
		default:
			return e.Job.ID + " open again"
		}
	case e.Type == EvSpawn && e.Spawn != nil:
		sp := e.Spawn
		switch {
		case sp.Status == SpawnEnded:
			return sp.ID + " ended: " + sp.Reason
		case sp.Status == SpawnRunning:
			return sp.ID + " checked in as " + sp.Callsign
		case sp.Window != "":
			return sp.ID + " started on " + sp.Account
		}
		out := sp.ID + " starting on " + sp.Account
		if sp.Job != "" {
			out += " for " + sp.Job
		}
		return out
	case e.Type == EvAccountLimit && e.Limit != nil:
		return e.Limit.Account + " limited until " + e.Limit.Until.Local().Format("15:04") + ": " + e.Limit.Reason
	}
	return ""
}
