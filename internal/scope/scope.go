// Package scope derives the canonical project scope for a working directory.
//
// A scope is the normalised git remote reference of the repository containing
// the directory, e.g. "github.com/philband/callboard". Hosts can be mapped
// through user-configured aliases so that an SSH host such as git.fs-g.org
// resolves to the canonical gitlab.fs-g.org.
package scope

import (
	"bytes"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
)

// Normalize turns a git remote URL into a canonical scope name.
//
// Supported forms: scp-like (git@host:owner/repo.git), ssh://, https://,
// http://, git://, file:// and plain paths. Scheme, user info, port and a
// trailing ".git" are dropped, the result is lower-cased, and the host is
// mapped through aliases. Local paths become "local:<path>".
func Normalize(remote string, aliases map[string]string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	var host, path string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		if u.Scheme == "file" {
			return local(u.Path)
		}
		host, path = u.Hostname(), u.Path
	case strings.HasPrefix(remote, "/") || strings.HasPrefix(remote, ".") || strings.HasPrefix(remote, "~"):
		return local(remote)
	default:
		// scp-like: [user@]host:path
		i := strings.Index(remote, ":")
		if i < 0 {
			return local(remote)
		}
		host, path = remote[:i], remote[i+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	}
	host = strings.ToLower(host)
	if a, ok := aliases[host]; ok {
		host = strings.ToLower(a)
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + strings.ToLower(path)
}

func local(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return "local:" + p
}

// Detect returns the scope for dir. It prefers the "origin" remote, then the
// first remote in git's order, then "local:<git common dir>", then
// "local:<dir>" outside of a repository.
func Detect(dir string, aliases map[string]string) string {
	if remote := gitOutput(dir, "config", "--get", "remote.origin.url"); remote != "" {
		if s := Normalize(remote, aliases); s != "" {
			return s
		}
	}
	if names := gitOutput(dir, "remote"); names != "" {
		first := strings.Fields(names)[0]
		if remote := gitOutput(dir, "config", "--get", "remote."+first+".url"); remote != "" {
			if s := Normalize(remote, aliases); s != "" {
				return s
			}
		}
	}
	if common := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); common != "" {
		return local(common)
	}
	return local(dir)
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}
