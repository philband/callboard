package cli

import (
	"os"
	"testing"
	"time"
)

// withStdin replaces os.Stdin with a temporary file holding content.
func withStdin(t *testing.T, content string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old; f.Close() })
}

func TestReadBodyArgJoinsArgs(t *testing.T) {
	withStdin(t, "from stdin")
	got, err := readBodyArg([]string{"hello", "there"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello there" {
		t.Fatalf("got %q, want %q", got, "hello there")
	}
}

func TestReadBodyArgReadsStdin(t *testing.T) {
	withStdin(t, "line one\nline two\n")
	got, err := readBodyArg(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "line one\nline two" {
		t.Fatalf("got %q", got)
	}
}

func TestDashStdin(t *testing.T) {
	withStdin(t, "piped\n")
	if got, err := dashStdin("plain"); err != nil || got != "plain" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := dashStdin("-"); err != nil || got != "piped" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPluralAndShortDur(t *testing.T) {
	if got := plural(1, "message"); got != "1 message" {
		t.Errorf("got %q", got)
	}
	if got := plural(2, "recipient"); got != "2 recipients" {
		t.Errorf("got %q", got)
	}
	if got := shortDur(9 * time.Minute); got != "9m" {
		t.Errorf("got %q", got)
	}
	if got := shortDur(30 * time.Second); got != "30s" {
		t.Errorf("got %q", got)
	}
	if got := shortDur(time.Hour); got != "1h" {
		t.Errorf("got %q", got)
	}
}

func TestTruncateAndFirstLine(t *testing.T) {
	if got := firstLine("  one\ntwo  "); got != "one" {
		t.Errorf("got %q", got)
	}
	if got := truncate("abcdef", 6); got != "abcdef" {
		t.Errorf("got %q", got)
	}
	if got := truncate("abcdefgh", 6); got != "abc..." {
		t.Errorf("got %q", got)
	}
}
