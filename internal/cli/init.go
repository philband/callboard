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
	register("init", "write agent instructions into CLAUDE.md/AGENTS.md (+ Claude Code hooks)", runInit)
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

Other agent sessions may be working on this project. Coordinate through @callboard@:

1. When told to start working: @callboard checkin --name <short-name> --role <coordinator|worker>@ and remember the callsign it prints. Pass @--as <callsign>@ on every later command.
2. Read the roster it prints. If you are the coordinator, hand out work with @callboard post --as <cs> --to <worker> "<title>" --body "<what to do>"@ (omit --to to let anyone claim it). Workers pick up work with @callboard jobs@ and @callboard claim --as <cs> <job>@.
3. Talk to one session (@--to <cs>@), the whole project (@--to scope:<scope>@) or everyone (@--to '*'@) with @callboard send --as <cs> --to <target> "<text>"@.
4. When your current task is finished: report with @callboard done --as <cs> <job> --result "<summary>"@, then run @callboard wait --as <cs>@ with your shell tool's timeout raised to 10 minutes (Claude Code: timeout 600000). It blocks until a message arrives (exit 3 after 9 minutes with nothing; then run it again). Act on what arrives, then wait again.
5. Before you end the session: @callboard checkout --as <cs>@.

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
	fs.BoolVar(&hook, "hook", false, "also install Claude Code hooks")
	fs.BoolVar(&jsonOut, "json", false, "print JSON instead of text")
	fs.Var(&files, "file", "target file, relative to -dir (repeatable; default: whichever of CLAUDE.md and AGENTS.md exist, else CLAUDE.md)")
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
		rel := filepath.Join(".claude", "settings.json")
		status, err := initInstallHooks(filepath.Join(dir, rel))
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

// initDefaultFiles targets whichever of CLAUDE.md and AGENTS.md exist, or
// CLAUDE.md alone when neither does.
func initDefaultFiles(dir string) []string {
	var files []string
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return []string{"CLAUDE.md"}
	}
	return files
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
func initInstallHooks(path string) (string, error) {
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
		"SessionStart": "callboard hook session-start",
		"Stop":         "callboard hook stop",
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
