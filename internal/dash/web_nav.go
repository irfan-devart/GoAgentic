package dash

import "time"

// The app shell around every page, laid out like a chat app: a sidebar
// listing the workspaces, and under them the chosen workspace's agents as
// conversations, beside the page.

// nav is the shell's sidebar.
type nav struct {
	Workspaces []navLink
	// Current is the chosen workspace; OnHome: its home is shown, not one
	// of its agents.
	Current string
	OnHome  bool
	Agents  []navLink
}

type navLink struct {
	Name, Href string
	// Dot colours the row's session; Badge counts what waits on the
	// principal; Bold marks a session whose turn has ended.
	Dot, State string
	Badge      int
	Bold       bool
	// Sub is a line under the name: an agent's title.
	Sub string
	// Current: the page shown; Selected: the workspace of the agent shown.
	Current, Selected bool
}

// fleetCache holds the workspace list for a while: every page shows it,
// and scanning every workspace on each refresh would be slow.
type fleetCache struct {
	at    time.Time
	all   bool
	links []navLink
}

const fleetCacheFor = 20 * time.Second

// workspaceNav is the shell for a workspace's pages; agent is the agent
// shown, "" on the workspace's home.
func (s *webServer) workspaceNav(ws *Workspace, agent string, all bool) *nav {
	n := &nav{Workspaces: s.workspaceLinks(ws, all), Current: ws.Name, OnHome: agent == ""}
	if agent != "" {
		for i := range n.Workspaces {
			n.Workspaces[i].Selected, n.Workspaces[i].Current = n.Workspaces[i].Current, false
		}
	}
	for _, a := range s.agentsInOrder(ws) {
		if a.Retired && !all && a.Name != agent {
			continue
		}
		l := navLink{Name: a.Name, Href: link(all, "w", ws.Name, a.Name), Sub: a.Title, Current: a.Name == agent,
			Badge: len(a.ForPrincipal(ws.Principal)) + a.InboxUnprocessed}
		l.Dot, l.State, l.Bold = sessionDot(a)
		n.Agents = append(n.Agents, l)
	}
	return n
}

// workspaceLinks lists every workspace served, with its most urgent
// session and what waits on the principal there; the current one from its
// fresh scan.
func (s *webServer) workspaceLinks(current *Workspace, all bool) []navLink {
	var links []navLink
	if s.src.FleetRoot == "" {
		links = []navLink{workspaceLink(current, all)}
	} else {
		s.fleetMu.Lock()
		if c := s.fleet; c.links != nil && c.all == all && time.Since(c.at) < fleetCacheFor {
			links = c.links
		} else {
			s.mu.Lock()
			f, err := ScanFleet(s.src.FleetRoot, s.src.Options(all))
			s.mu.Unlock()
			if err == nil {
				for _, ws := range f.Workspaces {
					links = append(links, workspaceLink(ws, all))
				}
				s.fleet = fleetCache{at: time.Now(), all: all, links: links}
			}
		}
		s.fleetMu.Unlock()
	}
	out := make([]navLink, 0, len(links))
	for _, l := range links {
		if l.Name == current.Name {
			l = workspaceLink(current, all)
			l.Current = true
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		out = append(out, workspaceLink(current, all))
		out[0].Current = true
	}
	return inOrder(out, func(l navLink) string { return l.Name }, s.loadOrder().Workspaces)
}

// agentsInOrder is the workspace's agents in the principal's sidebar order.
func (s *webServer) agentsInOrder(ws *Workspace) []*Agent {
	return inOrder(ws.Agents, func(a *Agent) string { return a.Name }, s.loadOrder().Agents[ws.Name])
}

func workspaceLink(ws *Workspace, all bool) navLink {
	l := navLink{Name: ws.Name, Href: link(all, "w", ws.Name), Badge: ws.Counts().ForYou}
	for _, a := range ws.Agents {
		dot, _, bold := sessionDot(a)
		l.Bold = l.Bold || bold
		if dotRank[dot] > dotRank[l.Dot] {
			l.Dot = dot
		}
	}
	return l
}

// dotRank orders dots by urgency, for a workspace's most urgent agent.
var dotRank = map[string]int{"": 0, "idle": 1, "working": 2, "foryou": 3, "blocked": 4}

// sessionDot is an agent's sidebar dot and its state in words; bold when
// its session waits for the principal's reply.
func sessionDot(a *Agent) (dot, state string, bold bool) {
	switch {
	case a.Live == nil || !a.Live.Running:
		return "", "not running", false
	case !sessionPane(a.Live):
		return "idle", "peer or task pane", false
	case a.Live.Status == "blocked":
		return "blocked", "prompt in its pane", true
	case a.Live.Status == "working":
		return "working", "working", false
	case settled(a.Live):
		return "foryou", "your turn", true
	}
	return "idle", a.Live.State(), false
}
