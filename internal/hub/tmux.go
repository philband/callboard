package hub

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/philband/callboard/internal/api"
)

// Tmux runs spawned sessions as windows of one session on a private tmux
// server (`tmux -L callboard`), so they never mix with the user's own.
// Attach with `tmux -L callboard attach`.
type Tmux struct {
	Socket  string // -L name
	Session string
}

// NewTmux returns the runner the hub uses.
func NewTmux() *Tmux { return &Tmux{Socket: "callboard", Session: "callboard"} }

func (t *Tmux) cmd(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", t.Socket}, args...)...)
}

func (t *Tmux) run(args ...string) (string, error) {
	out, err := t.cmd(args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (t *Tmux) ensure() error {
	if t.cmd("has-session", "-t", t.Session).Run() == nil {
		return nil
	}
	_, err := t.run("new-session", "-d", "-s", t.Session, "-n", "board", "-x", "220", "-y", "50")
	return err
}

// Launch opens a window running the agent CLI under a clean environment.
func (t *Tmux) Launch(spec LaunchSpec) (string, error) {
	if err := t.ensure(); err != nil {
		return "", err
	}
	env := spawnEnv(os.Environ(), spec)
	argv, err := agentCommand(spec, env)
	if err != nil {
		return "", err
	}
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", t.Session + ":", "-n", spec.ID, "-c", spec.Cwd, "--", "env", "-i"}
	args = append(args, env...)
	args = append(args, argv...)
	return t.run(args...)
}

func (t *Tmux) Windows() (map[string]bool, error) {
	if t.cmd("has-session", "-t", t.Session).Run() != nil {
		return map[string]bool{}, nil
	}
	out, err := t.run("list-windows", "-t", t.Session, "-F", "#{window_id}")
	if err != nil {
		return nil, err
	}
	ws := map[string]bool{}
	for _, w := range strings.Fields(out) {
		ws[w] = true
	}
	return ws, nil
}

func (t *Tmux) Kill(window string) error {
	_, err := t.run("kill-window", "-t", window)
	return err
}

func (t *Tmux) Capture(window string) (string, error) {
	return t.run("capture-pane", "-p", "-t", window)
}

func (t *Tmux) SendKeys(window string, keys ...string) error {
	_, err := t.run(append([]string{"send-keys", "-t", window}, keys...)...)
	return err
}

// agentCommand builds the CLI invocation for a spawn.
func agentCommand(spec LaunchSpec, env []string) ([]string, error) {
	switch spec.Account.Platform {
	case api.PlatformClaude:
		bin, err := exec.LookPath("claude")
		if err != nil {
			return nil, err
		}
		argv := []string{bin}
		if spec.Mode == "bypass" {
			argv = append(argv, "--dangerously-skip-permissions")
		} else {
			argv = append(argv, "--permission-mode", spec.Mode)
		}
		if spec.Model != "" {
			argv = append(argv, "--model", spec.Model)
		}
		if spec.SessionID != "" {
			argv = append(argv, "--session-id", spec.SessionID)
		}
		return append(argv, "-n", spec.ID, spec.Prompt), nil
	case api.PlatformCodex:
		bin, err := exec.LookPath("codex")
		if err != nil {
			return nil, err
		}
		argv := []string{bin}
		switch spec.Mode {
		case "bypass":
			argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
		case "auto":
			// Unattended: no approvals, workspace writes, and network so
			// the agent can reach the callboard socket.
			argv = append(argv, "-s", "workspace-write", "-a", "never", "-c", "sandbox_workspace_write.network_access=true")
		default:
			argv = append(argv, "-a", spec.Mode)
		}
		if spec.Model != "" {
			argv = append(argv, "-m", spec.Model)
		}
		// Interactive Codex runs the agent's commands through its shared
		// app-server daemon, whose environment is not ours: hand PATH and
		// the callboard variables over through Codex's own config.
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			if k == "PATH" || strings.HasPrefix(k, "CALLBOARD_") {
				argv = append(argv, "-c", "shell_environment_policy.set."+k+"="+strconv.Quote(v))
			}
		}
		return append(argv, spec.Prompt), nil
	}
	return nil, fmt.Errorf("cannot spawn platform %q", spec.Account.Platform)
}

// spawnEnv is the hub's environment without anything that ties a process to
// the agent session the hub happened to be started from, plus the account's
// config directory and the spawn id for check-in.
func spawnEnv(environ []string, spec LaunchSpec) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "CLAUDE"), strings.HasPrefix(k, "CODEX_"), strings.HasPrefix(k, "COPILOT_"),
			k == "CALLBOARD_AS", k == "CALLBOARD_SPAWN", k == "TMUX", k == "TMUX_PANE":
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "CALLBOARD_SPAWN="+spec.ID)
	if spec.Account.ConfigDir != "" {
		switch spec.Account.Platform {
		case api.PlatformClaude:
			out = append(out, "CLAUDE_CONFIG_DIR="+spec.Account.ConfigDir)
		case api.PlatformCodex:
			out = append(out, "CODEX_HOME="+spec.Account.ConfigDir)
		}
	}
	return out
}
