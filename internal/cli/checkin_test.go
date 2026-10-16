package cli

import "testing"

func TestCodexOneShot(t *testing.T) {
	for _, tt := range []struct {
		name, ci, args string
		want           bool
	}{
		{"interactive", "", "/bin/codex", false},
		{"exec", "1", "/bin/codex exec task", true},
		{"exec alias", "1", "/bin/codex e task", true},
		{"unknown", "1", "", true},
		{"app server", "1", "/bin/codex app-server --listen unix:// --managed-daemon", false},
		{"exec mentioning app server", "1", "/bin/codex exec app-server", true},
		{"interactive tui", "1", "/opt/homebrew/bin/codex --dangerously-bypass-approvals-and-sandbox -c x=1 You are a worker; exec the review", false},
		{"exec after flags", "1", "/bin/codex -c x=1 exec task", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexOneShot(tt.ci, tt.args); got != tt.want {
				t.Fatalf("codexOneShot(%q, %q) = %v, want %v", tt.ci, tt.args, got, tt.want)
			}
		})
	}
}
