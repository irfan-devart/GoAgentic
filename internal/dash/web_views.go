package dash

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The workspace home's views. The cards are what waits on the principal in
// the trackers, on others, and what is overdue, ranked across agents. Each
// agent's card shows its running session, if any: an agent whose turn has
// ended waits for a reply in its chat.

// nowLine is one running session.
type nowLine struct {
	Agent, Href string
	// State is what the session is doing, in the principal's terms; Class
	// colours it.
	State, Class string
	Working      bool
	// Last is the start of the agent's last message, when its chat can be
	// read.
	Last string
}

// nowLines lists the workspace's running sessions of the principal's own
// (not peer or task panes).
func (s *webServer) nowLines(ws *Workspace, all bool) []nowLine {
	var out []nowLine
	for _, a := range ws.Agents {
		if a.Live == nil || !a.Live.Running || !sessionPane(a.Live) {
			continue
		}
		l := nowLine{Agent: a.Name, Href: link(all, "w", ws.Name, a.Name)}
		switch {
		case a.Live.Status == "blocked":
			l.State, l.Class = "prompt in its pane", "blocked"
		case settled(a.Live):
			l.State, l.Class = "your turn", "foryou"
		case a.Live.Status == "working":
			l.State, l.Class, l.Working = "working", "ok", true
		default:
			l.State, l.Class = a.Live.State(), "dim"
		}
		l.Last = s.lastSaid(ws, a)
		out = append(out, l)
	}
	return out
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
	Agent, AgentHref string
	Item             *webItem
	Text             string
	Overdue          bool
	// Tag says what an item waits on, when not the principal.
	Tag, TagClass string
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
		base := queueLine{Agent: a.Name, AgentHref: link(all, "w", ws.Name, a.Name)}
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

// othersLines lists what waits on someone else, blocked or waiting on
// another owner, overdue first, then by priority.
func othersLines(ws *Workspace, all bool) []queueLine {
	var out []queueLine
	for _, a := range ws.Agents {
		seen := map[string]bool{}
		add := func(it Item, tag, class string) {
			if seen[it.ID] {
				return
			}
			seen[it.ID] = true
			wi := makeWebItem(ws, a, it, all)
			l := queueLine{Agent: a.Name, AgentHref: link(all, "w", ws.Name, a.Name), Item: &wi, Tag: tag, TagClass: class}
			l.Overdue = it.Overdue(ws.Scanned)
			l.pri = min(priorityRank(it.Priority), 4)
			l.rank = l.pri
			if l.Overdue {
				l.rank = 0
			}
			out = append(out, l)
		}
		for _, it := range a.BlockedItems() {
			add(it, "■ blocked", "blocked")
		}
		for _, it := range a.WaitingOnOthers(ws.Principal) {
			tag := "◆ waiting"
			if _, who, ok := it.State(); ok && who != "" {
				tag += " on " + who
			}
			add(it, tag, "waiting")
		}
	}
	return out
}

// overdueLines lists every open item past its due date, whoever it waits
// on; makeQueue puts the longest overdue first.
func overdueLines(ws *Workspace, all bool) []queueLine {
	var out []queueLine
	for _, a := range ws.Agents {
		for _, it := range a.Open {
			if !it.Overdue(ws.Scanned) {
				continue
			}
			wi := makeWebItem(ws, a, it, all)
			out = append(out, queueLine{Agent: a.Name, AgentHref: link(all, "w", ws.Name, a.Name), Item: &wi, Overdue: true})
		}
	}
	return out
}

// queueView is a ranked queue split for the page: the first Top shown, the
// rest folded; a card with its Title, colour (Class) and Empty line.
type queueView struct {
	Top, More           []queueLine
	Total               int
	Title, Class, Empty string
	Key                 string
}

func makeQueue(lines []queueLine, top int) *queueView {
	sortQueue(lines)
	q := &queueView{}
	for _, l := range lines {
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
