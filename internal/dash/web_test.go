package dash

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func get(t *testing.T, h http.Handler, path string, header ...string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
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
	req := httptest.NewRequest("GET", "/", nil)
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

func TestWebIsReadOnly(t *testing.T) {
	h := NewWebHandler(WebSource{FleetRoot: webFleet(t), Options: webOptions})
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/w/alpha/Sigrid", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, rec.Code)
		}
	}
}
