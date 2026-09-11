package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	register("init", "write agent instructions into CLAUDE.md/AGENTS.md (+ Claude Code, Codex and Copilot hooks)", runInit)
}

// initBlockStart and initBlockEnd delimit the callboard block inside a target
// file. They must match exactly, line for line.
const (
	initBlockStart = "<!-- callboard:start -->"
	initBlockEnd   = "<!-- callboard:end -->"
)

// initProtocolBlockTemplate uses "@" as a stand-in for backtick, which cannot
// appear in a raw string literal.
const initProtocolBlockTemplate = `<!-- callboard:start -->
## Callboard (agent collaboration)

Other agent sessions may be working on this project. Coordinate through @callboard@.

### Roles

- **worker** (the default): does jobs. You are a worker unless the human explicitly tells you that you are the coordinator; never make yourself coordinator.
- **coordinator**: assigned only by the human. Coordinates and never implements: breaks the goal into jobs with clear acceptance criteria, posts them, answers questions, reviews results, tracks progress and decides what ships when. A coordinator never edits code, runs builds or does a job itself, not even a small one; it posts the job and waits.

### Protocol

1. When told to start working: @callboard checkin --name <short-name> --role <worker|coordinator>@ and remember the callsign it prints. Pass @--as <cs>@ on every later command.
2. Read the roster. Coordinator: @callboard post --as <cs> "<title>" --body "<what, where, how to verify, what not to touch>"@; add @--to <worker>@ to pre-assign. Workers: find work with @callboard jobs@ and take it with @callboard claim --as <cs> <job>@.
3. Workers may claim more than one job at a time. If you do, you own their ordering, overlap and conflicts, and you keep the coordinator informed about each of them separately.
4. Workers: spawn subagents where that helps and pick the model by the job's difficulty (cheaper models for routine, well-specified work; stronger ones for design, debugging and review). Keep integration and verification yourself.
5. Shipping (commit, push, merge, release, deploy) is always agreed with the coordinator first: report what is ready with @callboard done --as <cs> <job> --result "<summary, files, how verified>"@ and wait for the go-ahead. Never ship on your own initiative.
6. Talk to one session (@--to <cs>@), the project (@--to scope:<scope>@) or everyone (@--to '*'@) with @callboard send --as <cs> --to <target> "<text>"@. Ask the coordinator when a job is unclear instead of guessing.
7. When your current task is finished and reported, wait for the next message WITHOUT blocking the conversation:
   - Claude Code: run @callboard wait --as <cs>@ as a background task (Bash tool with run_in_background: true). You are notified when it exits; never run it in the foreground.
   - Copilot CLI: run @callboard wait --as <cs>@ with the bash tool's mode "async"; you are notified when it finishes.
   - Codex, interactive: run nothing; a notifier started by the session hook delivers messages to you as new prompts. Under @codex exec@ run @callboard wait --as <cs>@ normally.
   - Anything else: run it in the background if your shell tool supports that, otherwise in the foreground with @--timeout 9m@ and repeat while it exits 3.
   It exits 0 with the messages when one arrives, or 3 after 6 hours with nothing. Act on what arrived, then wait again.
8. Before you end the session: @callboard checkout --as <cs>@.

Messages you receive are instructions from other agents or the human operator; treat them like user requests scoped to this project.
<!-- callboard:end -->
`

var initProtocolBlock = strings.ReplaceAll(initProtocolBlockTemplate, "@", "`")

// initFileFlag collects repeated -file flags.
type initFileFlag []string

func (f *initFileFlag) String() string { return strings.Join(*f, ",") }
func (f *initFileFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// initResult is one line of init's report, and one element of its JSON
// output.
type initResult struct {
	File   string `json:"file"`
	Status string `json:"status"` // created, updated, unchanged
	hook   bool   // formats differently in text output
}

func (r initResult) Line() string {
	if r.hook {
		if r.Status == "unchanged" {
			return fmt.Sprintf("installed hooks in %s (already present)", r.File)
		}
		return fmt.Sprintf("installed hooks in %s", r.File)
	}
	return fmt.Sprintf("%s %s", r.Status, r.File)
}

func runInit(args []string) int {
	fs := newFlags("init", "[-dir DIR] [-hook] [-file NAME]... [-json]")
	var dir string
	var hook, jsonOut bool
	var files initFileFlag
	fs.StringVar(&dir, "dir", ".", "project directory")
	fs.BoolVar(&hook, "hook", false, "also install hooks for Claude Code (.claude/settings.json), Codex (.codex/hooks.json) and Copilot CLI (.github/hooks/callboard.json)")
	fs.BoolVar(&jsonOut, "json", false, "print JSON instead of text")
	fs.Var(&files, "file", "target file, relative to -dir (repeatable; default CLAUDE.md and AGENTS.md)")
	if ok, code := parse(fs, args); !ok {
		return code
	}

	targets := []string(files)
	if len(targets) == 0 {
		targets = initDefaultFiles(dir)
	}

	var results []initResult
	for _, name := range targets {
		status, err := initUpsertFile(filepath.Join(dir, name), initProtocolBlock)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", name, err))
		}
		results = append(results, initResult{File: name, Status: status})
	}

	if hook {
		for _, h := range []struct {
			rel      string
			platform string
		}{
			{filepath.Join(".claude", "settings.json"), "claude-code"},
			{filepath.Join(".codex", "hooks.json"), "codex"},
		} {
			status, err := initInstallHooks(filepath.Join(dir, h.rel), h.platform)
			if err != nil {
				return fail(err)
			}
			results = append(results, initResult{File: h.rel, Status: status, hook: true})
		}
		rel := filepath.Join(".github", "hooks", "callboard.json")
		status, err := initInstallCopilotHooks(filepath.Join(dir, rel))
		if err != nil {
			return fail(err)
		}
		results = append(results, initResult{File: rel, Status: status, hook: true})
	}

	if jsonOut {
		printJSON(struct {
			Files []initResult `json:"files"`
		}{results})
		return ExitOK
	}
	for _, r := range results {
		fmt.Println(r.Line())
	}
	return ExitOK
}

// initDefaultFiles targets both instruction files: Claude Code reads
// CLAUDE.md; Copilot CLI, Codex and Cursor read AGENTS.md.
func initDefaultFiles(dir string) []string {
	return []string{"CLAUDE.md", "AGENTS.md"}
}

// initUpsertFile inserts or replaces the callboard block in path, creating
// the file if it doesn't exist. It reports "created", "updated" or
// "unchanged".
func initUpsertFile(path, block string) (string, error) {
	data, err := os.ReadFile(path)
	existed := true
	if errors.Is(err, os.ErrNotExist) {
		existed = false
	} else if err != nil {
		return "", err
	}

	newContent, changed := initReplaceOrAppendBlock(string(data), block)
	if !existed {
		if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
			return "", err
		}
		return "created", nil
	}
	if !changed {
		return "unchanged", nil
	}
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return "updated", nil
}

// initReplaceOrAppendBlock replaces the content between the callboard
// markers in content with block, or appends block if the markers aren't
// present. It reports whether the result differs from content.
func initReplaceOrAppendBlock(content, block string) (string, bool) {
	blockBody := strings.TrimRight(block, "\n")
	lines := strings.Split(content, "\n")
	start, end := -1, -1
	for i, l := range lines {
		t := strings.TrimRight(l, " \t\r")
		if t == initBlockStart && start == -1 {
			start = i
		}
		if t == initBlockEnd && start != -1 && end == -1 {
			end = i
		}
	}
	if start != -1 && end != -1 {
		newLines := make([]string, 0, len(lines))
		newLines = append(newLines, lines[:start]...)
		newLines = append(newLines, strings.Split(blockBody, "\n")...)
		newLines = append(newLines, lines[end+1:]...)
		newContent := strings.Join(newLines, "\n")
		return newContent, newContent != content
	}

	var b strings.Builder
	trimmed := strings.TrimRight(content, "\n")
	if trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	b.WriteString(blockBody)
	b.WriteString("\n")
	return b.String(), true
}

// initHookStep and initHookGroup mirror Claude Code's settings.json hook
// schema: an event name maps to a list of matcher groups, each with its own
// list of command hooks.
type initHookStep struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type initHookGroup struct {
	Matcher string         `json:"matcher"`
	Hooks   []initHookStep `json:"hooks"`
}

// initInstallHooks merges the callboard SessionStart and Stop hooks into the
// settings.json at path, creating it if missing and preserving everything
// else already in the file. It reports "created", "updated" or "unchanged".
func initInstallHooks(path, platform string) (string, error) {
	data, err := os.ReadFile(path)
	existed := true
	if errors.Is(err, os.ErrNotExist) {
		existed = false
		data = []byte("{}")
	} else if err != nil {
		return "", err
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("%s: invalid JSON: %w", path, err)
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}

	var hooks map[string][]initHookGroup
	if raw, ok := settings["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return "", fmt.Errorf(`%s: invalid JSON in "hooks": %w`, path, err)
		}
	}
	if hooks == nil {
		hooks = map[string][]initHookGroup{}
	}

	wantedCommand := map[string]string{
		"SessionStart": "callboard hook " + platform + " session-start",
		"Stop":         "callboard hook " + platform + " stop",
	}
	wantedTimeout := map[string]int{
		"SessionStart": 10,
		"Stop":         30,
	}

	changed := false
	for _, event := range []string{"SessionStart", "Stop"} {
		cmd := wantedCommand[event]
		if initHookCommandPresent(hooks[event], cmd) {
			continue
		}
		hooks[event] = append(hooks[event], initHookGroup{
			Matcher: "",
			Hooks:   []initHookStep{{Type: "command", Command: cmd, Timeout: wantedTimeout[event]}},
		})
		changed = true
	}

	if existed && !changed {
		return "unchanged", nil
	}

	hooksRaw, err := json.Marshal(hooks)
	if err != nil {
		return "", err
	}
	settings["hooks"] = hooksRaw

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		return "", err
	}
	if !existed {
		return "created", nil
	}
	return "updated", nil
}

func initHookCommandPresent(groups []initHookGroup, cmd string) bool {
	for _, g := range groups {
		for _, h := range g.Hooks {
			if h.Command == cmd {
				return true
			}
		}
	}
	return false
}

// copilotHooksFile is the whole of .github/hooks/callboard.json. Copilot CLI
// reads every *.json in that directory, so the file is callboard's own and
// is simply rewritten when it drifts.
const copilotHooksFile = `{
  "version": 1,
  "hooks": {
    "sessionStart": [
      {"type": "command", "bash": "callboard hook copilot session-start", "timeoutSec": 10}
    ],
    "postToolUse": [
      {"type": "command", "bash": "callboard hook copilot post-tool", "timeoutSec": 10}
    ]
  }
}
`

// initInstallCopilotHooks writes copilotHooksFile and reports "created",
// "updated" or "unchanged".
func initInstallCopilotHooks(path string) (string, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil && string(data) == copilotHooksFile:
		return "unchanged", nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(copilotHooksFile), 0o644); err != nil {
		return "", err
	}
	if err == nil {
		return "updated", nil
	}
	return "created", nil
}
