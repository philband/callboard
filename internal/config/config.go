// Package config resolves user configuration and on-disk locations.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the user configuration read from ConfigPath().
type Config struct {
	// HostAliases maps a git host as it appears in remote URLs to the
	// canonical host used in scope names, e.g. git.fs-g.org -> gitlab.fs-g.org.
	HostAliases map[string]string `json:"host_aliases"`
	// Spawn configures the accounts the hub may start agent sessions on.
	Spawn Spawn `json:"spawn"`
}

// Spawn configures spawning. With no accounts spawning is off.
type Spawn struct {
	Accounts []Account `json:"accounts"`
	// MaxTotal caps spawned sessions across all accounts; 0 means no cap.
	MaxTotal int `json:"max_total"`
	// Permission is the default permission mode for spawned sessions:
	// "bypass" (default), "auto", or any mode the platform's CLI accepts.
	Permission string `json:"permission"`
	// KeepContextBelow keeps a worker alive after its job when it has used
	// less than this fraction of its context window (default 0.10).
	KeepContextBelow float64 `json:"keep_context_below"`
	// RetireGrace is how long a finished worker may wrap up before it is
	// stopped (default 1m).
	RetireGrace Duration `json:"retire_grace"`
	// IdleRetire stops a kept worker that has had no job for this long
	// (default 30m).
	IdleRetire Duration `json:"idle_retire"`
	// AutoscaleDelay is how long an open job waits for a taker before the
	// hub spawns a worker for it (default 30s).
	AutoscaleDelay Duration `json:"autoscale_delay"`
	// LimitCooldown is how long an account rests after hitting a limit when
	// the platform does not say when it resets (default 1h).
	LimitCooldown Duration `json:"limit_cooldown"`
	// StartTimeout ends a spawn that has not checked in by then (default 5m).
	StartTimeout Duration `json:"start_timeout"`
}

// Account is one login on one platform.
type Account struct {
	Name string `json:"name"`
	// Platform is "claude" (or "claude-code") or "codex".
	Platform string `json:"platform"`
	// ConfigDir is CLAUDE_CONFIG_DIR or CODEX_HOME for this account; empty
	// uses the platform default (and unsets the variable).
	ConfigDir string `json:"config_dir"`
	// Max caps sessions on this account; 0 means no cap.
	Max int `json:"max"`
	// Pool lets the hub pick this account on its own (default true). An
	// account outside the pool is used only when named explicitly.
	Pool *bool `json:"pool"`
	// Permission overrides Spawn.Permission for this account.
	Permission string `json:"permission"`
	Model      string `json:"model"`
	// ContextWindow is the model's context size in tokens, for the keep
	// rule. Claude transcripts do not record it (default 200000); Codex
	// rollouts do and override this.
	ContextWindow int `json:"context_window"`
}

// InPool reports whether the hub may choose this account by itself.
func (a Account) InPool() bool { return a.Pool == nil || *a.Pool }

// Duration is a time.Duration written as a Go duration string in JSON.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Or returns d, or def when d is unset.
func (d Duration) Or(def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return time.Duration(d)
}

// Load reads the configuration file. A missing file yields an empty Config.
func Load() (Config, error) {
	var c Config
	data, err := os.ReadFile(ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, errors.New(ConfigPath() + ": " + err.Error())
	}
	for i := range c.Spawn.Accounts {
		a := &c.Spawn.Accounts[i]
		if a.Platform == "claude" {
			a.Platform = "claude-code"
		}
		if a.Platform != "claude-code" && a.Platform != "codex" {
			return c, fmt.Errorf("%s: account %q: platform must be claude or codex", ConfigPath(), a.Name)
		}
		if a.Name == "" {
			return c, fmt.Errorf("%s: spawn account without a name", ConfigPath())
		}
		if rest, ok := strings.CutPrefix(a.ConfigDir, "~/"); ok {
			a.ConfigDir = filepath.Join(home(), rest)
		}
	}
	return c, nil
}

// ConfigPath is $XDG_CONFIG_HOME/callboard/config.json or
// ~/.config/callboard/config.json.
func ConfigPath() string {
	if p := os.Getenv("CALLBOARD_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home(), ".config")
	}
	return filepath.Join(base, "callboard", "config.json")
}

// StateDir is $XDG_STATE_HOME/callboard or ~/.local/state/callboard. It holds
// the journal, the lock file, the hub log and (by default) the socket.
func StateDir() string {
	if p := os.Getenv("CALLBOARD_STATE_DIR"); p != "" {
		return p
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(home(), ".local", "state")
	}
	return filepath.Join(base, "callboard")
}

// SocketPath is CALLBOARD_SOCKET or StateDir()/hub.sock. The state dir is
// preferred over $XDG_RUNTIME_DIR to keep the path short; Unix socket paths
// are limited to ~104 bytes on macOS.
func SocketPath() string {
	if p := os.Getenv("CALLBOARD_SOCKET"); p != "" {
		return p
	}
	return filepath.Join(StateDir(), "hub.sock")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}
