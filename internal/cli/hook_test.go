package cli

import (
	"encoding/json"
	"testing"
)

func TestHookInputBothSpellings(t *testing.T) {
	var claude, copilot hookInput
	if err := json.Unmarshal([]byte(`{"session_id":"c1","cwd":"/a","stop_hook_active":true}`), &claude); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"sessionId":"g1","cwd":"/b","timestamp":1}`), &copilot); err != nil {
		t.Fatal(err)
	}
	if claude.id() != "c1" || !claude.StopHookActive || claude.Cwd != "/a" {
		t.Errorf("claude input = %+v", claude)
	}
	if copilot.id() != "g1" || copilot.Cwd != "/b" {
		t.Errorf("copilot input = %+v", copilot)
	}
}
