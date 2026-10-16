package hub

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/config"
)

type fakeRunner struct {
	mu       sync.Mutex
	n        int
	launched []LaunchSpec
	windows  map[string]bool
	panes    map[string]string
	keys     map[string][]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{windows: map[string]bool{}, panes: map[string]string{}, keys: map[string][]string{}}
}

func (r *fakeRunner) Launch(spec LaunchSpec) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	w := fmt.Sprintf("@%d", r.n)
	r.windows[w] = true
	r.launched = append(r.launched, spec)
	return w, nil
}

func (r *fakeRunner) Windows() (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for w, ok := range r.windows {
		out[w] = ok
	}
	return out, nil
}

func (r *fakeRunner) Kill(w string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.windows, w)
	return nil
}

func (r *fakeRunner) Capture(w string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.panes[w], nil
}

func (r *fakeRunner) SendKeys(w string, keys ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[w] = append(r.keys[w], keys...)
	return nil
}

func (r *fakeRunner) alive(w string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.windows[w]
}

// probes maps a spawn id to what its transcript says.
type probes struct {
	mu sync.Mutex
	m  map[string]Probe
}

func (p *probes) set(id string, pr Probe) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[id] = pr
}

func (p *probes) fn(sp api.Spawn, _ config.Account) Probe {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pr, ok := p.m[sp.ID]; ok {
		return pr
	}
	return Probe{Context: -1}
}

func f(b bool) *bool { return &b }

func spawnHub(t *testing.T, journal string) (*Hub, *fakeRunner, *probes, *clock) {
	t.Helper()
	clk := newClock()
	r := newFakeRunner()
	p := &probes{m: map[string]Probe{}}
	h := newHub(t, Options{
		JournalPath: journal,
		Now:         clk.now,
		Runner:      r,
		Probe:       p.fn,
		Spawn: config.Spawn{
			Accounts: []config.Account{
				{Name: "personal", Platform: api.PlatformClaude},
				{Name: "fsg", Platform: api.PlatformClaude, ConfigDir: "/x/.claude-fsg"},
				{Name: "codex", Platform: api.PlatformCodex},
				{Name: "gk", Platform: api.PlatformClaude, Pool: f(false)},
			},
		},
	})
	return h, r, p, clk
}

// workerCheckin checks a spawned session in the way its prompt says to.
func workerCheckin(t *testing.T, h *Hub, sp api.Spawn) string {
	t.Helper()
	res, err := h.Checkin(api.CheckinRequest{Name: sp.ID, Role: "worker", Scopes: []string{"proj"}, Spawn: sp.ID, PlatformSession: "ps-" + sp.ID})
	if err != nil {
		t.Fatal(err)
	}
	return res.Session.Callsign
}

func spawnOf(t *testing.T, h *Hub, id string) api.Spawn {
	t.Helper()
	for _, sp := range h.Spawns(true).Spawns {
		if sp.ID == id {
			return sp
		}
	}
	t.Fatalf("no spawn %s", id)
	return api.Spawn{}
}

func TestSpawnSpreadsAcrossPool(t *testing.T) {
	h, r, _, _ := spawnHub(t, "")
	var accts []string
	for range 6 {
		sp, err := h.Spawn(api.SpawnRequest{As: "human", Cwd: "/repo"})
		if err != nil {
			t.Fatal(err)
		}
		accts = append(accts, sp.Account)
	}
	if got := strings.Join(accts, ","); got != "personal,fsg,codex,personal,fsg,codex" {
		t.Fatalf("accounts = %s", got)
	}
	// Claude spawns get a preset session id and the account's config dir.
	if r.launched[1].SessionID == "" || r.launched[1].Account.ConfigDir != "/x/.claude-fsg" || r.launched[1].Mode != "bypass" {
		t.Fatalf("launch spec = %+v", r.launched[1])
	}
	if r.launched[2].SessionID != "" {
		t.Fatal("codex spawn must not preset a session id")
	}
	// gk is outside the pool but can be named.
	sp, err := h.Spawn(api.SpawnRequest{Account: "gk", Cwd: "/repo"})
	if err != nil || sp.Account != "gk" {
		t.Fatalf("explicit gk: %v %v", sp, err)
	}
}

func TestSpawnCaps(t *testing.T) {
	h, _, _, _ := spawnHub(t, "")
	h.spawnCfg.MaxTotal = 2
	h.spawnCfg.Accounts[2].Max = 1
	if _, err := h.Spawn(api.SpawnRequest{Platform: "codex", Cwd: "/r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Spawn(api.SpawnRequest{Platform: "codex", Cwd: "/r"}); err == nil || !strings.Contains(err.Error(), "no codex account") {
		t.Fatalf("account cap not enforced: %v", err)
	}
	if _, err := h.Spawn(api.SpawnRequest{Cwd: "/r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Spawn(api.SpawnRequest{Cwd: "/r"}); err == nil || !strings.Contains(err.Error(), "slots") {
		t.Fatalf("global cap not enforced: %v", err)
	}
	// Hand-started sessions count toward the global cap too.
	h.spawnCfg.MaxTotal = 4
	checkin(t, h, "solo", "proj")
	if _, err := h.Spawn(api.SpawnRequest{Cwd: "/r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Spawn(api.SpawnRequest{Cwd: "/r"}); err == nil || !strings.Contains(err.Error(), "slots") {
		t.Fatalf("hand-started session not counted: %v", err)
	}
}

func TestSpawnForJobReservesAndRetiresOnFullContext(t *testing.T) {
	h, r, p, clk := spawnHub(t, "")
	coord := checkin(t, h, "boss", "proj")
	job, _ := h.PostJob(api.PostJobRequest{As: coord, Title: "fix it", Body: "details", Platform: "codex"})
	sp, err := h.Spawn(api.SpawnRequest{As: coord, Job: job.ID, Cwd: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if sp.Account != "codex" {
		t.Fatalf("job platform hint ignored: %s", sp.Account)
	}
	if !strings.Contains(r.launched[0].Prompt, "--spawn "+sp.ID) || !strings.Contains(r.launched[0].Prompt, "details") {
		t.Fatalf("prompt = %q", r.launched[0].Prompt)
	}
	if j, _ := h.Job(job.ID); j.Assignee != sp.ID {
		t.Fatalf("job not reserved: %+v", j)
	}
	if _, err := h.ClaimJob(job.ID, coord); err == nil {
		t.Fatal("reserved job claimed by someone else")
	}
	cs := workerCheckin(t, h, sp)
	if got := spawnOf(t, h, sp.ID); got.Status != api.SpawnRunning || got.Callsign != cs || got.PlatformSession != "ps-"+sp.ID {
		t.Fatalf("spawn not linked: %+v", got)
	}
	if _, err := h.ClaimJob(job.ID, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CloseJob(job.ID, cs, api.JobDone, "ok"); err != nil {
		t.Fatal(err)
	}
	p.set(sp.ID, Probe{Context: 0.4})
	h.SuperviseOnce()
	if !r.alive(sp.Window) {
		t.Fatal("retired before the grace period")
	}
	clk.advance(2 * time.Minute)
	h.SuperviseOnce()
	if r.alive(sp.Window) {
		t.Fatal("not retired after its job")
	}
	got := spawnOf(t, h, sp.ID)
	if got.Status != api.SpawnEnded || !strings.Contains(got.Reason, "40%") {
		t.Fatalf("spawn = %+v", got)
	}
	if len(h.Sessions("proj")) != 1 {
		t.Fatal("retired session still checked in")
	}
}

func TestKeptWorkerGetsNextJobThenIdles(t *testing.T) {
	h, r, p, clk := spawnHub(t, "")
	coord := checkin(t, h, "boss", "proj")
	h.sessions[coord].Role = "coordinator"
	j1, _ := h.PostJob(api.PostJobRequest{As: coord, Title: "small"})
	sp, _ := h.Spawn(api.SpawnRequest{As: coord, Job: j1.ID, Cwd: "/repo"})
	cs := workerCheckin(t, h, sp)
	h.ClaimJob(j1.ID, cs)
	h.CloseJob(j1.ID, cs, api.JobDone, "")
	p.set(sp.ID, Probe{Context: 0.05})
	clk.advance(2 * time.Minute)
	h.SuperviseOnce()
	if !r.alive(sp.Window) {
		t.Fatal("low-context worker was retired")
	}
	// The next open job goes to the kept worker instead of a new spawn.
	j2, _ := h.PostJob(api.PostJobRequest{As: coord, Title: "next"})
	clk.advance(time.Minute)
	h.sessions[coord].LastSeen = clk.now()
	h.SuperviseOnce()
	if j, _ := h.Job(j2.ID); j.Assignee != cs {
		t.Fatalf("job not handed to kept worker: %+v", j)
	}
	if len(r.launched) != 1 {
		t.Fatalf("spawned %d sessions, want 1", len(r.launched))
	}
	h.ClaimJob(j2.ID, cs)
	h.CloseJob(j2.ID, cs, api.JobDone, "")
	clk.advance(2 * time.Minute)
	h.SuperviseOnce() // still small: kept again
	clk.advance(31 * time.Minute)
	h.SuperviseOnce()
	if r.alive(sp.Window) {
		t.Fatal("idle kept worker not retired")
	}
}

func TestAutoscaleNeedsCoordinator(t *testing.T) {
	h, r, _, clk := spawnHub(t, "")
	w := checkin(t, h, "someone", "proj")
	h.PostJob(api.PostJobRequest{As: w, Title: "t"})
	clk.advance(time.Minute)
	h.sessions[w].LastSeen = clk.now()
	h.SuperviseOnce()
	if len(r.launched) != 0 {
		t.Fatal("autoscaled without a coordinator")
	}
	res, _ := h.Checkin(api.CheckinRequest{Name: "boss", Role: "coordinator", Scopes: []string{"proj"}, Cwd: "/repo"})
	// A coordinator blocked in wait for longer than the idle window still counts.
	clk.advance(20 * time.Minute)
	h.waiting[res.Session.Callsign] = 1
	h.SuperviseOnce()
	if len(r.launched) != 1 || r.launched[0].Cwd != "/repo" {
		t.Fatalf("launched = %+v", r.launched)
	}
	// A second pass does not spawn again for the reserved job.
	h.SuperviseOnce()
	if len(r.launched) != 1 {
		t.Fatal("job spawned twice")
	}
}

func TestLimitFailsOverToAnotherAccount(t *testing.T) {
	h, r, p, clk := spawnHub(t, "")
	coord := checkin(t, h, "boss", "proj")
	job, _ := h.PostJob(api.PostJobRequest{As: coord, Title: "t"})
	sp, _ := h.Spawn(api.SpawnRequest{As: coord, Job: job.ID, Cwd: "/repo"})
	cs := workerCheckin(t, h, sp)
	h.ClaimJob(job.ID, cs)
	p.set(sp.ID, Probe{Limit: "rate_limit"})
	h.SuperviseOnce()
	if r.alive(sp.Window) {
		t.Fatal("limited session not stopped")
	}
	res := h.Spawns(false)
	if len(res.Spawns) != 1 || res.Spawns[0].Account == sp.Account || res.Spawns[0].Job != job.ID {
		t.Fatalf("no failover spawn: %+v", res.Spawns)
	}
	if !strings.Contains(r.launched[1].Prompt, "partial progress") {
		t.Fatal("failover prompt lacks the handover note")
	}
	for _, a := range res.Accounts {
		if a.Name == sp.Account && (a.Limited.IsZero() || !a.Limited.Equal(clk.now().Add(time.Hour))) {
			t.Fatalf("account not limited: %+v", a)
		}
	}
	// The limited account is skipped until the cooldown ends.
	if _, err := h.Spawn(api.SpawnRequest{Account: sp.Account, Cwd: "/r"}); err == nil {
		t.Fatal("spawned on a limited account")
	}
	clk.advance(61 * time.Minute)
	if _, err := h.Spawn(api.SpawnRequest{Account: sp.Account, Cwd: "/r"}); err != nil {
		t.Fatal(err)
	}
}

func TestStartupDialogsAndPaneLimit(t *testing.T) {
	h, r, _, clk := spawnHub(t, "")
	sp, _ := h.Spawn(api.SpawnRequest{Cwd: "/r"})
	r.panes[sp.Window] = " ❯ No, exit\n   Yes, I trust this folder\n"
	h.SuperviseOnce()
	if got := strings.Join(r.keys[sp.Window], " "); got != "Down Enter" {
		t.Fatalf("keys = %q", got)
	}
	r.panes[sp.Window] = "You've hit your limit · resets 3pm"
	h.SuperviseOnce()
	if r.alive(sp.Window) || spawnOf(t, h, sp.ID).Status != api.SpawnEnded {
		t.Fatal("pane limit not acted on")
	}
	sp2, _ := h.Spawn(api.SpawnRequest{Cwd: "/r"})
	clk.advance(6 * time.Minute)
	h.SuperviseOnce()
	if got := spawnOf(t, h, sp2.ID); got.Reason != "did not check in" {
		t.Fatalf("start timeout: %+v", got)
	}
}

func TestExitedSpawnReopensJob(t *testing.T) {
	h, r, _, _ := spawnHub(t, "")
	coord := checkin(t, h, "boss", "proj")
	job, _ := h.PostJob(api.PostJobRequest{As: coord, Title: "t"})
	sp, _ := h.Spawn(api.SpawnRequest{As: coord, Job: job.ID, Cwd: "/repo"})
	cs := workerCheckin(t, h, sp)
	h.ClaimJob(job.ID, cs)
	r.Kill(sp.Window)
	h.SuperviseOnce()
	j, _ := h.Job(job.ID)
	if j.Status != api.JobOpen || j.Assignee != "" || j.ClaimedBy != "" {
		t.Fatalf("job not reopened: %+v", j)
	}
	msgs, _ := h.Inbox(t.Context(), coord, 0, true)
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Body, "open again") {
		t.Fatalf("poster not told: %+v", msgs)
	}
}

func TestSpawnsSurviveReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	h, _, _, _ := spawnHub(t, path)
	sp, _ := h.Spawn(api.SpawnRequest{Cwd: "/r"})
	h.mu.Lock()
	l := api.Limit{Account: "fsg", Until: h.now().Add(time.Hour), Reason: "x"}
	h.commit(api.Event{Type: api.EvAccountLimit, Limit: &l})
	h.mu.Unlock()
	h.Close()
	h2, _, _, _ := spawnHub(t, path)
	if got := spawnOf(t, h2, sp.ID); got.Window == "" || got.Status != api.SpawnStarting {
		t.Fatalf("replayed spawn = %+v", got)
	}
	sp2, _ := h2.Spawn(api.SpawnRequest{Cwd: "/r"})
	if sp2.ID != "sp2" || sp2.Account != "codex" {
		t.Fatalf("after replay: %+v", sp2)
	}
}

func TestProbeTranscripts(t *testing.T) {
	claude := `{"type":"assistant","message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":1000,"cache_read_input_tokens":19000}}}
{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"content":[{"type":"text","text":"You've hit your limit"}]}}
`
	p := probeClaude([]byte(claude), 200000)
	if p.Limit != "You've hit your limit" || p.Context < 0.1 || p.Context > 0.11 {
		t.Fatalf("claude probe = %+v", p)
	}
	// A later successful turn clears an earlier error.
	p = probeClaude([]byte(claude+`{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`+"\n"), 200000)
	if p.Limit != "" {
		t.Fatalf("stale limit: %+v", p)
	}
	codex := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":47500},"model_context_window":475000},"rate_limits":{"primary":{"used_percent":100,"resets_at":1792700406}}}}
{"type":"event_msg","payload":{"type":"task_complete","error":{"message":"You've hit your usage limit.","codex_error_info":"usage_limit_exceeded"}}}
`
	p = probeCodex([]byte(codex), 0)
	if p.Limit == "" || p.Context != 0.1 || p.Until.Unix() != 1792700406 {
		t.Fatalf("codex probe = %+v", p)
	}
}

func TestSpawnEnvAndCommand(t *testing.T) {
	spec := LaunchSpec{ID: "sp1", Account: config.Account{Platform: api.PlatformClaude, ConfigDir: "/c"}, Mode: "bypass", SessionID: "u", Prompt: "go"}
	env := strings.Join(spawnEnv([]string{"PATH=/bin", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=x", "CODEX_THREAD_ID=y", "CALLBOARD_AS=a", "CALLBOARD_SOCKET=/s", "TMUX=t"}, spec), " ")
	if env != "PATH=/bin CALLBOARD_SOCKET=/s CALLBOARD_SPAWN=sp1 CLAUDE_CONFIG_DIR=/c" {
		t.Fatalf("env = %s", env)
	}
}

func TestCodexCommandCarriesEnv(t *testing.T) {
	spec := LaunchSpec{ID: "sp2", Account: config.Account{Platform: api.PlatformCodex}, Mode: "bypass", Prompt: "go"}
	argv, err := agentCommand(spec, []string{"PATH=/bin", "HOME=/h", "CALLBOARD_SPAWN=sp2"})
	if err != nil {
		t.Skip("codex not installed")
	}
	got := strings.Join(argv[1:], " ")
	want := `--dangerously-bypass-approvals-and-sandbox -c shell_environment_policy.set.PATH="/bin" -c shell_environment_policy.set.CALLBOARD_SPAWN="sp2" go`
	if got != want {
		t.Fatalf("argv = %s", got)
	}
}
