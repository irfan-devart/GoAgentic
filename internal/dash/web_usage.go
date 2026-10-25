package dash

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The running session's model and how full its context window is, read
// from the end of its transcript. Claude Code writes the model and the
// tokens sent on every reply; codex writes the model on each turn and the
// window itself with its token counts.

type sessionUsage struct {
	// Model is the harness's model id; Effort codex's reasoning effort.
	Model, Effort string
	// Used is the tokens in the context at the last reply; Window the
	// context window, 0 when not known.
	Used, Window int
}

// usageTail is how much of the transcript's end is read.
const usageTail = 1 << 20

// readUsage reads the model and context use from a transcript, or nil.
func readUsage(path string) *sessionUsage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > usageTail {
		_, _ = f.Seek(info.Size()-usageTail, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	lines := bytes.Split(data, []byte("\n"))
	u := &sessionUsage{}
	for i := len(lines) - 1; i >= 0 && (u.Model == "" || u.Used == 0); i-- {
		line := lines[i]
		switch {
		case bytes.Contains(line, []byte(`"usage"`)) && bytes.Contains(line, []byte(`"assistant"`)):
			var l struct {
				Type    string `json:"type"`
				Message struct {
					Model string `json:"model"`
					Usage struct {
						Input       int `json:"input_tokens"`
						CacheCreate int `json:"cache_creation_input_tokens"`
						CacheRead   int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &l) != nil || l.Type != "assistant" || l.Message.Model == "" || l.Message.Model == "<synthetic>" {
				continue
			}
			if u.Model == "" {
				u.Model = l.Message.Model
			}
			if u.Used == 0 {
				m := l.Message.Usage
				u.Used = m.Input + m.CacheCreate + m.CacheRead
			}
		case bytes.Contains(line, []byte(`"token_count"`)):
			var l struct {
				Payload struct {
					Type string `json:"type"`
					Info *struct {
						Last struct {
							Total int `json:"total_tokens"`
						} `json:"last_token_usage"`
						Window int `json:"model_context_window"`
					} `json:"info"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Payload.Type == "token_count" && l.Payload.Info != nil && u.Used == 0 {
				u.Used, u.Window = l.Payload.Info.Last.Total, l.Payload.Info.Window
			}
		case bytes.Contains(line, []byte(`"turn_context"`)) && u.Model == "":
			var l struct {
				Type    string `json:"type"`
				Payload struct {
					Model  string `json:"model"`
					Effort string `json:"effort"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Type == "turn_context" {
				u.Model, u.Effort = l.Payload.Model, l.Payload.Effort
			}
		}
	}
	if u.Model == "" && u.Used == 0 {
		return nil
	}
	if u.Window == 0 {
		u.Window = claudeWindow(u.Model)
	}
	return u
}

// claudeWindow is a Claude model's context window, which Claude Code does
// not write down: a million tokens for the 5 family and for a "[1m]"
// model, 200k before; 0 for a model that is not Claude.
func claudeWindow(model string) int {
	switch {
	case !strings.HasPrefix(model, "claude-"):
		return 0
	case strings.HasSuffix(model, "[1m]"), claudeFamily(model) >= 5:
		return 1_000_000
	}
	return 200_000
}

// claudeFamily is the major version in a Claude model id
// (claude-opus-5-5 → 5, claude-sonnet-4-6 → 4), or 0.
func claudeFamily(model string) int {
	for _, part := range strings.Split(strings.TrimSuffix(model, "[1m]"), "-")[1:] {
		var n int
		if _, err := fmt.Sscanf(part, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

// modelName is a model id as people say it: claude-opus-5-5 → Opus 5.5.
func modelName(id string) string {
	id = strings.TrimSuffix(id, "[1m]")
	if !strings.HasPrefix(id, "claude-") {
		return id
	}
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	if len(parts) == 0 || parts[0] == "" {
		return id
	}
	name := strings.ToUpper(parts[0][:1]) + parts[0][1:]
	var version []string
	for _, p := range parts[1:] {
		if len(p) > 2 { // a date stamp
			break
		}
		version = append(version, p)
	}
	if len(version) > 0 {
		name += " " + strings.Join(version, ".")
	}
	return name
}

// tokens is a count as it reads: 950, 12k, 271k, 1M.
func tokens(n int) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dM", n/1_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

// defaultContextBudget is the share of the context window a session
// should stay under before it wraps: short, wrapped sessions keep agents
// sharp and their memory curated.
const defaultContextBudget = 40

// contextBudget reads context-budget: "40" or "40%", else the default.
func contextBudget(v string) int {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimSpace(v), "%"), "%d", &n); err == nil && n > 0 && n <= 100 {
		return n
	}
	return defaultContextBudget
}

// contextHealth judges a session's context use against the budget:
// healthy, wrap soon (within a quarter of the budget), or past it.
func contextHealth(u *sessionUsage, budget int) *webCtx {
	if u == nil || u.Used == 0 || u.Window == 0 {
		return nil
	}
	if budget <= 0 {
		budget = defaultContextBudget
	}
	pct := min(u.Used*100/u.Window, 100)
	c := &webCtx{Pct: pct, Budget: budget, Tone: "ok", Text: fmt.Sprintf("%d%% context", pct), State: "healthy"}
	switch {
	case pct >= budget:
		c.Tone, c.State, c.Over = "blocked", "past budget", true
		c.Text = fmt.Sprintf("%d%% context, wrap", pct)
	case pct >= budget*3/4:
		c.Tone, c.State, c.Soon = "waiting", "wrap soon", true
		c.Text = fmt.Sprintf("%d%% context, wrap soon", pct)
	}
	c.Hint = fmt.Sprintf("%s of %s tokens in context at the last reply. Session health: %s (budget %d%%; wrap by then so the session stays sharp and its memory is saved)",
		tokens(u.Used), tokens(u.Window), c.State, budget)
	return c
}
