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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The web dashboard: the same picture as the TUI, served as HTML. It reads
// through the same scan and the same section logic, so the two agree. It
// writes two things, never a tracker: an answer to an item that waits on the
// principal, appended to the agent's inbox (answers.go), and a message typed
// into an agent's live session pane through Herdr (send).

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
	// ListenHost is the address the server listens on when it is not a
	// loopback one; requests must name it or a loopback host (see guard).
	ListenHost string
	// Now stamps answers; nil means time.Now.
	Now func() time.Time
	// Prompt types text into a live pane and submits it; nil means
	// `herdr agent prompt`.
	Prompt func(pane, text string) error
}

// Serve runs the web dashboard on addr until ctx ends.
func Serve(ctx context.Context, addr string, src WebSource, log io.Writer) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if !loopback(ln.Addr()) {
		src.ListenHost = ln.Addr().(*net.TCPAddr).IP.String()
	}
	srv := &http.Server{Handler: NewWebHandler(src), ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(log, "Dashboard on http://%s/ — Ctrl-C to stop.\n", ln.Addr())
	if src.ListenHost != "" {
		fmt.Fprintln(log, "Not a loopback address: anyone who can reach it can read every tracker shown, answer as you, and type into your agents' sessions.")
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
	mu   sync.Mutex
	chat *ChatCache
}

// NewWebHandler returns the dashboard's routes.
func NewWebHandler(src WebSource) http.Handler {
	s := &webServer{src: src, chat: NewChatCache()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /w/{ws}", s.workspace)
	mux.HandleFunc("GET /w/{ws}/{agent}", s.agent)
	mux.HandleFunc("GET /w/{ws}/{agent}/file", s.file)
	mux.HandleFunc("POST /w/{ws}/{agent}/answer", s.answer)
	mux.HandleFunc("POST /w/{ws}/{agent}/send", s.send)
	mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		data, _ := webFiles.ReadFile("web/style.css")
		_, _ = w.Write(data)
	})
	return s.guard(mux)
}

// guard turns away requests a browser was tricked into sending. The Host
// must be a loopback name or the address we listen on (against DNS
// rebinding). A write must carry the X-GoAgentic header, which a page on
// another site cannot add without a CORS preflight this server never
// grants, and any Origin must pass the same host check.
func (s *webServer) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			http.Error(w, "unexpected Host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			host := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
			if r.Header.Get("X-GoAgentic") != "1" || (origin != "" && (host == origin || !s.allowedHost(host))) {
				http.Error(w, "cross-site write refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *webServer) allowedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || (s.src.ListenHost != "" && host == s.src.ListenHost) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	Answered    []webItem
	Inbox       int
	Others      []webItem
	Blocked     []webItem
	Open        []webItem
	OpenCounts  map[string]int
	Parked      []webItem
	LastSession *Session
	// Chat is the latest session as a conversation; ChatLive says it is
	// still running in a pane.
	Chat     []webChat
	ChatLive bool
	ChatNote string
	// CanSend: the agent's own session is running in a pane, so the page
	// offers a send box posting to SendURL. SendNote says why not.
	CanSend     bool
	SendURL     string
	SendNote    string
	SessionHref string
	Health      Health
	HealthLine  string
}

type webChat struct {
	ChatEntry
	HTML    template.HTML
	Summary string
}

type webItem struct {
	Item
	Headline, Detail, Note, History string
	// Key names the item uniquely on a page (agent/id), so open panels
	// survive a refresh.
	Key string
	// CanAnswer: the item waits on the principal, so the page offers an
	// answer box posting to AnswerURL. Answers are the ones not yet picked
	// up by the agent.
	CanAnswer bool
	AnswerURL string
	Answers   []Answer
	Agent     string
	AgentLive bool
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
		for _, it := range append(a.ForPrincipal(ws.Principal), a.AnsweredItems(ws.Principal)...) {
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
		Plan: sec.Plan, Mine: items(sec.Mine), Answered: items(sec.Answered), Inbox: sec.Inbox,
		Others: items(sec.Others), Blocked: items(sec.Blocked), Open: items(sec.Open),
		OpenCounts: sec.OpenCounts, Parked: items(sec.Parked), Health: a.Health,
		HealthLine: fmt.Sprintf("standing %d/%d · sessions %d/%d · actions %d KB",
			len(a.Standing), maxStanding, len(a.Sessions), maxSessions, a.ActionsSize/1024),
	}
	wa.ChatLive = a.Live != nil && a.Live.Running
	if wa.CanSend, wa.SendNote = canSend(a); wa.CanSend {
		wa.SendURL = "/w/" + ws.Name + "/" + a.Name + "/send"
	}
	switch {
	case a.Transcript == "":
		wa.ChatNote = "No Claude Code session of " + a.Name + " found in this workspace's transcripts."
	default:
		entries, err := s.chat.Chat(a.Transcript, a.Name)
		if err != nil {
			wa.ChatNote = "Could not read the session: " + err.Error()
		}
		for _, e := range entries {
			wc := webChat{ChatEntry: e}
			switch e.Kind {
			case "agent":
				wc.HTML = chatMarkdown(e.Text)
			case "tools":
				wc.Summary = ToolSummary(e.Tools)
			}
			wa.Chat = append(wa.Chat, wc)
		}
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

// answer appends the principal's answer to an item to the agent's inbox.
// The page refreshes itself afterwards, so the reply is only a status.
func (s *webServer) answer(w http.ResponseWriter, r *http.Request) {
	ws, a, ok := s.findAgent(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.FormValue("id"), "#")
	var item *Item
	for i := range a.Open {
		if strings.TrimPrefix(a.Open[i].ID, "#") == id {
			item = &a.Open[i]
		}
	}
	if item == nil || !waitsOnPrincipal(*item, ws.Principal) {
		http.Error(w, "#"+id+" is not an open item waiting on "+firstName(ws.Principal), http.StatusBadRequest)
		return
	}
	now := time.Now
	if s.src.Now != nil {
		now = s.src.Now
	}
	s.mu.Lock()
	err := AppendAnswer(a, ws.Principal, id, r.FormValue("text"), now())
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// send types the principal's message into the agent's live session pane and
// submits it, as if typed there. It never starts an agent, and never writes
// to a peer or task pane.
func (s *webServer) send(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.findAgent(w, r)
	if !ok {
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	switch {
	case text == "":
		http.Error(w, "nothing to send", http.StatusBadRequest)
		return
	case len(text) > maxAnswer:
		http.Error(w, fmt.Sprintf("message longer than %d characters", maxAnswer), http.StatusBadRequest)
		return
	}
	if ok, why := canSend(a); !ok {
		http.Error(w, why, http.StatusConflict)
		return
	}
	prompt := s.src.Prompt
	if prompt == nil {
		prompt = herdrPrompt
	}
	if err := prompt(a.Live.PaneID, text); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// canSend says whether the page may type into the agent's pane: only its
// own session, running, and not waiting on a prompt in the pane. Otherwise
// the reason, for the page.
func canSend(a *Agent) (bool, string) {
	switch {
	case a.Live == nil || !a.Live.Running:
		return false, a.Name + " is not running. Open it from the dashboard (TUI) or Herdr to talk to it."
	case !sessionPane(a.Live):
		return false, a.Name + " is open only as a peer or task pane; those answer their caller, not you."
	case a.Live.Status == "blocked":
		return false, a.Name + " is waiting on a prompt in its pane. Answer it there."
	}
	return true, ""
}

// sessionPane is false for the panes /agents:ask and /agents:delegate open
// ("sigrid-peer", "Sigrid - Task"), which match the agent by label too.
func sessionPane(p *LivePane) bool {
	name, label := strings.ToLower(p.Name), strings.ToLower(strings.TrimSpace(p.Label))
	for _, kind := range []string{"peer", "task"} {
		if strings.HasSuffix(name, "-"+kind) || strings.HasSuffix(label, " - "+kind) {
			return false
		}
	}
	return true
}

// herdrPrompt submits text to a pane with `herdr agent prompt`. Herdr writes
// text and Enter; it cannot always confirm the agent took it, so the page
// shows the message once the transcript has it.
func herdrPrompt(pane, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, herdrBin(), "agent", "prompt", pane, text).CombinedOutput()
	if err != nil {
		return fmt.Errorf("herdr agent prompt: %s", herdrError(out, err))
	}
	return nil
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
	id := strings.TrimPrefix(it.ID, "#")
	wi := webItem{
		Item: it, Headline: it.Headline(), Key: a.Name + "/" + id,
		CanAnswer: waitsOnPrincipal(it, ws.Principal), Answers: a.Answers[id],
		AnswerURL: "/w/" + ws.Name + "/" + a.Name + "/answer",
		Agent:     a.Name, AgentLive: a.Live != nil && a.Live.Running,
	}
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
