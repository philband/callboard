package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return buf.String()
}

func TestInitBlock_CreatesFileWithBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	status, err := initUpsertFile(path, initProtocolBlock)
	if err != nil {
		t.Fatalf("initUpsertFile: %v", err)
	}
	if status != "created" {
		t.Fatalf("status = %q, want created", status)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, initBlockStart) || !strings.Contains(content, initBlockEnd) {
		t.Fatalf("file missing markers:\n%s", content)
	}
}

func TestInitBlock_IdempotentOnSecondRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	if _, err := initUpsertFile(path, initProtocolBlock); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	status, err := initUpsertFile(path, initProtocolBlock)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if status != "unchanged" {
		t.Fatalf("status = %q, want unchanged", status)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("content changed on idempotent run:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestInitBlock_PreservesSurroundingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	original := "# Project notes\n\nSome existing instructions.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("seeding file: %v", err)
	}

	status, err := initUpsertFile(path, initProtocolBlock)
	if err != nil {
		t.Fatalf("initUpsertFile: %v", err)
	}
	if status != "updated" {
		t.Fatalf("status = %q, want updated", status)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, original) {
		t.Fatalf("original content not preserved before block:\n%s", content)
	}
	if !strings.Contains(content, initBlockStart) {
		t.Fatalf("block missing:\n%s", content)
	}

	// Add trailing content after the block; a further run with identical
	// block content must leave both the prefix and the trailer untouched.
	trailer := "\n## After\n\nMore text.\n"
	if err := os.WriteFile(path, []byte(content+trailer), 0644); err != nil {
		t.Fatalf("appending trailer: %v", err)
	}

	status, err = initUpsertFile(path, initProtocolBlock)
	if err != nil {
		t.Fatalf("initUpsertFile (with trailer): %v", err)
	}
	if status != "unchanged" {
		t.Fatalf("status = %q, want unchanged (block content identical)", status)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content = string(data)
	if !strings.HasPrefix(content, original) {
		t.Fatalf("original content lost:\n%s", content)
	}
	if !strings.HasSuffix(content, trailer) {
		t.Fatalf("trailer content lost:\n%s", content)
	}
}

func TestInitBlock_ReplacesDriftedBlockInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	drifted := "prefix\n\n" + initBlockStart + "\nstale content\n" + initBlockEnd + "\n\nsuffix\n"
	if err := os.WriteFile(path, []byte(drifted), 0644); err != nil {
		t.Fatalf("seeding file: %v", err)
	}

	status, err := initUpsertFile(path, initProtocolBlock)
	if err != nil {
		t.Fatalf("initUpsertFile: %v", err)
	}
	if status != "updated" {
		t.Fatalf("status = %q, want updated", status)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "stale content") {
		t.Fatalf("stale block content not replaced:\n%s", content)
	}
	if !strings.HasPrefix(content, "prefix\n\n") || !strings.HasSuffix(content, "\nsuffix\n") {
		t.Fatalf("surrounding content not preserved:\n%s", content)
	}

	status, err = initUpsertFile(path, initProtocolBlock)
	if err != nil {
		t.Fatalf("initUpsertFile (second): %v", err)
	}
	if status != "unchanged" {
		t.Fatalf("status = %q, want unchanged", status)
	}
}

func TestInitDefaultFiles_Both(t *testing.T) {
	got := initDefaultFiles(t.TempDir())
	if len(got) != 2 || got[0] != "CLAUDE.md" || got[1] != "AGENTS.md" {
		t.Fatalf("initDefaultFiles = %v, want [CLAUDE.md AGENTS.md]", got)
	}
}

func TestInitCommand_DefaultFilesAndHookJSON(t *testing.T) {
	dir := t.TempDir()

	out := captureStdout(t, func() {
		if code := runInit([]string{"-dir", dir, "-hook", "-json"}); code != ExitOK {
			t.Fatalf("runInit exit code = %d, want %d", code, ExitOK)
		}
	})

	var result struct {
		Files []struct {
			File   string `json:"file"`
			Status string `json:"status"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	want := []string{"CLAUDE.md", "AGENTS.md", filepath.Join(".claude", "settings.json"), filepath.Join(".codex", "hooks.json"), filepath.Join(".github", "hooks", "callboard.json")}
	if len(result.Files) != len(want) {
		t.Fatalf("files = %v, want %v", result.Files, want)
	}
	for i, w := range want {
		if result.Files[i].File != w || result.Files[i].Status != "created" {
			t.Fatalf("files[%d] = %+v, want %s created", i, result.Files[i], w)
		}
	}
	wantHookFile := filepath.Join(".claude", "settings.json")
	if data, err := os.ReadFile(filepath.Join(dir, ".github", "hooks", "callboard.json")); err != nil || string(data) != copilotHooksFile {
		t.Fatalf("copilot hooks file: err=%v content=%q", err, data)
	}
	codexSettings := readSettings(t, filepath.Join(dir, ".codex", "hooks.json"))
	assertHookCommandPresent(t, codexSettings, "SessionStart", "callboard hook codex session-start")
	assertHookCommandPresent(t, codexSettings, "Stop", "callboard hook codex stop")
	// A second run leaves everything unchanged.
	out = captureStdout(t, func() {
		if code := runInit([]string{"-dir", dir, "-hook", "-json"}); code != ExitOK {
			t.Fatalf("second runInit exit code = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	for _, f := range result.Files {
		if f.Status != "unchanged" {
			t.Fatalf("second run: %+v, want unchanged", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, wantHookFile)); err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
}

// --- settings.json hook merge ---

func hookCommandCount(t *testing.T, settings map[string]any, event, cmd string) int {
	t.Helper()
	hooksAny, _ := settings["hooks"].(map[string]any)
	groupsAny, _ := hooksAny[event].([]any)
	count := 0
	for _, g := range groupsAny {
		group, _ := g.(map[string]any)
		steps, _ := group["hooks"].([]any)
		for _, s := range steps {
			step, _ := s.(map[string]any)
			if step["command"] == cmd {
				count++
			}
		}
	}
	return count
}

func assertHookCommandPresent(t *testing.T, settings map[string]any, event, cmd string) {
	t.Helper()
	if hookCommandCount(t, settings, event, cmd) == 0 {
		t.Fatalf("expected command %q under event %q, settings: %v", cmd, event, settings)
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, data)
	}
	return settings
}

func TestHooksInstall_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")

	status, err := initInstallHooks(path, "claude-code")
	if err != nil {
		t.Fatalf("initInstallHooks: %v", err)
	}
	if status != "created" {
		t.Fatalf("status = %q, want created", status)
	}

	settings := readSettings(t, path)
	assertHookCommandPresent(t, settings, "SessionStart", "callboard hook claude-code session-start")
	assertHookCommandPresent(t, settings, "Stop", "callboard hook claude-code stop")
}

func TestHooksInstall_PreservesUnrelatedKeysAndHooks(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(claudeDir, "settings.json")
	seed := `{
		"otherSetting": "keep-me",
		"hooks": {
			"PreToolUse": [
				{"matcher": "Bash", "hooks": [{"type": "command", "command": "some-other-hook", "timeout": 5}]}
			]
		}
	}`
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatalf("seeding file: %v", err)
	}

	status, err := initInstallHooks(path, "claude-code")
	if err != nil {
		t.Fatalf("initInstallHooks: %v", err)
	}
	if status != "updated" {
		t.Fatalf("status = %q, want updated", status)
	}

	settings := readSettings(t, path)
	if settings["otherSetting"] != "keep-me" {
		t.Fatalf("unrelated key lost: %v", settings)
	}
	assertHookCommandPresent(t, settings, "PreToolUse", "some-other-hook")
	assertHookCommandPresent(t, settings, "SessionStart", "callboard hook claude-code session-start")
	assertHookCommandPresent(t, settings, "Stop", "callboard hook claude-code stop")
}

func TestHooksInstall_NoDuplicateOnSecondRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")

	if _, err := initInstallHooks(path, "claude-code"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	status, err := initInstallHooks(path, "claude-code")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if status != "unchanged" {
		t.Fatalf("status = %q, want unchanged", status)
	}

	settings := readSettings(t, path)
	if n := hookCommandCount(t, settings, "Stop", "callboard hook claude-code stop"); n != 1 {
		t.Fatalf("Stop hook command count = %d, want 1", n)
	}
	if n := hookCommandCount(t, settings, "SessionStart", "callboard hook claude-code session-start"); n != 1 {
		t.Fatalf("SessionStart hook command count = %d, want 1", n)
	}
}

func TestHooksInstall_InvalidJSONFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bad := "{not valid json"
	if err := os.WriteFile(path, []byte(bad), 0644); err != nil {
		t.Fatalf("seeding file: %v", err)
	}

	if _, err := initInstallHooks(path, "claude-code"); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(data) != bad {
		t.Fatalf("file was modified despite invalid JSON: %s", data)
	}
}
