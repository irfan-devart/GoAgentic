package dash

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// webFleet is two workspaces with the sectioned tracker, one with a history
// note for item #5 and a session log.
func webFleet(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	for _, name := range []string{"alpha", "Beta"} {
		ws := filepath.Join(parent, name)
		write(t, filepath.Join(ws, "agents/CONVENTIONS.md"), "---\nprincipal: Norman Noble\n---\n")
		write(t, filepath.Join(ws, "agents/Sigrid/context.md"), "---\ntitle: Lead\n---\n")
		write(t, filepath.Join(ws, "agents/Sigrid/actions.md"), sectionedTracker)
	}
	write(t, filepath.Join(parent, "alpha/agents/Sigrid/items/5-tidy-memory.md"), "# 5 Tidy\n\n2026-09-20 <started>\n")
	write(t, filepath.Join(parent, "alpha/agents/Sigrid/memory/sessions/2026-09-28-ship.md"),
		"---\ndate: 2026-09-28\n---\n\n# Shipped it\n\nDid the thing.\n")
	write(t, filepath.Join(parent, "secret.md"), "outside the agent folder")
	return parent
}

// localReq is a request as the browser sends it through localhost.
func localReq(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "localhost:7777"
	return req
}

func get(t *testing.T, h http.Handler, path string, header ...string) (int, string) {
	t.Helper()
	req := localReq("GET", path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func webOptions(bool) Options { return Options{Now: now} }

func TestWebFleetWorkspaceAgent(t *testing.T) {
	parent := webFleet(t)
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions})

	// The home opens the first workspace, then the one last looked at.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, localReq("GET", "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/w/alpha" {
		t.Errorf("home = %d %q, want the first workspace", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, localReq("GET", "/w/Beta", nil))
	req := localReq("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Location") != "/w/Beta" {
		t.Errorf("home after Beta = %q, want /w/Beta", rec.Header().Get("Location"))
	}

	_, body := get(t, h, "/w/alpha")
	for _, want := range []string{`>Workspaces</h4>`, `class="current" href="/w/alpha"`, `href="/w/Beta"`, `>Agents</h4>`, "<!doctype html>"} {
		if !strings.Contains(body, want) {
			t.Errorf("sidebar lacks %q", want)
		}
	}

	_, body = get(t, h, "/w/alpha")
	for _, want := range []string{"<h1>alpha</h1>", "Waiting on you", `href="/w/alpha/Sigrid"`, "#5", "Tidy memory and links", "» #3 ship the thing"} {
		if !strings.Contains(body, want) {
			t.Errorf("workspace page lacks %q", want)
		}
	}

	_, body = get(t, h, "/w/alpha/Sigrid")
	for _, want := range []string{
		"Sigrid", "Lead", "Next session", "Waiting on you",
		`class="current" href="/w/alpha/Sigrid"`, `<div class="info"`, `<div class="chat-col">`, // the sidebar marks it; info beside the chat
		`Open actions <span class="dim">— P1 1`, "Parked — 1", "Shipped it",
		`href="/w/alpha/Sigrid/file?p=items%2F5-tidy-memory.md"`, // history note link
		"Long detail | with a pipe",                              // the rest of the Action cell
	} {
		if !strings.Contains(body, want) {
			t.Errorf("agent page lacks %q", want)
		}
	}
	// The six sections come from DetailSections, the same as the TUI.
	ws, _ := Scan(filepath.Join(parent, "alpha"), Options{Now: now})
	if sec := DetailSections(ws, ws.Agents[0]); len(sec.Parked) != 1 || len(sec.Open) == 0 {
		t.Errorf("sections = %+v", sec)
	}
}

func TestWebPartialAndAll(t *testing.T) {
	h := NewWebHandler(WebSource{FleetRoot: webFleet(t), Options: webOptions})
	_, body := get(t, h, "/w/alpha", "X-Partial", "1")
	if strings.Contains(body, "<!doctype html>") || !strings.Contains(body, "<h1>alpha</h1>") {
		t.Errorf("partial render must be <main> only:\n%.200s", body)
	}
	_, body = get(t, h, "/w/alpha?all=1")
	if !strings.Contains(body, `href="/w/alpha/Sigrid?all=1"`) {
		t.Error("?all=1 must stay on links")
	}
}

func TestWebFileStaysInTheAgentFolder(t *testing.T) {
	parent := webFleet(t)
	if err := os.Symlink(filepath.Join(parent, "secret.md"), filepath.Join(parent, "alpha/agents/Sigrid/items/link.md")); err != nil {
		t.Fatal(err)
	}
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions})

	code, body := get(t, h, "/w/alpha/Sigrid/file?p=items/5-tidy-memory.md")
	if code != 200 || !strings.Contains(body, "&lt;started&gt;") {
		t.Errorf("history note = %d, escaped body expected:\n%.300s", code, body)
	}
	for _, p := range []string{
		"../../../secret.md", "/etc/passwd", "items/link.md", "actions", "items/../../../../secret.md",
	} {
		if code, _ := get(t, h, "/w/alpha/Sigrid/file?p="+p); code != 404 {
			t.Errorf("file %q = %d, want 404", p, code)
		}
	}
	for _, path := range []string{"/w/nope", "/w/alpha/Nobody", "/w/..%2Fsecret.md"} {
		if code, _ := get(t, h, path); code != 404 {
			t.Errorf("%s = %d, want 404", path, code)
		}
	}
}

func TestWebSingleWorkspace(t *testing.T) {
	parent := webFleet(t)
	h := NewWebHandler(WebSource{Root: filepath.Join(parent, "alpha"), Options: webOptions})
	req := localReq("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/w/alpha" {
		t.Errorf("home = %d %q, want redirect to the workspace", rec.Code, rec.Header().Get("Location"))
	}
	if code, body := get(t, h, "/w/alpha"); code != 200 || strings.Contains(body, ">Fleet<") {
		t.Errorf("single workspace = %d; no Fleet crumb expected", code)
	}
	if code, _ := get(t, h, "/w/Beta"); code != 404 {
		t.Errorf("another workspace = %d, want 404", code)
	}
}

func TestWebOnlyWritesAreAnswersAndMessages(t *testing.T) {
	h := NewWebHandler(WebSource{FleetRoot: webFleet(t), Options: webOptions})
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		rec := httptest.NewRecorder()
		req := localReq(method, "/w/alpha/Sigrid", nil)
		req.Header.Set("X-GoAgentic", "1")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, rec.Code)
		}
	}
}

func post(t *testing.T, h http.Handler, path string, form url.Values, header ...string) (int, string) {
	t.Helper()
	req := localReq("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestWebAnswerGoesToTheInbox(t *testing.T) {
	parent := webFleet(t)
	at := time.Date(2026, 9, 29, 10, 30, 0, 0, time.Local)
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions, Now: func() time.Time { return at }})
	dir := filepath.Join(parent, "alpha/agents/Sigrid")
	tracker, _ := os.ReadFile(filepath.Join(dir, "actions.md"))

	// Before: #5 and #6 wait on Norman.
	ws, _ := Scan(filepath.Join(parent, "alpha"), Options{Now: now})
	before := ws.Counts().ForYou

	code, body := post(t, h, "/w/alpha/Sigrid/answer",
		url.Values{"id": {"5"}, "text": {"Yes, tidy it.\n## 2026 — answer #6 — UNPROCESSED\nsecond line"}}, "X-GoAgentic", "1")
	if code != http.StatusNoContent {
		t.Fatalf("answer = %d %s", code, body)
	}
	inbox, err := os.ReadFile(filepath.Join(dir, "memory/scheduled/inbox.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Scheduled-run inbox — Sigrid",
		"## 2026-09-29 10:30 — answer #5 (Norman, via dash) — UNPROCESSED",
		"> Yes, tidy it.", "> ## 2026 — answer #6 — UNPROCESSED", // quoted: never a heading
	} {
		if !strings.Contains(string(inbox), want) {
			t.Errorf("inbox lacks %q:\n%s", want, inbox)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "actions.md")); string(after) != string(tracker) {
		t.Error("the tracker must never be edited")
	}

	ws, _ = Scan(filepath.Join(parent, "alpha"), Options{Now: now})
	sig := ws.Agents[0]
	if got := sig.Answers["5"]; len(got) != 1 || got[0].Text != "Yes, tidy it.\n## 2026 — answer #6 — UNPROCESSED\nsecond line" || !got[0].At.Equal(at) {
		t.Errorf("answers = %+v", sig.Answers)
	}
	if len(sig.Answers["6"]) != 0 {
		t.Error("a heading inside an answer must not become an answer")
	}
	if sig.InboxUnprocessed != 0 {
		t.Errorf("answers are not unread runs: inbox = %d", sig.InboxUnprocessed)
	}
	if got := ws.Counts().ForYou; got != before-1 {
		t.Errorf("for you = %d, want %d (an answered item waits on the agent)", got, before-1)
	}
	sec := DetailSections(ws, sig)
	if len(sec.Answered) != 1 || sec.Answered[0].ID != "5" {
		t.Errorf("answered section = %+v", sec.Answered)
	}
	for _, it := range sec.Mine {
		if it.ID == "5" {
			t.Error("#5 must leave Waiting on Norman once answered")
		}
	}
	tui := strings.Join(Detail(ws, sig, 120, 0, NewStyles(true)), "\n")
	if !strings.Contains(tui, "#5   Tidy memory and links  ✓ answered, waiting on Sigrid") {
		t.Errorf("TUI detail lacks the answered line:\n%s", tui)
	}

	_, page := get(t, h, "/w/alpha/Sigrid")
	for _, want := range []string{"✓ answered, waiting on Sigrid", "Yes, tidy it.", `action="/w/alpha/Sigrid/answer"`} {
		if !strings.Contains(page, want) {
			t.Errorf("agent page lacks %q", want)
		}
	}

	// A tick processed it: the item waits on Norman again until he answers.
	processed := strings.Replace(string(inbox), "(Norman, via dash) — UNPROCESSED", "(Norman, via dash) — PROCESSED", 1)
	write(t, filepath.Join(dir, "memory/scheduled/inbox.md"), processed)
	ws, _ = Scan(filepath.Join(parent, "alpha"), Options{Now: now})
	if len(ws.Agents[0].Answers) != 0 || ws.Counts().ForYou != before {
		t.Errorf("processed answers must drop out: %+v", ws.Agents[0].Answers)
	}
}

func TestWebAnswerRefusals(t *testing.T) {
	parent := webFleet(t)
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions})
	ok := url.Values{"id": {"5"}, "text": {"yes"}}
	for name, c := range map[string]struct {
		form   url.Values
		header []string
		want   int
	}{
		"no header":          {ok, nil, http.StatusForbidden},
		"cross-site origin":  {ok, []string{"X-GoAgentic", "1", "Origin", "https://evil.example"}, http.StatusForbidden},
		"null origin":        {ok, []string{"X-GoAgentic", "1", "Origin", "null"}, http.StatusForbidden},
		"rebound host":       {ok, []string{"X-GoAgentic", "1", "Host", "evil.example:7777"}, http.StatusForbidden},
		"not waiting on him": {url.Values{"id": {"3"}, "text": {"x"}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
		"no such item":       {url.Values{"id": {"99"}, "text": {"x"}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
		"empty":              {url.Values{"id": {"5"}, "text": {"  "}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
		"too long":           {url.Values{"id": {"5"}, "text": {strings.Repeat("x", 4001)}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
	} {
		req := localReq("POST", "/w/alpha/Sigrid/answer", strings.NewReader(c.form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for i := 0; i+1 < len(c.header); i += 2 {
			if c.header[i] == "Host" {
				req.Host = c.header[i+1]
				continue
			}
			req.Header.Set(c.header[i], c.header[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s = %d, want %d (%s)", name, rec.Code, c.want, rec.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "alpha/agents/Sigrid/memory/scheduled/inbox.md")); err == nil {
		t.Error("a refused answer must write nothing")
	}
	// Same-origin from the tunnel's port is fine.
	if code, _ := post(t, h, "/w/alpha/Sigrid/answer", ok, "X-GoAgentic", "1", "Origin", "http://localhost:7777"); code != http.StatusNoContent {
		t.Errorf("same-origin answer = %d", code)
	}
	// A rebound Host is refused for reads too.
	req := localReq("GET", "/", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("rebound read = %d", rec.Code)
	}
}

func TestInboxIsNotAnOpenSession(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "agents/CONVENTIONS.md"), "---\nprincipal: Norman Noble\n---\n")
	write(t, filepath.Join(root, "agents/Sigrid/context.md"), "---\ntitle: Lead\n---\n")
	write(t, filepath.Join(root, "agents/Sigrid/actions.md"), sectionedTracker)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git: %v %s", err, out)
		}
	}
	write(t, filepath.Join(root, "agents/Sigrid/memory/scheduled/inbox.md"), "# Inbox\n")
	ws, _ := Scan(root, Options{Now: now})
	if n := ws.Agents[0].Uncommitted; n != 0 {
		t.Errorf("uncommitted = %d; an inbox written by a tick or an answer is not a session left open", n)
	}
	write(t, filepath.Join(root, "agents/Sigrid/notes.md"), "x")
	if ws, _ = Scan(root, Options{Now: now}); ws.Agents[0].Uncommitted != 1 {
		t.Errorf("other files still count: %d", ws.Agents[0].Uncommitted)
	}
}

// livePanes makes Sigrid in alpha live in one pane with the given name,
// label and status.
func livePanes(parent, name, label, status string) func(bool) Options {
	return func(bool) Options {
		return Options{Now: now, Herdr: func() ([]LivePane, error) {
			return []LivePane{{Name: name, Label: label, PaneID: "w1:p7", CWD: filepath.Join(parent, "alpha"),
				Agent: "claude", Status: status, Running: status != ""}}, nil
		}}
	}
}

func TestWebSendTypesIntoTheLivePane(t *testing.T) {
	parent := webFleet(t)
	var sent [][2]string
	src := WebSource{FleetRoot: parent, Options: livePanes(parent, "sigrid", "Sigrid - Lead", "idle"),
		Prompt: func(pane, text string) error { sent = append(sent, [2]string{pane, text}); return nil }}
	h := NewWebHandler(src)

	_, body := get(t, h, "/w/alpha/Sigrid")
	if !strings.Contains(body, `action="/w/alpha/Sigrid/send"`) {
		t.Fatalf("a live agent's page lacks the send box:\n%s", body)
	}
	code, msg := post(t, h, "/w/alpha/Sigrid/send", url.Values{"text": {"  Ship it.\nThen wrap.  "}}, "X-GoAgentic", "1")
	if code != http.StatusNoContent {
		t.Fatalf("send = %d %s", code, msg)
	}
	if len(sent) != 1 || sent[0] != [2]string{"w1:p7", "Ship it.\nThen wrap."} {
		t.Errorf("prompted = %q", sent)
	}
	if _, err := os.Stat(filepath.Join(parent, "alpha/agents/Sigrid/memory/scheduled/inbox.md")); err == nil {
		t.Error("a message goes to the pane, not the inbox")
	}
	// An item waiting on Norman takes a reply into the running session,
	// named by its number, not an answer into the inbox.
	if !strings.Contains(body, `class="answer reply" method="post" action="/w/alpha/Sigrid/send"`) || strings.Contains(body, `action="/w/alpha/Sigrid/answer"`) {
		t.Errorf("a running agent's items want Reply, not Answer:\n%s", body)
	}
	if code, msg := post(t, h, "/w/alpha/Sigrid/send", url.Values{"id": {"5"}, "text": {"Yes, tidy it."}}, "X-GoAgentic", "1"); code != http.StatusNoContent || len(sent) != 2 || sent[1][1] != "Re #5: Yes, tidy it." {
		t.Errorf("reply = %d %s, prompted %q", code, msg, sent)
	}
	if _, body := get(t, h, "/w/alpha"); !strings.Contains(body, "your turn") || !strings.Contains(body, `class="current bold" href="/w/alpha"`) {
		t.Errorf("Now and the sidebar lack Sigrid's turn:\n%s", body)
	}

	for name, c := range map[string]struct {
		src    WebSource
		form   url.Values
		header []string
		want   int
	}{
		"no header":   {src, url.Values{"text": {"x"}}, nil, http.StatusForbidden},
		"cross-site":  {src, url.Values{"text": {"x"}}, []string{"X-GoAgentic", "1", "Origin", "https://evil.example"}, http.StatusForbidden},
		"empty":       {src, url.Values{"text": {" \n "}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
		"too long":    {src, url.Values{"text": {strings.Repeat("x", 4001)}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
		"not running": {WebSource{FleetRoot: parent, Options: webOptions, Prompt: src.Prompt}, url.Values{"text": {"x"}}, []string{"X-GoAgentic", "1"}, http.StatusConflict},
		"shell only":  {WebSource{FleetRoot: parent, Options: livePanes(parent, "sigrid", "", ""), Prompt: src.Prompt}, url.Values{"text": {"x"}}, []string{"X-GoAgentic", "1"}, http.StatusConflict},
		"peer pane":   {WebSource{FleetRoot: parent, Options: livePanes(parent, "sigrid-peer", "Sigrid - Peer", "idle"), Prompt: src.Prompt}, url.Values{"text": {"x"}}, []string{"X-GoAgentic", "1"}, http.StatusConflict},
		"task pane":   {WebSource{FleetRoot: parent, Options: livePanes(parent, "", "Sigrid - Task", "working"), Prompt: src.Prompt}, url.Values{"text": {"x"}}, []string{"X-GoAgentic", "1"}, http.StatusConflict},
		"blocked":     {WebSource{FleetRoot: parent, Options: livePanes(parent, "sigrid", "Sigrid - Lead", "blocked"), Prompt: src.Prompt}, url.Values{"text": {"x"}}, []string{"X-GoAgentic", "1"}, http.StatusConflict},
	} {
		if code, msg := post(t, NewWebHandler(c.src), "/w/alpha/Sigrid/send", c.form, c.header...); code != c.want {
			t.Errorf("%s = %d, want %d (%s)", name, code, c.want, msg)
		}
	}
	if len(sent) != 2 {
		t.Errorf("a refused send must type nothing: %q", sent)
	}

	// Not running: no box, and the page says so instead of starting it.
	_, body = get(t, NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions}), "/w/alpha/Sigrid")
	if strings.Contains(body, "/send") || !strings.Contains(body, "<strong>Not running</strong>") {
		t.Errorf("a stopped agent's page must say it is not running, with no send box:\n%s", body)
	}
	// A failed prompt is reported, not swallowed.
	fail := src
	fail.Prompt = func(string, string) error { return os.ErrDeadlineExceeded }
	if code, _ := post(t, NewWebHandler(fail), "/w/alpha/Sigrid/send", url.Values{"text": {"x"}}, "X-GoAgentic", "1"); code != http.StatusBadGateway {
		t.Errorf("failed prompt = %d, want 502", code)
	}
}

// paneOpts serves alpha with Sigrid's pane in the given state (nil: not
// running), reading transcripts from tr.
func paneOpts(parent, tr string, pane **LivePane) func(bool) Options {
	return func(bool) Options {
		return Options{Now: now, Transcripts: tr, Herdr: func() ([]LivePane, error) {
			if *pane == nil {
				return nil, nil
			}
			p := **pane
			p.CWD = filepath.Join(parent, "alpha")
			return []LivePane{p}, nil
		}}
	}
}

func claudePane(status, session string) *LivePane {
	p := &LivePane{Name: "sigrid", PaneID: "w1:p7", Agent: "claude", Status: status, Running: true}
	if session != "" {
		p.Session = &struct {
			Agent string `json:"agent"`
			Value string `json:"value"`
		}{"claude", session}
	}
	return p
}

func TestWebStartShowsLoadingUntilReady(t *testing.T) {
	defer func(orig func(string) (string, error)) { lookPath = orig }(lookPath)
	lookPath = func(cli string) (string, error) { return "/bin/" + cli, nil }
	parent := webFleet(t)
	tr := t.TempDir()
	dir := filepath.Join(tr, projectDirName(filepath.Join(parent, "alpha")))
	// An earlier session that must never show as the new one.
	write(t, filepath.Join(dir, "old.jsonl"), transcriptFixture)

	var pane *LivePane
	release := make(chan struct{})
	var started []string
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: paneOpts(parent, tr, &pane), Launch: func(ws *Workspace, a *Agent) (string, error) {
		started = append(started, ws.Name+"/"+a.Name)
		<-release
		return "started", nil
	}})
	page := func() string { _, body := get(t, h, "/w/alpha/Sigrid"); return body }

	body := page()
	if !strings.Contains(body, `action="/w/alpha/Sigrid/start"`) || strings.Contains(body, `class="msg`) {
		t.Fatalf("stopped: Start and no chat expected:\n%s", body)
	}
	if code, msg := post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusAccepted {
		t.Fatalf("start = %d %s", code, msg)
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusConflict {
		t.Errorf("a second start while one is opening = %d, want 409", code)
	}
	loading := func(stage string) {
		t.Helper()
		body := page()
		if !strings.Contains(body, `class="loading"`) || strings.Contains(body, `class="msg`) || strings.Contains(body, "/start\"") || strings.Contains(body, "/send\"") {
			t.Errorf("%s: want the loading panel only:\n%s", stage, body)
		}
	}
	loading("pane opening")
	// The pane is up, the launch has not returned, no transcript yet.
	pane = claudePane("working", "new")
	loading("CLI starting")
	close(release)
	time.Sleep(20 * time.Millisecond)
	loading("no transcript yet")
	// The session has begun but not replied.
	write(t, filepath.Join(dir, "new.jsonl"), `{"type":"user","message":{"role":"user","content":"<command-name>/agents:start</command-name>\n<command-args>sigrid</command-args>"},"timestamp":"2026-09-29T10:00:00Z"}`+"\n")
	loading("start-up turn")
	// It replied and is idle: the chat is the new session, not the old one.
	f, _ := os.OpenFile(filepath.Join(dir, "new.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Ready. Session focus: #5."}]},"timestamp":"2026-09-29T10:01:00Z"}` + "\n")
	f.Close()
	pane = claudePane("done", "new") // Herdr's word for a finished turn, like idle
	body = page()
	if strings.Contains(body, `class="loading"`) || !strings.Contains(body, "Session focus: #5.") || strings.Contains(body, "Go with #3") || !strings.Contains(body, "/send\"") {
		t.Errorf("ready: want the new session's chat and the send box:\n%s", body)
	}
	if len(started) != 1 {
		t.Errorf("launched %d times", len(started))
	}
	// Ready clears the launch: once it stops again, Start works again.
	pane = nil
	if code, _ := post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusAccepted {
		t.Errorf("start after the agent stopped again = %d", code)
	}
}

func TestWebStartStoppedAtAPromptOrFailed(t *testing.T) {
	defer func(orig func(string) (string, error)) { lookPath = orig }(lookPath)
	lookPath = func(cli string) (string, error) { return "/bin/" + cli, nil }
	parent := webFleet(t)
	var pane *LivePane
	fail := errors.New("herdr agent start: agent sigrid is blocked during startup and is not ready for prompts")
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: paneOpts(parent, t.TempDir(), &pane), Launch: func(*Workspace, *Agent) (string, error) { return "", fail }})
	post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1")
	time.Sleep(20 * time.Millisecond)
	pane = claudePane("blocked", "")
	if _, body := get(t, h, "/w/alpha/Sigrid"); !strings.Contains(body, "waiting on a prompt in its pane") || strings.Contains(body, `class="loading"`) {
		t.Errorf("stopped at a prompt:\n%s", body)
	}

	pane = nil
	fail = errors.New("herdr tab create: no such workspace")
	h = NewWebHandler(WebSource{FleetRoot: parent, Options: paneOpts(parent, t.TempDir(), &pane), Launch: func(*Workspace, *Agent) (string, error) { return "", fail }})
	post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1")
	time.Sleep(20 * time.Millisecond)
	_, body := get(t, h, "/w/alpha/Sigrid")
	if !strings.Contains(body, `class="status error">Start failed: herdr tab create: no such workspace`) || !strings.Contains(body, `action="/w/alpha/Sigrid/start"`) {
		t.Errorf("failed start: error and Start again:\n%s", body)
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusAccepted {
		t.Errorf("start again after a failure = %d", code)
	}

	// Running: no Start, and a forced post is refused; no header refused.
	pane = claudePane("idle", "")
	if code, _ := post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusConflict {
		t.Errorf("start while running = %d", code)
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/start", nil); code != http.StatusForbidden {
		t.Errorf("start without the header = %d", code)
	}
	// No CLI for its harness: no Start, the page says why.
	pane = nil
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if _, body := get(t, NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions}), "/w/alpha/Sigrid"); strings.Contains(body, "/start\"") || !strings.Contains(body, "cannot start in claude: claude not installed") {
		t.Errorf("no harness CLI: page must explain, not offer Start:\n%s", body)
	}
}

func TestLaunchInBackgroundLeavesTheViewAlone(t *testing.T) {
	_, steps := launchPlan(&Workspace{Root: "/ws"}, &Agent{Name: "Varro"}, "w3", true)
	if got := strings.Join(steps[0].Args, " "); got != "tab create --cwd /ws --label Varro --no-focus --workspace w3" {
		t.Errorf("tab = %s", got)
	}
	_, steps = launchPlan(&Workspace{Root: "/ws"}, &Agent{Name: "Varro", Live: &LivePane{PaneID: "w3:p1"}}, "w3", true)
	for _, s := range steps {
		if s.Args[0] == "agent" && s.Args[1] == "focus" {
			t.Error("a background start must not focus the pane")
		}
	}
	roots := map[string]string{"w1": "/other", "w5": "/ws", "w2": "/ws/", "w9": ""}
	if got := spaceOf(roots, "/ws"); got != "w2" {
		t.Errorf("space = %q, want the lowest id of those in /ws", got)
	}
	if got := spaceOf(roots, "/nowhere"); got != "" {
		t.Errorf("space for an unknown root = %q", got)
	}
}

// codexRollout writes a codex session for agent in cwd, begun at start,
// with the given extra lines.
func codexRollout(t *testing.T, home, name, cwd, source string, start time.Time, lines ...string) {
	t.Helper()
	day := time.Now()
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"x","timestamp":%q,"cwd":%q,"thread_source":%q,"base_instructions":{"text":"You are Codex"}}}`,
		start.Format(time.RFC3339), start.Format(time.RFC3339), cwd, source)
	write(t, filepath.Join(home, "sessions", day.Format("2006/01/02"), "rollout-"+name+".jsonl"), meta+"\n"+strings.Join(lines, "\n")+"\n")
}

func codexMsg(at, role, text string) string {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":%q,"content":[{"type":%q,"text":%q}]}}`, at, role, kind, text)
}

func TestWebCodexChat(t *testing.T) {
	parent := webFleet(t)
	root := filepath.Join(parent, "alpha")
	home := t.TempDir()
	at := time.Now().Add(-time.Hour)
	injected := codexMsg("2026-10-25T06:08:03Z", "user", "# AGENTS.md instructions for "+root)
	codexRollout(t, home, "2026-10-25T13-00-00-old", root, "user", at.Add(-time.Hour),
		injected, codexMsg("2026-10-25T05:00:03Z", "user", "$agents-start sigrid"), codexMsg("2026-10-25T05:00:09Z", "assistant", "An older session."))
	codexRollout(t, home, "2026-10-25T14-08-02-now", root, "user", at,
		injected,
		codexMsg("2026-10-25T06:08:03Z", "user", "$agents-start sigrid"),
		codexMsg("2026-10-25T06:08:03Z", "user", "<skill>\n<name>agents-start</name>"),
		`{"timestamp":"2026-10-25T06:08:07Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"text(await tools.exec_command({cmd:\"cat agents/sigrid/soul.md\"}))"}}`,
		`{"timestamp":"2026-10-25T06:08:08Z","type":"response_item","payload":{"type":"reasoning","summary":[{"text":"secret reasoning"}]}}`,
		codexMsg("2026-10-25T06:09:07Z", "assistant", "Loaded, Norman. Focus: **#5**."))
	codexRollout(t, home, "2026-10-25T14-08-12-guardian", root, "guardian_review", at.Add(time.Minute),
		codexMsg("2026-10-25T06:08:13Z", "user", "$agents-start sigrid"), codexMsg("2026-10-25T06:08:15Z", "assistant", `{"risk_level":"low"}`))
	codexRollout(t, home, "2026-10-25T15-00-00-elsewhere", t.TempDir(), "user", at.Add(2*time.Minute),
		codexMsg("2026-10-25T07:00:03Z", "user", "$agents-start sigrid"), codexMsg("2026-10-25T07:00:09Z", "assistant", "Another workspace."))

	pane := &LivePane{Name: "sigrid", PaneID: "w1:p7", Agent: "codex", Status: "idle", Running: true}
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: paneOpts(parent, "", &pane), CodexHome: home})
	_, body := get(t, h, "/w/alpha/Sigrid")
	for _, want := range []string{"$agents-start sigrid", "Loaded, Norman. Focus: <strong>#5</strong>.", "⚙ exec", "cat agents/sigrid/soul.md", `action="/w/alpha/Sigrid/send"`} {
		if !strings.Contains(body, want) {
			t.Errorf("codex chat lacks %q", want)
		}
	}
	for _, not := range []string{"An older session.", "risk_level", "Another workspace.", "AGENTS.md instructions", "agents-start</name>", "secret reasoning", "Claude Code sessions only"} {
		if strings.Contains(body, not) {
			t.Errorf("codex chat must not show %q", not)
		}
	}

	// Started from this page: a session begun before the Start is an
	// earlier one, so it loads until the new session replies.
	defer func(orig func(string) (string, error)) { lookPath = orig }(lookPath)
	lookPath = func(cli string) (string, error) { return "/bin/" + cli, nil }
	var stopped *LivePane
	h = NewWebHandler(WebSource{FleetRoot: parent, Options: paneOpts(parent, "", &stopped), CodexHome: home,
		Launch: func(*Workspace, *Agent) (string, error) { return "started", nil }})
	post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1")
	time.Sleep(20 * time.Millisecond)
	stopped = &LivePane{Name: "sigrid", PaneID: "w1:p8", Agent: "codex", Status: "working", Running: true}
	if _, body := get(t, h, "/w/alpha/Sigrid"); !strings.Contains(body, `class="loading"`) || strings.Contains(body, "Loaded, Norman") {
		t.Errorf("starting: the earlier codex session must not show:\n%s", body)
	}
	codexRollout(t, home, "2026-10-25T16-00-00-new", root, "user", time.Now(),
		codexMsg("2026-10-25T08:00:03Z", "user", "$agents-start sigrid"))
	if _, body := get(t, h, "/w/alpha/Sigrid"); !strings.Contains(body, `class="loading"`) {
		t.Error("the new session has not replied yet: still loading")
	}
	f, _ := os.OpenFile(filepath.Join(home, "sessions", time.Now().Format("2006/01/02"), "rollout-2026-10-25T16-00-00-new.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(codexMsg("2026-10-25T08:00:30Z", "assistant", "Fresh start.") + "\n")
	f.Close()
	stopped.Status = "idle"
	if _, body := get(t, h, "/w/alpha/Sigrid"); strings.Contains(body, `class="loading"`) || !strings.Contains(body, "Fresh start.") || strings.Contains(body, "Loaded, Norman") {
		t.Errorf("ready: want the new codex session only:\n%s", body)
	}
}

func TestWebOtherHarnessSaysSo(t *testing.T) {
	parent := webFleet(t)
	pane := &LivePane{Name: "sigrid", PaneID: "w1:p7", Agent: "pi", Status: "idle", Running: true}
	_, body := get(t, NewWebHandler(WebSource{FleetRoot: parent, Options: paneOpts(parent, "", &pane), CodexHome: t.TempDir()}), "/w/alpha/Sigrid")
	if !strings.Contains(body, "Sigrid runs in pi; the chat reads Claude Code and codex sessions only.") {
		t.Errorf("chat note for a pi agent:\n%s", body)
	}
}

func TestWebWrapAndClose(t *testing.T) {
	parent := webFleet(t)
	tr := t.TempDir()
	write(t, filepath.Join(tr, projectDirName(filepath.Join(parent, "alpha")), "s1.jsonl"), transcriptFixture)
	pane := claudePane("idle", "s1")
	var typed []string
	var closed []string
	src := WebSource{FleetRoot: parent, Options: paneOpts(parent, tr, &pane),
		Prompt:    func(p, text string) error { typed = append(typed, p+" "+text); return nil },
		ClosePane: func(p string) error { closed = append(closed, p); return nil }}
	h := NewWebHandler(src)
	page := func() string { _, body := get(t, h, "/w/alpha/Sigrid"); return body }

	if body := page(); !strings.Contains(body, `action="/w/alpha/Sigrid/close"`) || !strings.Contains(body, "Wrap &amp; close") {
		t.Fatalf("a live session lacks Wrap & close:\n%s", body)
	}
	// Busy: the button is disabled and a forced post refused.
	pane = claudePane("working", "s1")
	if body := page(); !strings.Contains(body, `class="quiet" disabled title="Sigrid is busy`) {
		t.Errorf("busy agent: Wrap & close must be disabled:\n%s", body)
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/close", nil, "X-GoAgentic", "1"); code != http.StatusConflict || len(typed) != 0 {
		t.Errorf("wrap while busy = %d, typed %q", code, typed)
	}
	pane = claudePane("idle", "s1")
	if body := page(); strings.Contains(body, "disabled>Wrap") {
		t.Error("an idle agent's Wrap & close must be enabled")
	}
	if code, msg := post(t, h, "/w/alpha/Sigrid/close", nil, "X-GoAgentic", "1"); code != http.StatusAccepted {
		t.Fatalf("close = %d %s", code, msg)
	}
	if len(typed) != 1 || typed[0] != "w1:p7 /agents:wrap" {
		t.Errorf("typed = %q", typed)
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/close", nil, "X-GoAgentic", "1"); code != http.StatusConflict {
		t.Errorf("a second close while wrapping = %d", code)
	}
	pane = claudePane("working", "s1")
	if body := page(); !strings.Contains(body, "Wrapping up Sigrid…") || strings.Contains(body, "/send\"") || len(closed) != 0 {
		t.Errorf("wrapping: banner, no send box, pane open:\n%s", body)
	}
	// It wrote its session log and settled: the pane closes.
	write(t, filepath.Join(parent, "alpha/agents/Sigrid/memory/sessions/2026-10-25-wrapped.md"), "---\ndate: 2026-10-25\n---\n")
	pane = claudePane("idle", "s1")
	if body := page(); len(closed) != 1 || closed[0] != "w1:p7" || !strings.Contains(body, "<strong>Not running</strong>") {
		t.Errorf("wrapped: pane closed and not running; closed = %q\n%s", closed, body)
	}

	// Stopped without a log (it asked something): no close, Close anyway.
	pane = claudePane("idle", "s1")
	post(t, h, "/w/alpha/Sigrid/close", nil, "X-GoAgentic", "1")
	pane = claudePane("working", "s1")
	page()
	pane = claudePane("idle", "s1")
	body := page()
	if len(closed) != 1 || !strings.Contains(body, "stopped before its wrap finished") || !strings.Contains(body, "Close anyway") || !strings.Contains(body, "data-confirm=") {
		t.Errorf("stopped: pane stays, Close anyway offered; closed = %q\n%s", closed, body)
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/close", url.Values{"force": {"1"}}, "X-GoAgentic", "1"); code != http.StatusNoContent || len(closed) != 2 {
		t.Errorf("close anyway = %d, closed = %q", code, closed)
	}

	// Codex types its own wrap command; nothing running or a peer pane: refused.
	pane = &LivePane{Name: "sigrid", PaneID: "w1:p9", Agent: "codex", Status: "idle", Running: true}
	post(t, NewWebHandler(src), "/w/alpha/Sigrid/close", nil, "X-GoAgentic", "1")
	if typed[len(typed)-1] != "w1:p9 $agents-wrap" {
		t.Errorf("codex wrap typed = %q", typed[len(typed)-1])
	}
	for name, p := range map[string]*LivePane{"stopped": nil, "peer": {Name: "sigrid-peer", PaneID: "w1:p3", Agent: "claude", Status: "idle", Running: true}} {
		pane = p
		if code, _ := post(t, NewWebHandler(src), "/w/alpha/Sigrid/close", nil, "X-GoAgentic", "1"); code != http.StatusConflict {
			t.Errorf("%s: close = %d, want 409", name, code)
		}
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/close", nil); code != http.StatusForbidden {
		t.Errorf("close without the header = %d", code)
	}
}

func TestQueueRanksAcrossAgents(t *testing.T) {
	day := func(d int) *time.Time { t := now.AddDate(0, 0, d); return &t }
	line := func(name, pri string, due *time.Time, rank int) queueLine {
		it := webItem{Item: Item{ID: name, Priority: pri, Due: due}}
		l := queueLine{Agent: name, Item: &it, pri: min(priorityRank(pri), 4)}
		l.rank = l.pri
		if rank >= 0 {
			l.rank = rank
		}
		return l
	}
	q := makeQueue([]queueLine{
		line("answered", "P1", nil, 6),
		line("p3", "P3", nil, -1),
		line("p1-later", "P1", day(5), -1),
		line("late-p2", "P2", day(-3), 0),
		line("none", "", nil, -1),
		line("p1-soon", "P1", day(1), -1),
		line("late-p1", "P1", day(-1), 0),
	}, 3)
	var got []string
	for _, l := range append(q.Top, q.More...) {
		got = append(got, l.Agent)
	}
	want := "late-p1 late-p2 p1-soon p1-later p3 none answered"
	if strings.Join(got, " ") != want || len(q.Top) != 3 || q.Total != 6 {
		t.Errorf("queue = %v (top %d, total %d), want %s", got, len(q.Top), q.Total, want)
	}
}

func TestWebSidebarReorder(t *testing.T) {
	parent := webFleet(t)
	write(t, filepath.Join(parent, "alpha/agents/Astrid/context.md"), "---\ntitle: Partner\n---\n")
	write(t, filepath.Join(parent, "alpha/agents/Astrid/actions.md"), sectionedTracker)
	order := filepath.Join(t.TempDir(), "order.json")
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions, OrderFile: order})
	before := func(body, a, b string) bool {
		i, j := strings.Index(body, `data-name="`+a+`"`), strings.Index(body, `data-name="`+b+`"`)
		return i >= 0 && j >= 0 && i < j
	}
	if _, body := get(t, h, "/w/alpha"); !before(body, "alpha", "Beta") || !before(body, "Astrid", "Sigrid") {
		t.Fatalf("default order wanted first:\n%s", body)
	}
	for _, f := range []url.Values{
		{"kind": {"workspaces"}, "name": {"Beta", "alpha"}},
		{"kind": {"agents"}, "ws": {"alpha"}, "name": {"Sigrid", "Astrid"}},
	} {
		if code, msg := post(t, h, "/order", f, "X-GoAgentic", "1"); code != http.StatusNoContent {
			t.Fatalf("reorder %v = %d %s", f, code, msg)
		}
	}
	_, body := get(t, h, "/w/alpha")
	if !before(body, "Beta", "alpha") || !before(body, "Sigrid", "Astrid") {
		t.Errorf("saved order not shown:\n%s", body)
	}
	// The home's agent table follows it too.
	if i, j := strings.Index(body, `<td><a href="/w/alpha/Sigrid">`), strings.Index(body, `<td><a href="/w/alpha/Astrid">`); i < 0 || j < i {
		t.Error("agent table must follow the sidebar order")
	}
	for name, c := range map[string]struct {
		form   url.Values
		header []string
		want   int
	}{
		"no header": {url.Values{"kind": {"workspaces"}, "name": {"alpha"}}, nil, http.StatusForbidden},
		"bad kind":  {url.Values{"kind": {"x"}, "name": {"alpha"}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
		"no ws":     {url.Values{"kind": {"agents"}, "name": {"alpha"}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
		"no names":  {url.Values{"kind": {"workspaces"}}, []string{"X-GoAgentic", "1"}, http.StatusBadRequest},
	} {
		if code, _ := post(t, h, "/order", c.form, c.header...); code != c.want {
			t.Errorf("%s = %d, want %d", name, code, c.want)
		}
	}
}
