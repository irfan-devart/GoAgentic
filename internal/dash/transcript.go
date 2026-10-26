package dash

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The chat view: an agent's running session, read from its Claude Code
// transcript or codex rollout (codex.go), as the conversation the principal sees in the terminal. Only
// what was said is shown: the principal's messages and the agent's replies.
// Thinking, tool output and injected context are left out; tool calls fold
// into one line between messages.

// ChatEntry is one line of the chat: a message from the principal ("you"),
// a reply from the agent ("agent"), or the tools it ran in between
// ("tools").
type ChatEntry struct {
	Kind string
	At   time.Time
	Text string
	// Tools lists what ran, one line each, for a "tools" entry.
	Tools []string
}

// maxChat bounds the entries shown; a long session shows its latest part.
const maxChat = 300

// ChatCache parses each transcript once and then only the lines appended
// since, so a page refreshing every few seconds does not re-read megabytes.
type ChatCache struct {
	mu    sync.Mutex
	files map[string]*chatFile
}

type chatFile struct {
	offset int64
	// entries hold the whole file's chat, with the agent that each
	// /agents:start switched to, so one transcript can hold two agents.
	entries []chatLine
	agent   string
	// codex: the file is a codex rollout (its first line is the session
	// meta), not a Claude Code transcript.
	codex bool
}

type chatLine struct {
	agent string
	ChatEntry
}

// NewChatCache returns an empty cache.
func NewChatCache() *ChatCache { return &ChatCache{files: map[string]*chatFile{}} }

// Chat returns the part of the transcript at path that belongs to agent
// (from its last /agents:start on), latest maxChat entries.
func (c *ChatCache) Chat(path, agent string) ([]ChatEntry, error) {
	out, err := c.Session(path, agent)
	if len(out) > maxChat {
		out = out[len(out)-maxChat:]
	}
	return out, err
}

// Session is Chat without the bound: the whole of the agent's session.
func (c *ChatCache) Session(path, agent string) ([]ChatEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	cf := c.files[path]
	if cf == nil || info.Size() < cf.offset {
		cf = &chatFile{}
		c.files[path] = cf
	}
	if info.Size() > cf.offset {
		if err := cf.read(path); err != nil {
			return nil, err
		}
	}
	agent = strings.ToLower(agent)
	// The agent's session starts at its last start in this file.
	start := 0
	for i, l := range cf.entries {
		if l.Kind == "you" && l.agent == agent && isStartText(l.Text) {
			start = i
		}
	}
	var out []ChatEntry
	for _, l := range cf.entries[start:] {
		if l.agent == agent {
			out = append(out, l.ChatEntry)
		}
	}
	return out, nil
}

// read parses complete lines from the offset on; a half-written last line
// waits for the next call.
func (cf *chatFile) read(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(cf.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // EOF, or a partial line kept for later
		}
		if cf.offset == 0 && bytes.Contains(line, []byte(`"type":"session_meta"`)) {
			cf.codex = true
		}
		cf.offset += int64(len(line))
		if cf.codex {
			cf.addCodex(line)
			continue
		}
		cf.add(line)
	}
	return nil
}

type transcriptLine struct {
	Type        string          `json:"type"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Timestamp   time.Time       `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (cf *chatFile) add(line []byte) {
	// Most lines are request blobs, hooks and tool results: skip them
	// before decoding.
	if !bytes.Contains(line, []byte(`"type":"user"`)) && !bytes.Contains(line, []byte(`"type":"assistant"`)) {
		return
	}
	var tl transcriptLine
	if json.Unmarshal(line, &tl) != nil || tl.IsMeta || tl.IsSidechain {
		return
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(tl.Message, &msg) != nil {
		return
	}
	var blocks []contentBlock
	var text string
	if json.Unmarshal(msg.Content, &text) == nil {
		blocks = []contentBlock{{Type: "text", Text: text}}
	} else if json.Unmarshal(msg.Content, &blocks) != nil {
		return
	}

	switch tl.Type {
	case "user":
		for _, b := range blocks {
			if b.Type != "text" {
				continue // tool results
			}
			said, agent := userText(b.Text)
			if said == "" {
				continue
			}
			if agent != "" {
				cf.agent = agent
			}
			cf.push(ChatEntry{Kind: "you", At: tl.Timestamp, Text: said})
		}
	case "assistant":
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					cf.push(ChatEntry{Kind: "agent", At: tl.Timestamp, Text: t})
				}
			case "tool_use":
				desc := toolLine(b.Name, b.Input)
				if n := len(cf.entries); n > 0 && cf.entries[n-1].Kind == "tools" && cf.entries[n-1].agent == cf.agent {
					cf.entries[n-1].Tools = append(cf.entries[n-1].Tools, desc)
					continue
				}
				cf.push(ChatEntry{Kind: "tools", At: tl.Timestamp, Tools: []string{desc}})
			}
		}
	}
}

func (cf *chatFile) push(e ChatEntry) {
	cf.entries = append(cf.entries, chatLine{agent: cf.agent, ChatEntry: e})
}

// userText is what the principal typed, or "" for text the harness
// injected. A slash command shows as typed; /agents:start also names the
// agent the session now belongs to (not for peer or task sessions, which
// run in their own transcripts anyway).
func userText(s string) (said, agent string) {
	s = strings.TrimSpace(s)
	if m := startArgsRe.FindStringSubmatch(s); m != nil || strings.Contains(s, "<command-name>") {
		name := between(s, "<command-name>", "</command-name>")
		args := ""
		if m != nil {
			args = strings.TrimSpace(m[1])
		}
		if strings.TrimPrefix(name, "/") == "agents:start" {
			if f := strings.Fields(args); len(f) > 0 && !(len(f) > 1 && (strings.EqualFold(f[1], "peer") || strings.EqualFold(f[1], "task"))) {
				agent = strings.ToLower(f[0])
			}
		}
		return strings.TrimSpace(name + " " + args), agent
	}
	for _, injected := range []string{"<system-reminder>", "<local-command", "<command-message>", "<task-notification>", "Caveat:"} {
		if strings.HasPrefix(s, injected) {
			return "", ""
		}
	}
	return s, ""
}

func between(s, open, close string) string {
	_, rest, ok := strings.Cut(s, open)
	if !ok {
		return ""
	}
	in, _, _ := strings.Cut(rest, close)
	return strings.TrimSpace(in)
}

// toolLine says what a tool call did in a few words: the command's
// description, the file it touched, the search it ran.
func toolLine(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string {
		v, _ := in[k].(string)
		return strings.TrimSpace(v)
	}
	detail := ""
	for _, k := range []string{"description", "file_path", "pattern", "query", "url", "prompt", "command"} {
		if v := str(k); v != "" {
			detail = v
			break
		}
	}
	if i := strings.IndexByte(detail, '\n'); i >= 0 {
		detail = detail[:i] + " …"
	}
	if r := []rune(detail); len(r) > 120 {
		detail = string(r[:120]) + "…"
	}
	if detail == "" {
		return name
	}
	return name + ": " + detail
}

// ToolSummary counts a tools entry by tool: "Bash ×3 · Edit".
func ToolSummary(tools []string) string {
	counts := map[string]int{}
	var order []string
	for _, t := range tools {
		name, _, _ := strings.Cut(t, ":")
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	parts := make([]string, len(order))
	for i, n := range order {
		parts[i] = n
		if counts[n] > 1 {
			parts[i] += fmt.Sprintf(" ×%d", counts[n])
		}
	}
	return strings.Join(parts, " · ")
}

var (
	mdCodeRe = regexp.MustCompile("`([^`\\n]+)`")
	mdBoldRe = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdLinkRe = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	mdHeadRe = regexp.MustCompile(`(?m)^#{1,6} (.+)$`)
)

// chatMarkdown renders the little Markdown an agent's reply uses (code
// spans and fences, bold, headings, links) as HTML. The text is escaped
// first, so nothing in a reply can add markup of its own; line breaks are
// kept by the stylesheet.
func chatMarkdown(s string) template.HTML {
	var b strings.Builder
	parts := strings.Split(s, "```")
	for i, part := range parts {
		if i%2 == 1 {
			// A fence: drop the language tag on its first line.
			if nl := strings.IndexByte(part, '\n'); nl >= 0 && !strings.ContainsAny(part[:nl], " \t") {
				part = part[nl+1:]
			}
			b.WriteString("<pre>" + html.EscapeString(strings.TrimRight(part, "\n")) + "</pre>")
			continue
		}
		b.WriteString(chatProse(part))
	}
	return template.HTML(b.String()) // escaped above
}

// chatProse renders text outside fences: tables as tables, the rest line by
// line with the inline marks.
func chatProse(s string) string {
	var b strings.Builder
	lines := strings.Split(s, "\n")
	text := []string{}
	flush := func() {
		if len(text) > 0 {
			b.WriteString(chatInline(strings.Join(text, "\n")))
			text = text[:0]
		}
	}
	for i := 0; i < len(lines); {
		if i+1 < len(lines) && isTableRow(lines[i]) && mdRuleRe.MatchString(strings.TrimSpace(lines[i+1])) {
			end := i + 2
			for end < len(lines) && isTableRow(lines[end]) {
				end++
			}
			// A table is a block of its own: it takes the place of the
			// blank lines around it.
			if n := len(text); n > 0 && strings.TrimSpace(text[n-1]) == "" {
				text = text[:n-1]
			}
			flush()
			b.WriteString(chatTable(lines[i], lines[i+2:end]))
			i = end
			if i < len(lines) && strings.TrimSpace(lines[i]) == "" {
				i++
			}
			continue
		}
		text = append(text, lines[i])
		i++
	}
	flush()
	return b.String()
}

var mdRuleRe = regexp.MustCompile(`^\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?$`)

func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "|") && strings.Count(t, "|") >= 2
}

func tableCells(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimSuffix(strings.TrimPrefix(t, "|"), "|")
	cells := strings.Split(t, "|")
	for i, c := range cells {
		cells[i] = chatInline(strings.TrimSpace(c))
	}
	return cells
}

// chatTable renders a Markdown table on one line, so the stylesheet's kept
// line breaks add no gaps inside it.
func chatTable(head string, rows []string) string {
	var b strings.Builder
	b.WriteString(`<div class="mdtable"><table><thead><tr>`)
	for _, c := range tableCells(head) {
		b.WriteString("<th>" + c + "</th>")
	}
	b.WriteString("</tr></thead><tbody>")
	for _, r := range rows {
		b.WriteString("<tr>")
		for _, c := range tableCells(r) {
			b.WriteString("<td>" + c + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table></div>")
	return b.String()
}

func chatInline(s string) string {
	t := html.EscapeString(s)
	t = mdCodeRe.ReplaceAllString(t, "<code>$1</code>")
	t = mdBoldRe.ReplaceAllString(t, "<strong>$1</strong>")
	t = mdLinkRe.ReplaceAllString(t, `<a href="$2" rel="noreferrer" target="_blank">$1</a>`)
	t = mdHeadRe.ReplaceAllString(t, "<strong>$1</strong>")
	return t
}
