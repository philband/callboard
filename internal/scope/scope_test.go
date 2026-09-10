package scope

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNormalize(t *testing.T) {
	aliases := map[string]string{"git.fs-g.org": "gitlab.fs-g.org"}
	cases := map[string]string{
		"git@github.com:philband/callboard.git":                    "github.com/philband/callboard",
		"https://github.com/Philband/Callboard":                    "github.com/philband/callboard",
		"https://user:token@github.com/philband/callboard.git/":    "github.com/philband/callboard",
		"ssh://git@git.fs-g.org:2222/philipp.bandow/callboard.git": "gitlab.fs-g.org/philipp.bandow/callboard",
		"git@git.fs-g.org:philipp.bandow/callboard.git":            "gitlab.fs-g.org/philipp.bandow/callboard",
		"https://gitlab.fs-g.org/philipp.bandow/callboard":         "gitlab.fs-g.org/philipp.bandow/callboard",
		"git://example.org/a/b/c.git":                              "example.org/a/b/c",
		"host.example:repo.git":                                    "host.example/repo",
		"/srv/git/thing.git":                                       "local:/srv/git/thing.git",
		"file:///srv/git/thing":                                    "local:/srv/git/thing",
		"":                                                         "",
		"nonsense":                                                 "local:" + mustAbs("nonsense"),
	}
	for in, want := range cases {
		if got := Normalize(in, aliases); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return a
}

func TestDetect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if got := Detect(dir, nil); got != "local:"+filepath.Join(mustReal(t, dir), ".git") {
		t.Errorf("no-remote scope = %q", got)
	}
	run("remote", "add", "upstream", "git@github.com:someone/else.git")
	if got := Detect(dir, nil); got != "github.com/someone/else" {
		t.Errorf("first-remote scope = %q", got)
	}
	run("remote", "add", "origin", "git@git.fs-g.org:philipp.bandow/callboard.git")
	if got := Detect(dir, map[string]string{"git.fs-g.org": "gitlab.fs-g.org"}); got != "gitlab.fs-g.org/philipp.bandow/callboard" {
		t.Errorf("origin scope = %q", got)
	}
	sub := filepath.Join(dir, "a", "b")
	if err := exec.Command("mkdir", "-p", sub).Run(); err != nil {
		t.Fatal(err)
	}
	if got := Detect(sub, map[string]string{"git.fs-g.org": "gitlab.fs-g.org"}); got != "gitlab.fs-g.org/philipp.bandow/callboard" {
		t.Errorf("subdir scope = %q", got)
	}
	outside := t.TempDir()
	if got := Detect(outside, nil); got != "local:"+mustAbs(outside) {
		t.Errorf("outside-git scope = %q", got)
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
