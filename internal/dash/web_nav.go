package dash

import "path/filepath"

// The app shell around every page, laid out like a chat app: a top bar with
// a workspace picker, a sidebar listing the workspace's agents as
// conversations (or, for all workspaces, the workspaces), and the page.

// nav is the shell's picker and sidebar.
type nav struct {
	// Workspaces fill the picker; empty when only one workspace is served.
	Workspaces []navLink
	// AllHref is the all-workspaces view, "" when only one is served.
	AllHref string
	// Current names what the picker shows: a workspace, or "" for all.
	Current  string
	HomeHref string
	OnHome   bool
	// Items are the sidebar rows: agents, or workspaces on the all view.
	Items []navLink
}

type navLink struct {
	Name, Href string
	// Dot colours the row's status; Badge counts what waits on the
	// principal; Bold marks a session whose turn has ended.
	Dot, State string
	Badge      int
	Bold       bool
	Current    bool
	// Sub is a dim note under the name (an agent's title).
	Sub string
}

// pickerLinks lists every workspace for the picker.
func (s *webServer) pickerLinks(current string, all bool) []navLink {
	if s.src.FleetRoot == "" {
		return nil
	}
	var out []navLink
	for _, dir := range FleetDirs(s.src.FleetRoot) {
		name := filepath.Base(dir)
		out = append(out, navLink{Name: name, Href: link(all, "w", name), Current: name == current})
	}
	return out
}

// workspaceNav is the shell for a workspace's pages; agent is the agent
// shown, "" on the workspace's home.
func (s *webServer) workspaceNav(ws *Workspace, agent string, all bool) *nav {
	n := &nav{Workspaces: s.pickerLinks(ws.Name, all), Current: ws.Name, HomeHref: link(all, "w", ws.Name), OnHome: agent == ""}
	if s.src.FleetRoot != "" {
		n.AllHref = link(all)
	}
	for _, a := range ws.Agents {
		if a.Retired && !all && a.Name != agent {
			continue
		}
		l := navLink{Name: a.Name, Href: link(all, "w", ws.Name, a.Name), Sub: a.Title, Current: a.Name == agent,
			Badge: len(a.ForPrincipal(ws.Principal)) + a.InboxUnprocessed}
		l.Dot, l.State, l.Bold = sessionDot(a)
		n.Items = append(n.Items, l)
	}
	return n
}

// fleetNav is the shell for the all-workspaces view: the sidebar lists the
// workspaces.
func (s *webServer) fleetNav(f *Fleet, all bool) *nav {
	n := &nav{Workspaces: s.pickerLinks("", all), AllHref: link(all), HomeHref: link(all), OnHome: true}
	for _, ws := range f.Workspaces {
		l := navLink{Name: ws.Name, Href: link(all, "w", ws.Name), Badge: ws.Counts().ForYou}
		for _, a := range ws.Agents {
			dot, _, bold := sessionDot(a)
			l.Bold = l.Bold || bold
			if dotRank[dot] > dotRank[l.Dot] {
				l.Dot = dot
			}
		}
		n.Items = append(n.Items, l)
	}
	return n
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
