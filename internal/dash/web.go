package dash

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The web dashboard: the same picture as the TUI, served as HTML. It reads
// through the same scan and the same section logic, so the two agree. It is
// read-only: every route is a GET and nothing is written.

//go:embed web/page.tmpl web/style.css
var webFiles embed.FS

var webTemplate = template.Must(template.New("page.tmpl").Funcs(template.FuncMap{
	"trimHash": func(id string) string { return strings.TrimPrefix(id, "#") },
}).ParseFS(webFiles, "web/page.tmpl"))

// WebSource tells the server what to show. With FleetRoot set it serves
// every workspace under it; otherwise only the workspace at Root.
type WebSource struct {
	FleetRoot string
	Root      string
	// Options builds the scan options; all includes retired agents.
	Options func(all bool) Options
}

// Serve runs the web dashboard on addr until ctx ends.
func Serve(ctx context.Context, addr string, src WebSource, log io.Writer) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: NewWebHandler(src), ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(log, "Dashboard on http://%s/ — Ctrl-C to stop.\n", ln.Addr())
	if !loopback(ln.Addr()) {
		fmt.Fprintln(log, "Not a loopback address: anyone who can reach it can read every tracker shown.")
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

type webServer struct {
	src WebSource
	// mu keeps scans one at a time: each asks Herdr and git, and an
	// auto-refreshing tab or two must not pile them up.
	mu sync.Mutex
}

// NewWebHandler returns the dashboard's routes.
func NewWebHandler(src WebSource) http.Handler {
	s := &webServer{src: src}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /w/{ws}", s.workspace)
	mux.HandleFunc("GET /w/{ws}/{agent}", s.agent)
	mux.HandleFunc("GET /w/{ws}/{agent}/file", s.file)
	mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		data, _ := webFiles.ReadFile("web/style.css")
		_, _ = w.Write(data)
	})
	return mux
}

// page is everything the template needs; one of Fleet, Workspace, Agent or
// File is set.
type page struct {
	Title   string
	Heading string
	Crumbs  []crumb
	All     bool
	Scanned time.Time
	Live    bool
	Counts  *Counts
	First   string

	Fleet     []fleetRow
	Workspace *webWorkspace
	Agent     *webAgent
	File      *webFile
}

type crumb struct{ Name, Href string }

type fleetRow struct {
	Name, Href, Tone, Live, NeedsYou string
	Counts                           Counts
}

type webWorkspace struct {
	Waiting []waitingLine
	Rows    []agentRow
}

type waitingLine struct {
	Agent, AgentHref string
	// First marks the agent's first line, the only one that names it.
	First bool
	Item  *webItem
	// Text is set instead of Item for unread scheduled runs.
	Text string
}

type agentRow struct {
	Row
	Href, Tone, LiveClass string
	NextP1                bool
}

type webAgent struct {
	Name, Title string
	Facts       []string
	Plan        []PlanStep
	Mine        []webItem
	Inbox       int
	Others      []webItem
	Blocked     []webItem
	Open        []webItem
	OpenCounts  map[string]int
	Parked      []webItem
	LastSession *Session
	SessionHref string
	Health      Health
	HealthLine  string
}

type webItem struct {
	Item
	Headline, Detail, Note, History string
}

type webFile struct {
	Name, Text string
}

func (s *webServer) all(r *http.Request) bool { return r.URL.Query().Get("all") == "1" }

// link keeps ?all=1 on every link once it is set.
func link(all bool, parts ...string) string {
	p := "/" + strings.Join(parts, "/")
	if all {
		p += "?all=1"
	}
	return p
}

func (s *webServer) home(w http.ResponseWriter, r *http.Request) {
	all := s.all(r)
	if s.src.FleetRoot == "" {
		http.Redirect(w, r, link(all, "w", filepath.Base(s.src.Root)), http.StatusFound)
		return
	}
	s.mu.Lock()
	f, err := ScanFleet(s.src.FleetRoot, s.src.Options(all))
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c := f.Counts()
	p := page{Title: "Fleet", Heading: "FLEET", All: all, Scanned: f.Scanned, Live: f.Live, Counts: &c}
	for _, ws := range f.Workspaces {
		live := "–"
		if ws.Live {
			live = fmt.Sprint(ws.Counts().Live)
		}
		p.Fleet = append(p.Fleet, fleetRow{
			Name: ws.Name, Href: link(all, "w", ws.Name), Tone: toneClass(workspaceTone(ws)),
			Live: live, NeedsYou: NeedsYou(ws), Counts: ws.Counts(),
		})
	}
	s.render(w, r, p)
}

// scanWorkspace finds a workspace by its directory name and scans it.
func (s *webServer) scanWorkspace(name string, all bool) (*Workspace, error) {
	root := ""
	if s.src.FleetRoot == "" {
		if name == filepath.Base(s.src.Root) {
			root = s.src.Root
		}
	} else {
		for _, dir := range FleetDirs(s.src.FleetRoot) {
			if filepath.Base(dir) == name {
				root = dir
			}
		}
	}
	if root == "" {
		return nil, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Scan(root, s.src.Options(all))
}

var errNotFound = errors.New("not found")

func (s *webServer) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		http.NotFound(w, nil)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (s *webServer) crumbs(all bool, ws string, more ...crumb) []crumb {
	var out []crumb
	if s.src.FleetRoot != "" {
		out = append(out, crumb{"Fleet", link(all)})
	}
	out = append(out, crumb{ws, link(all, "w", ws)})
	return append(out, more...)
}

func (s *webServer) workspace(w http.ResponseWriter, r *http.Request) {
	all := s.all(r)
	ws, err := s.scanWorkspace(r.PathValue("ws"), all)
	if err != nil {
		s.fail(w, err)
		return
	}
	c := ws.Counts()
	p := page{
		Title: ws.Name, Heading: strings.ToUpper(ws.Name), Crumbs: s.crumbs(all, ws.Name), All: all, Scanned: ws.Scanned, Live: ws.Live,
		Counts: &c, First: firstName(ws.Principal), Workspace: &webWorkspace{},
	}
	for _, a := range ws.Agents {
		href := link(all, "w", ws.Name, a.Name)
		start := len(p.Workspace.Waiting)
		for _, it := range a.ForPrincipal(ws.Principal) {
			wi := makeWebItem(ws, a, it, all)
			p.Workspace.Waiting = append(p.Workspace.Waiting, waitingLine{Agent: a.Name, AgentHref: href, Item: &wi})
		}
		if a.InboxUnprocessed > 0 {
			p.Workspace.Waiting = append(p.Workspace.Waiting, waitingLine{Agent: a.Name, AgentHref: href,
				Text: fmt.Sprintf("%d scheduled run(s) not yet read", a.InboxUnprocessed)})
		}
		if len(p.Workspace.Waiting) > start {
			p.Workspace.Waiting[start].First = true
		}
		row := MakeRow(ws, a)
		p.Workspace.Rows = append(p.Workspace.Rows, agentRow{
			Row: row, Href: href, Tone: toneClass(agentTone(ws, a)), LiveClass: liveClass(row.Live),
			NextP1: strings.HasPrefix(row.Next, "P1 "),
		})
	}
	s.render(w, r, p)
}

func (s *webServer) findAgent(w http.ResponseWriter, r *http.Request) (*Workspace, *Agent, bool) {
	ws, err := s.scanWorkspace(r.PathValue("ws"), true)
	if err != nil {
		s.fail(w, err)
		return nil, nil, false
	}
	for _, a := range ws.Agents {
		if a.Name == r.PathValue("agent") {
			return ws, a, true
		}
	}
	http.NotFound(w, r)
	return nil, nil, false
}

func (s *webServer) agent(w http.ResponseWriter, r *http.Request) {
	all := s.all(r)
	ws, a, ok := s.findAgent(w, r)
	if !ok {
		return
	}
	sec := DetailSections(ws, a)
	items := func(list []Item) []webItem {
		out := make([]webItem, len(list))
		for i, it := range list {
			out[i] = makeWebItem(ws, a, it, all)
		}
		return out
	}
	wa := &webAgent{
		Name: a.Name, Title: a.Title, Facts: detailFacts(ws, a),
		Plan: sec.Plan, Mine: items(sec.Mine), Inbox: sec.Inbox,
		Others: items(sec.Others), Blocked: items(sec.Blocked), Open: items(sec.Open),
		OpenCounts: sec.OpenCounts, Parked: items(sec.Parked), Health: a.Health,
		HealthLine: fmt.Sprintf("standing %d/%d · sessions %d/%d · actions %d KB",
			len(a.Standing), maxStanding, len(a.Sessions), maxSessions, a.ActionsSize/1024),
	}
	if last, ok := a.LastSession(); ok {
		wa.LastSession = &last
		wa.SessionHref = fileLink(ws, a, filepath.Join("memory", "sessions", last.File), all)
	}
	c := ws.Counts()
	s.render(w, r, page{
		Title: a.Name + " · " + ws.Name, Heading: strings.ToUpper(ws.Name), Crumbs: s.crumbs(all, ws.Name, crumb{a.Name, link(all, "w", ws.Name, a.Name)}),
		All: all, Scanned: ws.Scanned, Live: ws.Live, Counts: &c, First: firstName(ws.Principal), Agent: wa,
	})
}

// file shows one Markdown file from the agent's own folder: an item's
// history note or a session log.
func (s *webServer) file(w http.ResponseWriter, r *http.Request) {
	all := s.all(r)
	ws, a, ok := s.findAgent(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("p")
	path, ok := agentFile(a.Dir, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, page{
		Title: rel + " · " + a.Name,
		Crumbs: s.crumbs(all, ws.Name, crumb{a.Name, link(all, "w", ws.Name, a.Name)},
			crumb{rel, ""}),
		All: all, Scanned: ws.Scanned, Live: ws.Live, File: &webFile{Name: rel, Text: string(data)},
	})
}

// agentFile resolves rel inside the agent's folder: a .md file, no way out
// of the folder by .. or a symlink.
func agentFile(dir, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) || filepath.Ext(rel) != ".md" {
		return "", false
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	path, err := filepath.EvalSymlinks(filepath.Join(dir, clean))
	if err != nil || !strings.HasPrefix(path, base+string(filepath.Separator)) {
		return "", false
	}
	return path, true
}

func fileLink(ws *Workspace, a *Agent, rel string, all bool) string {
	q := "?p=" + template.URLQueryEscaper(filepath.ToSlash(rel))
	if all {
		q += "&all=1"
	}
	return "/w/" + ws.Name + "/" + a.Name + "/file" + q
}

// makeWebItem adds what the page shows beside the item: the rest of the
// Action cell, a parked note, and a link to its history note if one exists.
func makeWebItem(ws *Workspace, a *Agent, it Item, all bool) webItem {
	wi := webItem{Item: it, Headline: it.Headline()}
	if detail := strings.TrimSpace(strings.TrimPrefix(cleanMarkdown(it.Action), wi.Headline)); detail != "" && detail != wi.Headline {
		wi.Detail = strings.TrimSpace(strings.TrimLeft(detail, "—–-.: "))
	}
	if it.Parked() {
		wi.Note = parkedNote(it.Status)
	}
	if note := itemNote(a.Dir, it.ID); note != "" {
		wi.History = fileLink(ws, a, note, all)
	}
	return wi
}

// itemNote finds items/<id>-<slug>.md (or items/<id>.md) for an item.
func itemNote(dir, id string) string {
	id = strings.TrimPrefix(id, "#")
	if id == "" {
		return ""
	}
	if fileExists(filepath.Join(dir, "items", id+".md")) {
		return filepath.Join("items", id+".md")
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "items", id+"-*.md")); len(m) > 0 {
		return filepath.Join("items", filepath.Base(m[0]))
	}
	return ""
}

func toneClass(t tone) string {
	return [...]string{"off", "idle", "working", "foryou", "waiting", "blocked"}[t]
}

func liveClass(live string) string {
	switch live {
	case "working", "blocked":
		return "ok"
	case "idle", "running":
		return "accent"
	}
	return "dim"
}

// render writes the page, or only its <main> when the page's own refresh
// asks (X-Partial), so the view updates without a reload.
func (s *webServer) render(w http.ResponseWriter, r *http.Request, p page) {
	name := "page"
	if r.Header.Get("X-Partial") == "1" {
		name = "main"
	}
	var buf bytes.Buffer
	if err := webTemplate.ExecuteTemplate(&buf, name, p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}
