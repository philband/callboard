package hub

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/config"
)

// Spawning: the hub starts agent sessions in tmux windows on configured
// accounts, one per job, and stops them when their job is finished and their
// context is used up. The supervisor loop answers start-up dialogs, notices
// exits and account limits, and (while a coordinator is present in a scope)
// spawns workers for jobs nobody has taken.

// autoscaleBy is Spawn.By for sessions the hub started on its own.
const autoscaleBy = "autoscale"

// hubSender is the From of messages the hub writes itself.
const hubSender = "callboard"

// Runner starts and controls spawned sessions. The tmux implementation is
// in tmux.go; tests use a fake.
type Runner interface {
	Launch(spec LaunchSpec) (window string, err error)
	// Windows lists the live window ids.
	Windows() (map[string]bool, error)
	Kill(window string) error
	// Capture returns the visible text of a window.
	Capture(window string) (string, error)
	SendKeys(window string, keys ...string) error
}

// LaunchSpec is everything a Runner needs to start one session.
type LaunchSpec struct {
	ID        string
	Account   config.Account
	Cwd       string
	Prompt    string
	Model     string
	Mode      string // permission mode
	SessionID string // preset platform session id (Claude only)
}

// Probe is what the transcript of a spawned session says about it.
type Probe struct {
	// Context is the fraction of the context window in use; negative when
	// unknown.
	Context float64
	// Limit is non-empty when the account hit a usage or login limit.
	Limit string
	// Until is when the limit resets, if the platform said so.
	Until time.Time
}

// ProbeFunc reads a spawned session's transcript.
type ProbeFunc func(sp api.Spawn, acct config.Account) Probe

// spawnEnabled reports whether any account is configured. Caller holds mu.
func (h *Hub) spawnEnabled() bool { return h.runner != nil && len(h.spawnCfg.Accounts) > 0 }

func (h *Hub) account(name string) (config.Account, bool) {
	for _, a := range h.spawnCfg.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return config.Account{}, false
}

// live counts spawns that have not ended, per account and in total. Caller
// holds mu.
func (h *Hub) live() (map[string]int, int) {
	per, total := map[string]int{}, 0
	for _, sp := range h.spawns {
		if sp.Status != api.SpawnEnded {
			per[sp.Account]++
			total++
		}
	}
	return per, total
}

// limited reports whether an account is resting after a limit. Caller holds mu.
func (h *Hub) limited(name string) (api.Limit, bool) {
	l, ok := h.limits[name]
	return l, ok && h.now().Before(l.Until)
}

// pickAccount chooses the account for a spawn. Caller holds mu.
func (h *Hub) pickAccount(name, platform string) (config.Account, error) {
	per, total := h.live()
	if max := h.spawnCfg.MaxTotal; max > 0 && total >= max {
		return config.Account{}, conflict("all %d spawn slots are in use", max)
	}
	if name != "" {
		a, ok := h.account(name)
		if !ok {
			return a, notFound("unknown account %q", name)
		}
		if l, ok := h.limited(name); ok {
			return a, conflict("account %s is limited until %s: %s", name, l.Until.Local().Format("15:04"), l.Reason)
		}
		if a.Max > 0 && per[name] >= a.Max {
			return a, conflict("account %s is at its cap of %d", name, a.Max)
		}
		return a, nil
	}
	choose := func(platform string) (config.Account, bool) {
		var best config.Account
		bestFree, found := 0, false
		for _, a := range h.spawnCfg.Accounts {
			if !a.InPool() || (platform != "" && a.Platform != platform) {
				continue
			}
			if _, ok := h.limited(a.Name); ok {
				continue
			}
			free := 1 << 20
			if a.Max > 0 {
				free = a.Max
			}
			free -= per[a.Name]
			if free <= 0 {
				continue
			}
			if !found || free > bestFree {
				best, bestFree, found = a, free, true
			}
		}
		return best, found
	}
	if a, ok := choose(platform); ok {
		return a, nil
	}
	// A platform the poster asked for gives way only when every account on
	// it is limited, not when it is merely busy.
	if platform != "" && h.allLimited(platform) {
		if a, ok := choose(""); ok {
			return a, nil
		}
	}
	if platform != "" {
		return config.Account{}, conflict("no %s account has a free slot", platform)
	}
	return config.Account{}, conflict("no account has a free slot")
}

func (h *Hub) allLimited(platform string) bool {
	for _, a := range h.spawnCfg.Accounts {
		if a.InPool() && a.Platform == platform {
			if _, ok := h.limited(a.Name); !ok {
				return false
			}
		}
	}
	return true
}

func normPlatform(p string) string {
	if p == "claude" {
		return api.PlatformClaude
	}
	return p
}

// Spawn starts a session on an account.
func (h *Hub) Spawn(req api.SpawnRequest) (api.Spawn, error) {
	h.mu.Lock()
	sp, spec, err := h.prepareSpawn(req, "")
	h.mu.Unlock()
	if err != nil {
		return api.Spawn{}, err
	}
	return h.launch(sp, spec)
}

// prepareSpawn picks the account, records the spawn and reserves its job.
// note is prepended to the prompt. Caller holds mu.
func (h *Hub) prepareSpawn(req api.SpawnRequest, note string) (api.Spawn, LaunchSpec, error) {
	if !h.spawnEnabled() {
		return api.Spawn{}, LaunchSpec{}, badRequest("spawning is not configured: add spawn.accounts to %s", config.ConfigPath())
	}
	platform := normPlatform(req.Platform)
	if platform != "" && platform != api.PlatformClaude && platform != api.PlatformCodex {
		return api.Spawn{}, LaunchSpec{}, badRequest("platform must be claude or codex")
	}
	var job *api.Job
	scope := req.Scope
	if req.Job != "" {
		j, ok := h.jobs[req.Job]
		if !ok {
			return api.Spawn{}, LaunchSpec{}, notFound("unknown job %q", req.Job)
		}
		if j.Status != api.JobOpen || j.Assignee != "" {
			return api.Spawn{}, LaunchSpec{}, conflict("job %s is not free (%s, assignee %q)", j.ID, j.Status, j.Assignee)
		}
		job = j
		scope = j.Scope
		if platform == "" {
			platform = normPlatform(j.Platform)
		}
	}
	if req.Cwd == "" {
		return api.Spawn{}, LaunchSpec{}, badRequest("spawn needs a working directory")
	}
	acct, err := h.pickAccount(req.Account, platform)
	if err != nil {
		return api.Spawn{}, LaunchSpec{}, err
	}
	sp := api.Spawn{
		ID:        "sp" + strconv.Itoa(h.nspawn+1),
		Account:   acct.Name,
		Platform:  acct.Platform,
		Scope:     scope,
		Cwd:       req.Cwd,
		By:        req.As,
		Status:    api.SpawnStarting,
		StartedAt: h.now(),
	}
	if job != nil {
		sp.Job = job.ID
	}
	if sp.Platform == api.PlatformClaude {
		sp.PlatformSession = newUUID()
	}
	if _, err := h.commit(api.Event{Type: api.EvSpawn, Spawn: &sp, Callsign: req.As}); err != nil {
		return api.Spawn{}, LaunchSpec{}, err
	}
	if job != nil {
		nj := *job
		nj.Assignee = sp.ID
		if _, err := h.commit(api.Event{Type: api.EvJobAssign, Job: &nj, Callsign: hubSender}); err != nil {
			return api.Spawn{}, LaunchSpec{}, err
		}
	}
	mode := acct.Permission
	if mode == "" {
		mode = h.spawnCfg.Permission
	}
	if mode == "" {
		mode = "bypass"
	}
	model := req.Model
	if model == "" {
		model = acct.Model
	}
	spec := LaunchSpec{
		ID:        sp.ID,
		Account:   acct,
		Cwd:       sp.Cwd,
		Prompt:    spawnPrompt(sp, job, note, req.Prompt),
		Model:     model,
		Mode:      mode,
		SessionID: sp.PlatformSession,
	}
	return sp, spec, nil
}

// launch starts the window for a prepared spawn, outside the lock.
func (h *Hub) launch(sp api.Spawn, spec LaunchSpec) (api.Spawn, error) {
	window, err := h.runner.Launch(spec)
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.spawns[sp.ID]
	if !ok || cur.Status == api.SpawnEnded {
		return sp, conflict("spawn %s ended while starting", sp.ID)
	}
	if err != nil {
		h.endSpawn(sp.ID, "launch failed: "+err.Error())
		return api.Spawn{}, &Error{500, "launch failed: " + err.Error()}
	}
	ns := *cur
	ns.Window = window
	if _, err := h.commit(api.Event{Type: api.EvSpawn, Spawn: &ns}); err != nil {
		return api.Spawn{}, err
	}
	return ns, nil
}

func spawnPrompt(sp api.Spawn, job *api.Job, note, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a callboard worker, started by callboard as spawn %s on the %s account.\n", sp.ID, sp.Account)
	fmt.Fprintf(&b, "First check in: callboard checkin --name %s --role worker --spawn %s\n\n", sp.ID, sp.ID)
	if note != "" {
		b.WriteString(note + "\n\n")
	}
	if job != nil {
		fmt.Fprintf(&b, "Your job is %s: %s\n", job.ID, job.Title)
		if job.Body != "" {
			b.WriteString("\n" + job.Body + "\n")
		}
		fmt.Fprintf(&b, "\nClaim it with `callboard claim --as <your callsign> %s`, do it following the callboard protocol in CLAUDE.md / AGENTS.md, and report with `callboard done` or `callboard fail`.\n", job.ID)
	} else {
		b.WriteString("Look at the open jobs with `callboard jobs --status open`, claim one, and work following the callboard protocol in CLAUDE.md / AGENTS.md.\n")
	}
	if extra != "" {
		b.WriteString("\n" + extra + "\n")
	}
	b.WriteString("\nAfter reporting, wait for messages as the protocol says. Callboard stops this session when it is no longer needed; do not check out on your own.")
	return b.String()
}

func newUUID() string {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])
}

// endSpawn marks a spawn ended, checks its session out and reopens any job
// it held. It does not touch the window. Caller holds mu.
func (h *Hub) endSpawn(id, reason string) {
	sp, ok := h.spawns[id]
	if !ok || sp.Status == api.SpawnEnded {
		return
	}
	ns := *sp
	ns.Status = api.SpawnEnded
	ns.Reason = reason
	ns.EndedAt = h.now()
	if _, err := h.commit(api.Event{Type: api.EvSpawn, Spawn: &ns}); err != nil {
		fmt.Fprintf(os.Stderr, "spawn %s: %v\n", id, err)
		return
	}
	for _, j := range h.heldJobs(ns) {
		nj := *j
		nj.Status = api.JobOpen
		nj.Assignee = ""
		nj.ClaimedBy = ""
		nj.ClaimedAt = time.Time{}
		if _, err := h.commit(api.Event{Type: api.EvJobAssign, Job: &nj, Callsign: hubSender}); err != nil {
			continue
		}
		body := fmt.Sprintf("job %s (%s) is open again: %s ended (%s)", nj.ID, nj.Title, id, reason)
		if _, _, err := h.post(hubSender, nj.PostedBy, api.KindSystem, body, nj.ID); err != nil && !isNotFound(err) {
			fmt.Fprintf(os.Stderr, "spawn %s: %v\n", id, err)
		}
	}
	if ns.Callsign != "" {
		if s, ok := h.sessions[ns.Callsign]; ok && s.Spawn == id {
			_, _ = h.commit(api.Event{Type: api.EvCheckout, Callsign: ns.Callsign})
		}
	}
}

// heldJobs are the unfinished jobs a spawn holds: reserved for it or claimed
// by its session. Caller holds mu.
func (h *Hub) heldJobs(sp api.Spawn) []*api.Job {
	var out []*api.Job
	for _, id := range h.jobOrder {
		j := h.jobs[id]
		switch {
		case j.Status == api.JobOpen && (j.Assignee == sp.ID || (sp.Callsign != "" && j.Assignee == sp.Callsign)):
		case j.Status == api.JobClaimed && sp.Callsign != "" && j.ClaimedBy == sp.Callsign:
		default:
			continue
		}
		out = append(out, j)
	}
	return out
}

// linkSpawn attaches a checking-in session to its spawn. Caller holds mu.
func (h *Hub) linkSpawn(s *api.Session) error {
	sp, ok := h.spawns[s.Spawn]
	if !ok || sp.Status == api.SpawnEnded {
		s.Spawn = ""
		return nil
	}
	ns := *sp
	ns.Callsign = s.Callsign
	ns.Status = api.SpawnRunning
	if s.PlatformSession != "" {
		ns.PlatformSession = s.PlatformSession
	}
	if _, err := h.commit(api.Event{Type: api.EvSpawn, Spawn: &ns}); err != nil {
		return err
	}
	// A job reserved under the spawn id moves to the callsign, which may
	// differ (sp3-2) when the name was taken.
	for _, j := range h.jobs {
		if j.Status == api.JobOpen && j.Assignee == sp.ID && sp.ID != s.Callsign {
			nj := *j
			nj.Assignee = s.Callsign
			if _, err := h.commit(api.Event{Type: api.EvJobAssign, Job: &nj, Callsign: hubSender}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Spawns lists spawns (ended ones too when all is set) and account load.
func (h *Hub) Spawns(all bool) api.SpawnsResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	res := api.SpawnsResponse{Spawns: []api.Spawn{}, Accounts: []api.Account{}, MaxTotal: h.spawnCfg.MaxTotal, Enabled: h.spawnEnabled()}
	for _, id := range h.spawnOrder {
		if sp := h.spawns[id]; all || sp.Status != api.SpawnEnded {
			res.Spawns = append(res.Spawns, *sp)
		}
	}
	per, _ := h.live()
	for _, a := range h.spawnCfg.Accounts {
		acc := api.Account{Name: a.Name, Platform: a.Platform, Pool: a.InPool(), Max: a.Max, Running: per[a.Name]}
		if l, ok := h.limited(a.Name); ok {
			acc.Limited, acc.Reason = l.Until, l.Reason
		}
		res.Accounts = append(res.Accounts, acc)
	}
	return res
}

// Despawn stops a spawned session and reopens its unfinished jobs.
func (h *Hub) Despawn(id, as, reason string) error {
	h.mu.Lock()
	sp, ok := h.spawns[id]
	if !ok {
		h.mu.Unlock()
		return notFound("unknown spawn %q", id)
	}
	if sp.Status == api.SpawnEnded {
		h.mu.Unlock()
		return conflict("spawn %s has already ended", id)
	}
	window := sp.Window
	if reason == "" {
		reason = "stopped"
		if as != "" {
			reason += " by " + as
		}
	}
	h.endSpawn(id, reason)
	h.mu.Unlock()
	if window != "" {
		return h.runner.Kill(window)
	}
	return nil
}

// Supervise runs the spawn supervisor until ctx ends.
func (h *Hub) Supervise(ctx context.Context, every time.Duration) {
	if !h.spawnEnabled() {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		h.SuperviseOnce()
		select {
		case <-ctx.Done():
			return
		case <-h.closing:
			return
		case <-t.C:
		}
	}
}

// SuperviseOnce runs one pass of the supervisor.
func (h *Hub) SuperviseOnce() {
	windows, werr := h.runner.Windows()
	h.mu.Lock()
	var live []api.Spawn
	for _, id := range h.spawnOrder {
		if sp := h.spawns[id]; sp.Status != api.SpawnEnded {
			live = append(live, *sp)
		}
	}
	h.mu.Unlock()
	for _, sp := range live {
		h.superviseSpawn(sp, windows, werr == nil)
	}
	h.autoscale()
}

func (h *Hub) superviseSpawn(sp api.Spawn, windows map[string]bool, haveWindows bool) {
	acct, _ := h.account(sp.Account)
	if sp.Window == "" {
		return // still launching
	}
	if haveWindows && !windows[sp.Window] {
		h.mu.Lock()
		h.endSpawn(sp.ID, "exited")
		h.mu.Unlock()
		return
	}
	now := h.now()
	if sp.Status == api.SpawnStarting {
		pane, err := h.runner.Capture(sp.Window)
		if err == nil {
			if reason := paneLimit(pane); reason != "" {
				h.hitLimit(sp, acct, Probe{Limit: reason})
				return
			}
			if keys := dialogKeys(pane); keys != nil && h.answered[sp.ID] < 10 {
				h.answered[sp.ID]++
				_ = h.runner.SendKeys(sp.Window, keys...)
			}
		}
		if now.Sub(sp.StartedAt) > h.spawnCfg.StartTimeout.Or(5*time.Minute) {
			h.stop(sp, "did not check in")
		}
		return
	}
	probe := h.probe(sp, acct)
	if probe.Limit != "" {
		h.hitLimit(sp, acct, probe)
		return
	}
	h.mu.Lock()
	if _, ok := h.sessions[sp.Callsign]; !ok {
		h.mu.Unlock()
		h.stop(sp, "checked out")
		return
	}
	held := len(h.heldJobs(sp)) > 0
	var last *api.Job
	for _, j := range h.jobs {
		if j.ClaimedBy == sp.Callsign && (j.Status == api.JobDone || j.Status == api.JobFailed) {
			if last == nil || j.ClosedAt.After(last.ClosedAt) {
				last = j
			}
		}
	}
	h.mu.Unlock()
	if held || last == nil {
		return
	}
	idle := now.Sub(last.ClosedAt)
	if idle < h.spawnCfg.RetireGrace.Or(time.Minute) {
		return
	}
	if h.kept[sp.ID] != last.ID {
		keep := h.spawnCfg.KeepContextBelow
		if keep == 0 {
			keep = 0.10
		}
		if probe.Context < 0 || probe.Context >= keep {
			h.stop(sp, fmt.Sprintf("job %s finished, context %s", last.ID, pct(probe.Context)))
			return
		}
		h.kept[sp.ID] = last.ID
		return
	}
	if idle >= h.spawnCfg.IdleRetire.Or(30*time.Minute) {
		h.stop(sp, "idle since job "+last.ID)
	}
}

func pct(f float64) string {
	if f < 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

// stop ends a spawn and kills its window.
func (h *Hub) stop(sp api.Spawn, reason string) {
	h.mu.Lock()
	h.endSpawn(sp.ID, reason)
	h.mu.Unlock()
	if sp.Window != "" {
		_ = h.runner.Kill(sp.Window)
	}
}

// hitLimit rests the account, stops the spawn and moves its job to another
// account when one is free.
func (h *Hub) hitLimit(sp api.Spawn, acct config.Account, p Probe) {
	until := p.Until
	if until.IsZero() || until.Before(h.now()) {
		until = h.now().Add(h.spawnCfg.LimitCooldown.Or(time.Hour))
	}
	h.mu.Lock()
	jobs := h.heldJobs(sp)
	var jobIDs []string
	for _, j := range jobs {
		jobIDs = append(jobIDs, j.ID)
	}
	l := api.Limit{Account: acct.Name, Until: until, Reason: p.Limit}
	_, _ = h.commit(api.Event{Type: api.EvAccountLimit, Limit: &l})
	h.endSpawn(sp.ID, "account limit: "+p.Limit)
	type next struct {
		sp   api.Spawn
		spec LaunchSpec
	}
	var starts []next
	for _, id := range jobIDs {
		note := fmt.Sprintf("Spawn %s started on this job and was stopped when its account hit a limit. Check the working tree and the job's messages for partial progress before starting over.", sp.ID)
		nsp, spec, err := h.prepareSpawn(api.SpawnRequest{As: autoscaleBy, Job: id, Cwd: sp.Cwd, Platform: h.jobs[id].Platform}, note)
		if err != nil {
			continue
		}
		starts = append(starts, next{nsp, spec})
	}
	h.mu.Unlock()
	_ = h.runner.Kill(sp.Window)
	for _, s := range starts {
		_, _ = h.launch(s.sp, s.spec)
	}
}

// coordinatorPresent reports whether a coordinator is checked in to scope
// and alive: seen recently, or blocked in a wait. Caller holds mu.
func (h *Hub) coordinatorPresent(scope string) (*api.Session, bool) {
	for _, s := range h.sessions {
		if s.Role == "coordinator" && s.InScope(scope) && (h.status(s) != api.StatusGone || h.waiting[s.Callsign] > 0) {
			return s, true
		}
	}
	return nil, false
}

// autoscale gives each open, untaken job in a scope with a coordinator a
// worker: a kept one that is idle, or a new spawn.
func (h *Hub) autoscale() {
	h.mu.Lock()
	delay := h.spawnCfg.AutoscaleDelay.Or(30 * time.Second)
	type next struct {
		sp   api.Spawn
		spec LaunchSpec
	}
	var starts []next
	idle := h.idleKept()
	for _, id := range h.jobOrder {
		j := h.jobs[id]
		if j.Status != api.JobOpen || j.Assignee != "" || h.now().Sub(j.PostedAt) < delay {
			continue
		}
		coord, ok := h.coordinatorPresent(j.Scope)
		if !ok {
			continue
		}
		if sp, ok := takeIdle(&idle, normPlatform(j.Platform)); ok {
			nj := *j
			nj.Assignee = sp.Callsign
			if _, err := h.commit(api.Event{Type: api.EvJobAssign, Job: &nj, Callsign: hubSender}); err == nil {
				body := fmt.Sprintf("Job %s is yours: %s", nj.ID, nj.Title)
				if nj.Body != "" {
					body += "\n\n" + nj.Body
				}
				body += fmt.Sprintf("\n\nClaim it with `callboard claim --as %s %s`.", sp.Callsign, nj.ID)
				_, _, _ = h.post(hubSender, sp.Callsign, api.KindJob, body, nj.ID)
			}
			continue
		}
		sp, spec, err := h.prepareSpawn(api.SpawnRequest{As: autoscaleBy, Job: j.ID, Cwd: coord.Cwd}, "")
		if err != nil {
			continue
		}
		starts = append(starts, next{sp, spec})
	}
	h.mu.Unlock()
	for _, s := range starts {
		_, _ = h.launch(s.sp, s.spec)
	}
}

// idleKept lists running spawns that were kept after a job and hold none.
// Caller holds mu.
func (h *Hub) idleKept() []api.Spawn {
	var out []api.Spawn
	for _, id := range h.spawnOrder {
		sp := h.spawns[id]
		if sp.Status != api.SpawnRunning || h.kept[sp.ID] == "" || len(h.heldJobs(*sp)) > 0 {
			continue
		}
		out = append(out, *sp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func takeIdle(idle *[]api.Spawn, platform string) (api.Spawn, bool) {
	for i, sp := range *idle {
		if platform == "" || sp.Platform == platform {
			*idle = append((*idle)[:i], (*idle)[i+1:]...)
			return sp, true
		}
	}
	return api.Spawn{}, false
}

// Pane patterns. Only checked before the session checks in, when the
// screen holds the CLI's own start-up output and not the agent's work.
var paneLimits = []string{
	"hit your usage limit",
	"hit your limit",
	"usage limit reached",
	"Please run /login",
	"Not logged in",
	"Invalid API key",
}

func paneLimit(pane string) string {
	for _, p := range paneLimits {
		if strings.Contains(pane, p) {
			return p
		}
	}
	return ""
}

// dialogKeys answers the start-up dialogs that would otherwise hold an
// unattended session: folder trust (Claude defaults to "No, exit"), the
// bypass-permissions warning, and Codex notices such as a model change.
func dialogKeys(pane string) []string {
	switch {
	case strings.Contains(pane, "Yes, I trust this folder"):
		return []string{"Down", "Enter"}
	case strings.Contains(pane, "Yes, I accept"):
		return []string{"Down", "Enter"}
	case strings.Contains(pane, "Trust and continue"):
		return []string{"Enter"}
	case strings.Contains(pane, "enter/esc continue"), strings.Contains(pane, "enter continue"):
		return []string{"Enter"}
	}
	return nil
}
