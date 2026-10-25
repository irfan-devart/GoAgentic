package dash

import (
	"errors"
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

	code, body := get(t, h, "/")
	if code != 200 {
		t.Fatalf("fleet = %d", code)
	}
	for _, want := range []string{"FLEET", `href="/w/alpha"`, `href="/w/Beta"`, "Sigrid 2", "<!doctype html>"} {
		if !strings.Contains(body, want) {
			t.Errorf("fleet page lacks %q", want)
		}
	}

	_, body = get(t, h, "/w/alpha")
	for _, want := range []string{"ALPHA", "Waiting on Norman", `href="/w/alpha/Sigrid"`, "#5", "Tidy memory and links", "» #3 ship the thing"} {
		if !strings.Contains(body, want) {
			t.Errorf("workspace page lacks %q", want)
		}
	}

	_, body = get(t, h, "/w/alpha/Sigrid")
	for _, want := range []string{
		"Sigrid", "Lead", "Next session (agent's own plan)", "Waiting on Norman",
		"Open actions — P1 1", "Parked — 1", "Shipped it",
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
	if strings.Contains(body, "<!doctype html>") || !strings.Contains(body, "ALPHA") {
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
	if len(sent) != 1 {
		t.Errorf("a refused send must type nothing: %q", sent)
	}

	// Not running: no box, and the page says so instead of starting it.
	_, body = get(t, NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions}), "/w/alpha/Sigrid")
	if strings.Contains(body, "/send") || !strings.Contains(body, "Sigrid is not running.") {
		t.Errorf("a stopped agent's page must say it is not running, with no send box:\n%s", body)
	}
	// A failed prompt is reported, not swallowed.
	fail := src
	fail.Prompt = func(string, string) error { return os.ErrDeadlineExceeded }
	if code, _ := post(t, NewWebHandler(fail), "/w/alpha/Sigrid/send", url.Values{"text": {"x"}}, "X-GoAgentic", "1"); code != http.StatusBadGateway {
		t.Errorf("failed prompt = %d, want 502", code)
	}
}

func TestWebStartOpensAStoppedAgent(t *testing.T) {
	defer func(orig func(string) (string, error)) { lookPath = orig }(lookPath)
	lookPath = func(cli string) (string, error) { return "/bin/" + cli, nil }
	parent := webFleet(t)
	var started []string
	launch := func(ws *Workspace, a *Agent) (string, error) {
		started = append(started, ws.Name+"/"+a.Name)
		return "started", nil
	}
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions, Launch: launch})

	_, body := get(t, h, "/w/alpha/Sigrid")
	if !strings.Contains(body, `action="/w/alpha/Sigrid/start"`) || !strings.Contains(body, "Start Sigrid") {
		t.Fatalf("a stopped agent's page lacks Start:\n%s", body)
	}
	if code, msg := post(t, h, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusNoContent {
		t.Fatalf("start = %d %s", code, msg)
	}
	if len(started) != 1 || started[0] != "alpha/Sigrid" {
		t.Errorf("launched = %q", started)
	}
	if code, _ := post(t, h, "/w/alpha/Sigrid/start", nil); code != http.StatusForbidden {
		t.Errorf("start without the header = %d", code)
	}

	// Running: no Start, and a forced post is refused.
	live := NewWebHandler(WebSource{FleetRoot: parent, Options: livePanes(parent, "sigrid", "Sigrid - Lead", "idle"), Launch: launch})
	if _, body := get(t, live, "/w/alpha/Sigrid"); strings.Contains(body, "/start") {
		t.Error("a running agent must not offer Start")
	}
	if code, _ := post(t, live, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusConflict {
		t.Errorf("start while running = %d", code)
	}
	// A leftover shell pane is not running: Start reuses it (LaunchPlan).
	shell := NewWebHandler(WebSource{FleetRoot: parent, Options: livePanes(parent, "sigrid", "", ""), Launch: launch})
	if code, _ := post(t, shell, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusNoContent {
		t.Errorf("start from a shell pane = %d", code)
	}

	// A start that stops at a prompt says where to answer it.
	stuck := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions, Launch: func(*Workspace, *Agent) (string, error) {
		return "", errors.New("herdr agent start: agent sigrid is blocked during startup and is not ready for prompts")
	}})
	if code, msg := post(t, stuck, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusBadGateway || !strings.Contains(msg, "answer it there") {
		t.Errorf("stuck start = %d %s", code, msg)
	}

	// A second click while the first is still opening is refused.
	entered, release := make(chan struct{}), make(chan struct{})
	slow := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions, Launch: func(*Workspace, *Agent) (string, error) {
		close(entered)
		<-release
		return "started", nil
	}})
	done := make(chan int)
	go func() { code, _ := post(t, slow, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); done <- code }()
	<-entered
	if code, _ := post(t, slow, "/w/alpha/Sigrid/start", nil, "X-GoAgentic", "1"); code != http.StatusConflict {
		t.Errorf("a second start while one is opening = %d, want 409", code)
	}
	close(release)
	if code := <-done; code != http.StatusNoContent {
		t.Errorf("first start = %d", code)
	}

	// No CLI for its harness: no Start, the page says why.
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if _, body := get(t, h, "/w/alpha/Sigrid"); strings.Contains(body, "/start") || !strings.Contains(body, "cannot start in claude: claude not installed") {
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

func TestWebChatNoteNamesAnotherHarness(t *testing.T) {
	parent := webFleet(t)
	write(t, filepath.Join(parent, "alpha/agents/CONVENTIONS.md"), "---\nprincipal: Norman Noble\nharness: codex\n---\n")
	_, body := get(t, NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions}), "/w/alpha/Sigrid")
	if !strings.Contains(body, "Sigrid runs in codex; the chat shows Claude Code sessions only.") {
		t.Errorf("chat note for a codex agent:\n%s", body)
	}
}
