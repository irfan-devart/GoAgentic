package dash

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The two views every page leads with. Now is the sessions running in a
// pane, the only time-critical part: an agent whose turn has ended waits for
// a reply in its chat. The queue is what waits on the principal in the
// trackers, ranked across agents (and across workspaces on the fleet page),
// answered at any time.

// nowLine is one running session.
type nowLine struct {
	Agent, Href, Workspace, WsHref string
	// State is what the session is doing, in the principal's terms; Class
	// colours it.
	State, Class string
	Working      bool
	// Last is the start of the agent's last message, when its chat can be
	// read.
	Last  string
	order int
}

// nowLines lists the workspace's running sessions of the principal's own
// (not peer or task panes): its turn ended first, then working.
func (s *webServer) nowLines(ws *Workspace, all bool) []nowLine {
	var out []nowLine
	for _, a := range ws.Agents {
		if a.Live == nil || !a.Live.Running || !sessionPane(a.Live) {
			continue
		}
		l := nowLine{Agent: a.Name, Href: link(all, "w", ws.Name, a.Name), Workspace: ws.Name, WsHref: link(all, "w", ws.Name)}
		switch {
		case a.Live.Status == "blocked":
			l.State, l.Class, l.order = "prompt in its pane", "blocked", 0
		case settled(a.Live):
			l.State, l.Class, l.order = "your turn", "foryou", 1
		case a.Live.Status == "working":
			l.State, l.Class, l.Working, l.order = "working", "ok", true, 2
		default:
			l.State, l.Class, l.order = a.Live.State(), "dim", 3
		}
		l.Last = s.lastSaid(ws, a)
		out = append(out, l)
	}
	return out
}

func sortNow(lines []nowLine) {
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].order < lines[j].order })
}

// lastSaid is the first line of the agent's last message in its running
// session, cut short, or "".
func (s *webServer) lastSaid(ws *Workspace, a *Agent) string {
	path, ok, _ := s.chatPath(ws, a)
	if !ok || path == "" || !fileExists(path) {
		return ""
	}
	entries, _ := s.chat.Chat(path, a.Name)
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != "agent" {
			continue
		}
		for _, line := range strings.Split(entries[i].Text, "\n") {
			if line = strings.TrimSpace(strings.NewReplacer("**", "", "`", "", "#", "").Replace(line)); line != "" {
				return cut(line, 140)
			}
		}
	}
	return ""
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// queueLine is one thing waiting on the principal: an item, or an agent's
// unread scheduled runs (Text).
type queueLine struct {
	Agent, AgentHref, Workspace, WsHref string
	Item                                *webItem
	Text                                string
	Overdue                             bool
	// ShowWs: the fleet page, where a line names its workspace too.
	ShowWs bool
	// rank orders the queue: overdue, P1–P3, no priority, unread runs,
	// answered; pri orders the overdue among themselves.
	rank, pri int
}

// queueLines lists what waits on the principal in the workspace: items by
// priority, overdue first; then unread runs; answered items last, as they
// wait on the agent now.
func queueLines(ws *Workspace, all bool) []queueLine {
	var out []queueLine
	for _, a := range ws.Agents {
		base := queueLine{Agent: a.Name, AgentHref: link(all, "w", ws.Name, a.Name), Workspace: ws.Name, WsHref: link(all, "w", ws.Name)}
		for _, it := range a.ForPrincipal(ws.Principal) {
			wi := makeWebItem(ws, a, it, all)
			l := base
			l.Item, l.Overdue = &wi, it.Overdue(ws.Scanned)
			l.pri = min(priorityRank(it.Priority), 4) // P1–P3, then none
			l.rank = l.pri
			if l.Overdue {
				l.rank = 0
			}
			out = append(out, l)
		}
		if a.InboxUnprocessed > 0 {
			l := base
			l.Text, l.rank = plural(a.InboxUnprocessed, "scheduled run")+" not yet read: start "+a.Name+" to drain its inbox", 5
			out = append(out, l)
		}
		for _, it := range a.AnsweredItems(ws.Principal) {
			wi := makeWebItem(ws, a, it, all)
			l := base
			l.Item, l.rank = &wi, 6
			out = append(out, l)
		}
	}
	return out
}

// sortQueue ranks the queue; a tie keeps the agents' own order.
func sortQueue(lines []queueLine) {
	sort.SliceStable(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.pri != b.pri {
			return a.pri < b.pri
		}
		if a.Item != nil && b.Item != nil && a.Item.Due != nil && b.Item.Due != nil {
			return a.Item.Due.Before(*b.Item.Due)
		}
		return a.Item != nil && a.Item.Due != nil && (b.Item == nil || b.Item.Due == nil)
	})
}

// queueView is a ranked queue split for the page: the first Top shown, the
// rest folded.
type queueView struct {
	Top, More []queueLine
	// Fleet: lines name their workspace too.
	Fleet bool
	Total int
}

func makeQueue(lines []queueLine, top int, fleet bool) *queueView {
	sortQueue(lines)
	q := &queueView{Fleet: fleet}
	for i, l := range lines {
		lines[i].ShowWs = fleet
		if l.rank < 6 {
			q.Total++
		}
	}
	if len(lines) > top {
		q.Top, q.More = lines[:top], lines[top:]
	} else {
		q.Top = lines
	}
	return q
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
