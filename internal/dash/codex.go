package dash

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Codex sessions: one rollout-*.jsonl per session under
// $CODEX_HOME/sessions/YYYY/MM/DD (default ~/.codex). The first line is the
// session's meta (cwd, start, and whether it is the user's thread or a
// side thread such as the guardian reviewer); then the turns as
// response_item lines. Herdr names no session for a codex pane, so the
// agent's session is the newest user thread in its workspace whose first
// message starts it.

// codexDays bounds how far back a session is looked for.
const codexDays = 14

// CodexHome is $CODEX_HOME, else ~/.codex.
func CodexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// codexHead is what a rollout's first lines say: where and when it began,
// whether it is a user thread, and the agent its first message started.
type codexHead struct {
	cwd     string
	started time.Time
	user    bool
	agent   string
	// final: the first message has been seen, so the head will not change.
	final bool
}

var codexHeads = struct {
	sync.Mutex
	m map[string]codexHead
}{m: map[string]codexHead{}}

// CodexSession is the newest codex session of agent in the workspace at
// root that began at or after since (zero: any), or "".
func CodexSession(home, root, agent string, since time.Time) string {
	agent = strings.ToLower(agent)
	var files []string
	for d := 0; d < codexDays; d++ {
		day := time.Now().AddDate(0, 0, -d)
		m, _ := filepath.Glob(filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"), "rollout-*.jsonl"))
		files = append(files, m...)
	}
	// rollout-<start time>-<id>: newest first by name.
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	root = filepath.Clean(root)
	for _, path := range files {
		h := readCodexHead(path)
		if !h.user || h.agent != agent || (!since.IsZero() && h.started.Before(since)) {
			continue
		}
		if cwd := filepath.Clean(h.cwd); cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			return path
		}
	}
	return ""
}

func readCodexHead(path string) codexHead {
	codexHeads.Lock()
	h, ok := codexHeads.m[path]
	codexHeads.Unlock()
	if ok && h.final {
		return h
	}
	h = codexHead{}
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for n := 0; n < 200; n++ {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var l codexLine
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		switch {
		case l.Type == "session_meta":
			var m struct {
				Cwd          string    `json:"cwd"`
				Timestamp    time.Time `json:"timestamp"`
				ThreadSource string    `json:"thread_source"`
				Parent       string    `json:"parent_thread_id"`
			}
			_ = json.Unmarshal(l.Payload, &m)
			h.cwd, h.started = m.Cwd, m.Timestamp
			h.user = m.ThreadSource == "user" && m.Parent == ""
			if !h.user {
				h.final = true
			}
		case l.Type == "response_item":
			if said, agent := codexUser(l.Payload); said != "" {
				h.agent, h.final = agent, true
			}
		}
		if h.final {
			break
		}
	}
	codexHeads.Lock()
	codexHeads.m[path] = h
	codexHeads.Unlock()
	return h
}

type codexLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Name    string `json:"name"`
	Input   string `json:"input"`
	Args    string `json:"arguments"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// codexInjected are user messages codex adds itself: AGENTS.md, the
// environment, a skill's text.
var codexInjected = []string{"# AGENTS.md", "<environment_context>", "<skill>", "<user_instructions>", "<permissions", "<turn_aborted>", "<subagent"}

// startTextRe is an agent start as typed in any harness (conventions §
// Invocation): /agents:start, $agents-start or /agents-start, then the name.
var startTextRe = regexp.MustCompile(`^(?:/agents:start|\$agents-start|/agents-start)\s+(\S+)(?:\s+(\S+))?`)

// isStartText says whether a chat line is an agent start.
func isStartText(s string) bool { return startTextRe.MatchString(s) }

// codexUser is the principal's text in a user message, and the agent it
// starts (not for peer or task sessions), or "" for injected text.
func codexUser(payload json.RawMessage) (said, agent string) {
	if !bytes.Contains(payload, []byte(`"role":"user"`)) {
		return "", ""
	}
	var it codexItem
	if json.Unmarshal(payload, &it) != nil || it.Type != "message" || it.Role != "user" {
		return "", ""
	}
	var parts []string
	for _, c := range it.Content {
		t := strings.TrimSpace(c.Text)
		injected := t == ""
		for _, p := range codexInjected {
			injected = injected || strings.HasPrefix(t, p)
		}
		if !injected {
			parts = append(parts, t)
		}
	}
	said = strings.Join(parts, "\n")
	if m := startTextRe.FindStringSubmatch(said); m != nil && !strings.EqualFold(m[2], "peer") && !strings.EqualFold(m[2], "task") {
		agent = strings.ToLower(m[1])
	}
	return said, agent
}

// codexCmdRe finds the command in a codex exec call's input.
var codexCmdRe = regexp.MustCompile(`cmd"?\s*:\s*"((?:[^"\\]|\\.){1,120})`)

// addCodex is chatFile.add for a codex rollout line.
func (cf *chatFile) addCodex(line []byte) {
	if !bytes.Contains(line, []byte(`"type":"response_item"`)) {
		return
	}
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	if said, agent := codexUser(l.Payload); said != "" {
		if agent != "" {
			cf.agent = agent
		}
		cf.push(ChatEntry{Kind: "you", At: l.Timestamp, Text: said})
		return
	}
	var it codexItem
	if json.Unmarshal(l.Payload, &it) != nil {
		return
	}
	switch {
	case it.Type == "message" && it.Role == "assistant":
		var parts []string
		for _, c := range it.Content {
			if t := strings.TrimSpace(c.Text); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			cf.push(ChatEntry{Kind: "agent", At: l.Timestamp, Text: strings.Join(parts, "\n\n")})
		}
	case it.Type == "custom_tool_call" || it.Type == "function_call":
		desc := it.Name
		if m := codexCmdRe.FindStringSubmatch(it.Input + it.Args); m != nil {
			desc += ": " + strings.ReplaceAll(m[1], `\"`, `"`)
		}
		if n := len(cf.entries); n > 0 && cf.entries[n-1].Kind == "tools" && cf.entries[n-1].agent == cf.agent {
			cf.entries[n-1].Tools = append(cf.entries[n-1].Tools, desc)
			return
		}
		cf.push(ChatEntry{Kind: "tools", At: l.Timestamp, Tools: []string{desc}})
	}
}
