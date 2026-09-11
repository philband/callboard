// Package build identifies the executable a callboard process is running
// from, so a client, a long-running daemon and the hub can tell whether they
// are the same build and upgrade themselves when they are not.
package build

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version is the release version. The linker sets cli.Version, which mirrors
// itself here at start, so every package sees the same string.
var Version = "dev"

// Info describes one executable. ModTime is the identity that matters: the
// binary is rebuilt in place, so a newer file is a newer build.
type Info struct {
	Version string    `json:"version"`
	ModTime time.Time `json:"mod_time,omitzero"`
	Size    int64     `json:"size,omitempty"`
}

// NewerThan reports whether a was built after b.
func (a Info) NewerThan(b Info) bool { return a.ModTime.After(b.ModTime) }

// String renders the build for a human: version and build time.
func (i Info) String() string {
	if i.ModTime.IsZero() {
		return i.Version
	}
	return i.Version + " " + i.ModTime.Local().Format("2006-01-02 15:04:05")
}

var (
	once   sync.Once
	cached Info
)

// This describes the running executable. The stat is taken once: the file
// may be rewritten under us, and every comparison must use the build we
// actually started from. A package var so tests can stub it.
var This = func() Info {
	once.Do(func() { cached = OnDisk() })
	return cached
}

// OnDisk re-reads the executable's file. Unlike This it is not cached, so it
// answers "has the binary been rebuilt since we started?".
var OnDisk = func() Info {
	i := Info{Version: Version}
	exe, err := os.Executable()
	if err != nil {
		return i
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	st, err := os.Stat(exe)
	if err != nil {
		return i
	}
	i.ModTime = st.ModTime()
	i.Size = st.Size()
	return i
}
