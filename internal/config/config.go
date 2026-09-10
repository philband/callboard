// Package config resolves user configuration and on-disk locations.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is the user configuration read from ConfigPath().
type Config struct {
	// HostAliases maps a git host as it appears in remote URLs to the
	// canonical host used in scope names, e.g. git.fs-g.org -> gitlab.fs-g.org.
	HostAliases map[string]string `json:"host_aliases"`
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
