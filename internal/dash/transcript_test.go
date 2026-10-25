package dash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const transcriptFixture = `{"type":"user","isSidechain":false,"timestamp":"2026-10-25T03:43:12.000Z","message":{"role":"user","content":"<command-message>agents:start</command-message>\n<command-name>/agents:start</command-name>\n<command-args>sigrid</command-args>"}}
{"type":"user","isMeta":true,"timestamp":"2026-10-25T03:43:12.000Z","message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: ..."}]}}
{"type":"api-request-blob","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>injected</system-reminder>"}]}}
{"type":"assistant","timestamp":"2026-10-25T03:43:16.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret reasoning"}]}}
{"type":"assistant","timestamp":"2026-10-25T03:43:17.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"date","description":"Show the date"}}]}}
{"type":"user","timestamp":"2026-10-25T03:43:18.000Z","message":{"role":"user","content":[{"type":"tool_result","content":"Sun Oct 25 tool output"}]}}
{"type":"assistant","timestamp":"2026-10-25T03:43:19.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x/actions.md"}}]}}
{"type":"assistant","timestamp":"2026-10-25T03:43:20.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Proposed focus: **#3** ` + "`ship`" + `."}]}}
{"type":"user","timestamp":"2026-10-25T03:44:00.000Z","message":{"role":"user","content":"Go with #3 <b>now</b>"}}
{"type":"user","isSidechain":true,"timestamp":"2026-10-25T03:44:01.000Z","message":{"role":"user","content":"subagent prompt"}}
{"type":"user","timestamp":"2026-10-25T03:44:02.000Z","message":{"role":"user","content":"<system-reminder>hook said</system-reminder>"}}
`

func TestChatFromTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, path, transcriptFixture+`{"type":"assistant","timestamp":"2026-10-25T03:44:05.000Z","message":{"role":"ass`) // half-written line
	c := NewChatCache()
	chat, err := c.Chat(path, "Sigrid")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range chat {
		got = append(got, e.Kind+": "+e.Text+strings.Join(e.Tools, "|"))
	}
	want := []string{
		"you: /agents:start sigrid",
		"tools: Bash: Show the date|Read: /x/actions.md",
		"agent: Proposed focus: **#3** `ship`.",
		"you: Go with #3 <b>now</b>",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("chat =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ToolSummary(chat[1].Tools) != "Bash · Read" {
		t.Errorf("summary = %q", ToolSummary(chat[1].Tools))
	}

	// The half-written line completes, and another agent takes over the
	// same transcript: only the appended part is read, and each agent sees
	// its own part.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`istant","content":[{"type":"text","text":"On it."}]}}
{"type":"user","timestamp":"2026-10-25T04:00:00.000Z","message":{"role":"user","content":"<command-name>/agents:start</command-name>\n<command-args>varro</command-args>"}}
{"type":"assistant","timestamp":"2026-10-25T04:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Varro here."}]}}
`)
	f.Close()
	chat, _ = c.Chat(path, "sigrid")
	if last := chat[len(chat)-1]; last.Text != "On it." || len(chat) != 5 {
		t.Errorf("sigrid after append = %+v", chat)
	}
	varro, _ := c.Chat(path, "varro")
	if len(varro) != 2 || varro[1].Text != "Varro here." {
		t.Errorf("varro = %+v", varro)
	}
}

func TestChatMarkdownEscapes(t *testing.T) {
	got := string(chatMarkdown("**Bold** `<x>` <script>alert(1)</script> [site](https://e.example/?a=1&b=2) [bad](javascript:alert(1))\n## Head\n```go\nfmt.Println(\"<hi>\")\n```"))
	for _, want := range []string{
		"<strong>Bold</strong>", "<code>&lt;x&gt;</code>", "&lt;script&gt;",
		`<a href="https://e.example/?a=1&amp;b=2" rel="noreferrer" target="_blank">site</a>`,
		"[bad](javascript:alert(1))", "<strong>Head</strong>", "<pre>fmt.Println(&#34;&lt;hi&gt;&#34;)</pre>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Error("a reply must never add markup")
	}
}

func TestWebAgentPageShowsTheSession(t *testing.T) {
	parent := webFleet(t)
	root := filepath.Join(parent, "alpha")
	pane := func() ([]LivePane, error) {
		return []LivePane{{Name: "sigrid", PaneID: "w1:p7", CWD: root, Agent: "claude", Status: "idle", Running: true}}, nil
	}
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: func(bool) Options { return Options{Now: now, Herdr: pane} }})
	_, body := get(t, h, "/w/alpha/Sigrid")
	if !strings.Contains(body, "No session of Sigrid found") {
		t.Error("live without a transcript: the page must say so")
	}

	// Point the scan at a transcripts directory holding the fixture.
	tr := t.TempDir()
	write(t, filepath.Join(tr, projectDirName(root), "s.jsonl"), transcriptFixture)
	h = NewWebHandler(WebSource{FleetRoot: parent, Options: func(bool) Options { return Options{Now: now, Transcripts: tr, Herdr: pane} }})
	_, body = get(t, h, "/w/alpha/Sigrid")
	for _, want := range []string{`class="msg you"`, "Go with #3 &lt;b&gt;now&lt;/b&gt;", "<strong>#3</strong>", "⚙ Bash · Read"} {
		if !strings.Contains(body, want) {
			t.Errorf("agent page lacks %q", want)
		}
	}
	if strings.Contains(body, "secret reasoning") || strings.Contains(body, "Sun Oct 25 tool output") || strings.Contains(body, "subagent prompt") {
		t.Error("thinking, tool output and sidechains must stay out")
	}

	// Stopped: no chat history, only when the last session ended and its log.
	h = NewWebHandler(WebSource{FleetRoot: parent, Options: func(bool) Options { return Options{Now: now, Transcripts: tr} }})
	_, body = get(t, h, "/w/alpha/Sigrid")
	if strings.Contains(body, `class="msg`) || strings.Contains(body, `id="chat"`) {
		t.Error("a stopped agent must show no chat history")
	}
	for _, want := range []string{"<strong>Not running</strong> · last session ended", "log →", `status-pill dim"><span class="sdot"></span>not running`} {
		if !strings.Contains(body, want) {
			t.Errorf("stopped agent page lacks %q", want)
		}
	}
}
