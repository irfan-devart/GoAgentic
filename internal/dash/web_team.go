package dash

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The team page: a lead and its members at work, watch only. Who is live
// and on what, the work orders in their lanes, the lead's outcomes, and
// every log line as one feed. It writes nothing: the lead answers its
// members, and what needs the principal reaches them through the lead.

type webTeam struct {
	Lead, LeadTitle, LeadHref string
	Initial                   string
	Hue                       int
	// Members counts the lead's members; Live is the lead and every member
	// with a running pane or an order in flight, Idle the rest.
	Members int
	Live    []teamMember
	Idle    []teamMember
	// NeedsYou is what the principal should see: an order waiting on them,
	// past its due time, or at the round limit.
	NeedsYou []*webOrder
	Lanes    []teamLane
	Outcomes []webOutcome
	Closed   []webOutcome
	Feed     []feedDay
	Counts   teamCounts
}

type teamCounts struct {
	Working, Questions, Review, DoneToday, NeedsYou int
}

type teamMember struct {
	Name, Title, Href string
	Initial           string
	Hue               int
	Lead              bool
	State, Class      string
	Activity          *activity
	Order             *webOrder
	Last              *LogLine
}

type webOrder struct {
	WorkOrder
	Href, OutcomeTitle string
	// Lane is the board column it sits in; Flag says why it needs the
	// principal, if it does.
	Lane, Flag string
	// Used is the share of its timebox gone, in percent; Elapsed in words.
	Used    int
	Elapsed string
	Last    *LogLine
	Stopped bool
}

type teamLane struct {
	Title, Class, Empty string
	Orders              []*webOrder
	Note                string
}

type webOutcome struct {
	Outcome
	Href   string
	Orders []*webOrder
	Done   int
}

type feedDay struct {
	Day   string
	Lines []feedLine
}

type feedLine struct {
	At        time.Time
	Who, Text string
	Hue       int
	Lead      bool
	// No and Href name the work order or outcome the line is from.
	No, Href string
	Kind     string
	NoClock  bool
}

// roundLimit is the rounds after which an order goes to the principal
// (reference/task.md § 6).
const roundLimit = 5

// feedMax is how many lines the activity feed shows.
const feedMax = 80

func (s *webServer) team(w http.ResponseWriter, r *http.Request) {
	all := s.all(r)
	ws, err := s.scanWorkspace(r.PathValue("ws"), true)
	if err != nil {
		s.fail(w, err)
		return
	}
	t := TeamOf(ws, r.PathValue("lead"))
	if t == nil {
		http.NotFound(w, r)
		return
	}
	wt := s.makeTeam(ws, t, all)
	n := s.workspaceNav(ws, "", all)
	n.OnHome = false
	for i := range n.Workspaces {
		n.Workspaces[i].Selected, n.Workspaces[i].Current = n.Workspaces[i].Current, false
	}
	for i := range n.Teams {
		n.Teams[i].Current = strings.EqualFold(n.Teams[i].Name, t.Lead.Name)
	}
	s.render(w, r, page{
		Title: t.Lead.Name + " team · " + ws.Name,
		All:   all, Scanned: ws.Scanned, Live: ws.Live, First: firstName(ws.Principal), Team: wt, Nav: n,
	})
}

func (s *webServer) makeTeam(ws *Workspace, t *Team, all bool) *webTeam {
	now := ws.Scanned
	wt := &webTeam{
		Lead: t.Lead.Name, LeadTitle: t.Lead.Title, LeadHref: link(all, "w", ws.Name, t.Lead.Name),
		Initial: strings.ToUpper(t.Lead.Name[:1]), Hue: avatarHue(t.Lead.Name), Members: len(t.Members),
	}
	titles := map[string]string{}
	for _, o := range t.Outcomes {
		titles[o.File] = o.Title
	}
	members := map[string]*Agent{}
	for _, m := range t.Members {
		members[m.Name] = m
	}
	orders := make([]*webOrder, len(t.Orders))
	for i, o := range t.Orders {
		wo := &webOrder{WorkOrder: o, OutcomeTitle: titles[o.Outcome], Stopped: o.Status == "stopped"}
		if m := members[o.Member]; m != nil {
			wo.Href = fileLink(ws, m, o.Path, all)
		}
		if n := len(o.Log); n > 0 {
			last := latest(o.Log)
			wo.Last = &last
		}
		wo.Lane = laneOf(o.Status)
		if o.InFlight() {
			d := now.Sub(o.Start)
			wo.Elapsed = roundDur(d)
			if o.Timebox > 0 {
				wo.Used = min(100, int(100*d/o.Timebox))
			}
			switch {
			case o.Status == "needs-principal":
				wo.Flag = "needs you"
			case !o.Due.IsZero() && now.After(o.Due):
				wo.Flag = "⏱ overdue"
			case o.Rounds >= roundLimit:
				wo.Flag = fmt.Sprintf("%d rounds", o.Rounds)
			}
			if wo.Flag != "" {
				wt.NeedsYou = append(wt.NeedsYou, wo)
			}
		}
		orders[i] = wo
	}

	// The board: what is in flight by lane, and what finished today.
	lanes := []teamLane{
		{Title: "Working", Class: "working", Empty: "Nobody is on a work order."},
		{Title: "Question to lead", Class: "waiting", Empty: "No open questions."},
		{Title: "Review", Class: "review", Empty: "Nothing handed back."},
		{Title: "Done today", Class: "done", Empty: "Nothing finished today."},
	}
	today := dayOf(now)
	week := 0
	for _, wo := range orders {
		switch {
		case wo.InFlight():
			for i := range lanes {
				if lanes[i].Title == wo.Lane {
					lanes[i].Orders = append(lanes[i].Orders, wo)
				}
			}
		case dayOf(wo.LastAt()).Equal(today):
			lanes[3].Orders = append(lanes[3].Orders, wo)
		case now.Sub(wo.LastAt()) < 7*24*time.Hour:
			week++
		}
	}
	if week > 0 {
		lanes[3].Note = fmt.Sprintf("%d more done in the last 7 days", week)
	}
	wt.Lanes = lanes
	wt.Counts = teamCounts{Working: len(lanes[0].Orders), Questions: len(lanes[1].Orders), Review: len(lanes[2].Orders),
		DoneToday: len(lanes[3].Orders), NeedsYou: len(wt.NeedsYou)}

	// Who is live: the lead in its own session, each member in its task
	// pane (or its own session), with the order it is on.
	lead := teamMember{Name: t.Lead.Name, Title: t.Lead.Title, Href: wt.LeadHref, Lead: true,
		Initial: wt.Initial, Hue: wt.Hue}
	lead.State, lead.Class = paneState(t.Lead.Live)
	if lead.State == "working" {
		lead.Activity = s.paneActivity(t.Lead.Live)
	}
	lead.Last = lastSaid(orders, t.Lead.Name)
	wt.Live = append(wt.Live, lead)
	for _, m := range t.Members {
		tm := teamMember{Name: m.Name, Title: m.Title, Href: link(all, "w", ws.Name, m.Name),
			Initial: strings.ToUpper(m.Name[:1]), Hue: avatarHue(m.Name)}
		for _, wo := range orders {
			if wo.Member == m.Name && wo.InFlight() {
				tm.Order = wo
				break
			}
		}
		pane := taskPane(ws, m)
		if pane == nil && m.Live != nil && m.Live.Running {
			pane = m.Live
		}
		tm.State, tm.Class = paneState(pane)
		if tm.State == "working" {
			tm.Activity = s.paneActivity(pane)
		}
		tm.Last = lastSaid(orders, m.Name)
		if tm.Order != nil || (pane != nil && pane.Running) {
			wt.Live = append(wt.Live, tm)
		} else {
			wt.Idle = append(wt.Idle, tm)
		}
	}

	// Outcomes: the active ones with their orders, then the closed.
	for _, o := range t.Outcomes {
		wo := webOutcome{Outcome: o, Href: fileLink(ws, t.Lead, filepath.Join("outcomes", o.File+".md"), all)}
		for _, ord := range orders {
			if ord.Outcome != o.File {
				continue
			}
			if ord.InFlight() {
				wo.Orders = append(wo.Orders, ord)
			} else {
				wo.Done++
			}
		}
		if o.Status == "done" || o.Status == "closed" {
			wt.Closed = append(wt.Closed, wo)
		} else {
			wt.Outcomes = append(wt.Outcomes, wo)
		}
	}

	wt.Feed = teamFeed(ws, t, orders, wt.Outcomes, all)
	return wt
}

// teamFeed merges every log line, every new order and every outcome
// decision into one timeline, newest first, grouped by day.
func teamFeed(ws *Workspace, t *Team, orders []*webOrder, outcomes []webOutcome, all bool) []feedDay {
	known := map[string]bool{strings.ToLower(t.Lead.Name): true, strings.ToLower(firstName(ws.Principal)): true}
	for _, m := range t.Members {
		known[strings.ToLower(m.Name)] = true
	}
	speaker := func(who string) string {
		if known[strings.ToLower(who)] {
			return who
		}
		return ""
	}
	var lines []feedLine
	for _, o := range orders {
		if !o.Start.IsZero() {
			lines = append(lines, feedLine{At: o.Start, Who: t.Lead.Name, Lead: true, Kind: "issued",
				Text: "handed " + o.Member + " " + orDefault(o.No, "a work order") + ": " + o.Title, No: o.No, Href: o.Href})
		}
		for _, l := range o.Log {
			lines = append(lines, feedLine{At: l.At, Who: speaker(l.Who), Text: clip(l.Text, 240), No: o.No, Href: o.Href})
		}
	}
	for _, o := range outcomes {
		for _, d := range o.Decisions {
			lines = append(lines, feedLine{At: d.At, Who: speaker(d.Who), Text: clip(d.Text, 240), No: "Outcome", Href: o.Href, Kind: "decision", NoClock: d.NoClock})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].At.After(lines[j].At) })
	if len(lines) > feedMax {
		lines = lines[:feedMax]
	}
	var days []feedDay
	now := ws.Scanned
	for _, l := range lines {
		if l.Who != "" {
			l.Hue = avatarHue(l.Who)
			l.Lead = strings.EqualFold(l.Who, t.Lead.Name)
		}
		day := dayName(l.At, now)
		if len(days) == 0 || days[len(days)-1].Day != day {
			days = append(days, feedDay{Day: day})
		}
		days[len(days)-1].Lines = append(days[len(days)-1].Lines, l)
	}
	return days
}

// teamLinks are the sidebar's team rows for a workspace.
func teamLinks(ws *Workspace, all bool) []navLink {
	var out []navLink
	for _, t := range Teams(ws) {
		l := navLink{Name: t.Lead.Name, Href: link(all, "t", ws.Name, t.Lead.Name)}
		dot, _, _ := sessionDot(t.Lead)
		var on []string
		for _, m := range t.Members {
			if p := taskPane(ws, m); p != nil && p.Running && p.Status == "working" {
				dot = "working"
			}
		}
		for _, o := range t.Orders {
			if !o.InFlight() {
				continue
			}
			on = append(on, orDefault(o.No, "WO")+" "+o.Member)
			if o.Status == "needs-principal" || (!o.Due.IsZero() && ws.Scanned.After(o.Due)) || o.Rounds >= roundLimit {
				l.Badge++
			}
		}
		l.Dot = dot
		l.Sub = plural(len(t.Members), "member")
		if len(on) > 0 {
			l.Sub += " · " + strings.Join(on, ", ")
		} else {
			l.Sub += " · quiet"
		}
		out = append(out, l)
	}
	return out
}

// teamCard is a team's line on the workspace home.
type teamCard struct {
	Lead, Title, Href, Dot string
	Members                int
	Counts                 teamCounts
	// On is what is in flight, one line per order.
	On []string
}

func teamCards(ws *Workspace, all bool) []teamCard {
	var out []teamCard
	links := teamLinks(ws, all)
	for i, t := range Teams(ws) {
		c := teamCard{Lead: t.Lead.Name, Title: t.Lead.Title, Href: links[i].Href, Dot: links[i].Dot, Members: len(t.Members)}
		today := dayOf(ws.Scanned)
		for _, o := range t.Orders {
			if o.InFlight() {
				c.On = append(c.On, o.Member+" · "+orDefault(o.No, "WO")+" "+clip(o.Title, 70)+" ("+o.Status+")")
			}
			switch {
			case o.Status == "question" || o.Status == "needs-principal":
				c.Counts.Questions++
			case o.Status == "review":
				c.Counts.Review++
			case o.InFlight():
				c.Counts.Working++
			case dayOf(o.LastAt()).Equal(today):
				c.Counts.DoneToday++
			}
		}
		c.Counts.NeedsYou = links[i].Badge
		out = append(out, c)
	}
	return out
}

// taskPane is the member's /agents:delegate pane ("balbus-task",
// "Balbus - Task") under the workspace, or nil.
func taskPane(ws *Workspace, m *Agent) *LivePane {
	root := filepath.Clean(ws.Root)
	name := strings.ToLower(m.Name)
	var found *LivePane
	for i := range ws.Panes {
		p := &ws.Panes[i]
		cwd := filepath.Clean(p.CWD)
		if cwd != root && !strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(p.Label))
		if strings.ToLower(p.Name) != name+"-task" && label != name+" - task" {
			continue
		}
		if found == nil || (p.Running && !found.Running) {
			found = p
		}
	}
	return found
}

// paneState is a team member's one word on its pane.
func paneState(p *LivePane) (string, string) {
	switch {
	case p == nil || !p.Running:
		return "not running", "dim"
	case p.Status == "working":
		return "working", "ok"
	case p.Status == "blocked":
		return "prompt in its pane", "blocked"
	}
	return "idle", "dim"
}

// paneActivity is a working pane's status line.
func (s *webServer) paneActivity(p *LivePane) *activity {
	if p == nil {
		return nil
	}
	return s.activityOf(&Agent{Live: p})
}

func laneOf(status string) string {
	switch status {
	case "question", "needs-principal":
		return "Question to lead"
	case "review":
		return "Review"
	case "done", "stopped":
		return "Done today"
	}
	return "Working"
}

// latest is the newest line of a log (logs are not always in order).
func latest(log []LogLine) LogLine {
	l := log[0]
	for _, x := range log[1:] {
		if !x.At.Before(l.At) {
			l = x
		}
	}
	return l
}

// lastSaid is the newest log line written by who, across the orders.
func lastSaid(orders []*webOrder, who string) *LogLine {
	var best *LogLine
	for _, o := range orders {
		for i := range o.Log {
			l := &o.Log[i]
			if strings.EqualFold(l.Who, who) && (best == nil || l.At.After(best.At)) {
				best = l
			}
		}
	}
	return best
}

func dayOf(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// dayName heads a day of the feed: Today, Yesterday, or "Sat 24 Oct".
func dayName(t, now time.Time) string {
	switch dayOf(now).Sub(dayOf(t)) {
	case 0:
		return "Today"
	case 24 * time.Hour:
		return "Yesterday"
	}
	return t.Local().Format("Mon 02 Jan")
}

// roundDur says a duration in hours and minutes: "47m", "2h 05m".
func roundDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	m := int(d.Minutes())
	if m >= 60 && m%60 == 0 {
		return fmt.Sprintf("%dh", m/60)
	}
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %02dm", m/60, m%60)
}
