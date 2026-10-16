package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/config"
)

// tailBytes bounds how much of a transcript one probe reads.
const tailBytes = 2 << 20

// defaultClaudeWindow is used when the account sets no context_window;
// Claude transcripts record usage but not the window size.
const defaultClaudeWindow = 200_000

// readTranscript probes a spawned session through its platform's transcript:
// Claude Code's <config>/projects/*/<session>.jsonl or Codex's
// <home>/sessions/*/*/*/rollout-*-<thread>.jsonl.
func readTranscript(sp api.Spawn, acct config.Account) Probe {
	if sp.PlatformSession == "" {
		return Probe{Context: -1}
	}
	home, _ := os.UserHomeDir()
	switch sp.Platform {
	case api.PlatformClaude:
		dir := acct.ConfigDir
		if dir == "" {
			dir = filepath.Join(home, ".claude")
		}
		files, _ := filepath.Glob(filepath.Join(dir, "projects", "*", sp.PlatformSession+".jsonl"))
		if len(files) == 0 {
			return Probe{Context: -1}
		}
		window := acct.ContextWindow
		if window <= 0 {
			window = defaultClaudeWindow
		}
		return probeClaude(tail(files[0]), window)
	case api.PlatformCodex:
		dir := acct.ConfigDir
		if dir == "" {
			dir = filepath.Join(home, ".codex")
		}
		files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*", "*", "rollout-*-"+sp.PlatformSession+".jsonl"))
		if len(files) == 0 {
			return Probe{Context: -1}
		}
		return probeCodex(tail(files[0]), acct.ContextWindow)
	}
	return Probe{Context: -1}
}

func tail(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailBytes {
		_, _ = f.Seek(-tailBytes, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	return data
}

func lines(data []byte, fn func([]byte)) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<16), 16<<20)
	for sc.Scan() {
		fn(sc.Bytes())
	}
}

// probeClaude takes the context in use from the last assistant turn's usage
// and reports a limit when the last assistant entry is an API error about
// usage, billing or login.
func probeClaude(data []byte, window int) Probe {
	p := Probe{Context: -1}
	lines(data, func(l []byte) {
		var e struct {
			Type     string `json:"type"`
			APIError bool   `json:"isApiErrorMessage"`
			Error    string `json:"error"`
			Message  struct {
				Usage *struct {
					Input         int `json:"input_tokens"`
					CacheCreation int `json:"cache_creation_input_tokens"`
					CacheRead     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(l, &e) != nil || e.Type != "assistant" {
			return
		}
		if e.APIError {
			switch e.Error {
			case "rate_limit", "billing_error", "authentication_failed":
				p.Limit = e.Error
				if len(e.Message.Content) > 0 && e.Message.Content[0].Text != "" {
					p.Limit = e.Message.Content[0].Text
				}
			default:
				p.Limit = ""
			}
			return
		}
		p.Limit = ""
		if u := e.Message.Usage; u != nil {
			p.Context = float64(u.Input+u.CacheCreation+u.CacheRead) / float64(window)
		}
	})
	return p
}

// probeCodex reads token_count events (context in use, the model's window,
// rate limit state) and turn errors marked usage_limit_exceeded.
func probeCodex(data []byte, window int) Probe {
	p := Probe{Context: -1}
	var resets int64
	lines(data, func(l []byte) {
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info *struct {
					Last struct {
						Input int `json:"input_tokens"`
					} `json:"last_token_usage"`
					Window int `json:"model_context_window"`
				} `json:"info"`
				RateLimits *struct {
					Primary *struct {
						Used     float64 `json:"used_percent"`
						ResetsAt int64   `json:"resets_at"`
					} `json:"primary"`
				} `json:"rate_limits"`
				Error *struct {
					Message string `json:"message"`
					Info    string `json:"codex_error_info"`
				} `json:"error"`
			} `json:"payload"`
		}
		if json.Unmarshal(l, &e) != nil || e.Type != "event_msg" {
			return
		}
		switch e.Payload.Type {
		case "token_count":
			if i := e.Payload.Info; i != nil {
				w := i.Window
				if w <= 0 {
					w = window
				}
				if w > 0 {
					p.Context = float64(i.Last.Input) / float64(w)
				}
			}
			if r := e.Payload.RateLimits; r != nil && r.Primary != nil && r.Primary.ResetsAt > 0 {
				resets = r.Primary.ResetsAt
			}
		case "task_complete", "error":
			if err := e.Payload.Error; err != nil && err.Info == "usage_limit_exceeded" {
				p.Limit = err.Message
			} else if e.Payload.Type == "task_complete" {
				p.Limit = ""
			}
		}
	})
	if p.Limit != "" && resets > 0 {
		p.Until = time.Unix(resets, 0)
	}
	return p
}
