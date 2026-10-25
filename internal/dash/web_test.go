package dash

import (
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

func TestWebOnlyWriteIsAnAnswer(t *testing.T) {
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
