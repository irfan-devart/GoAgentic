package dash

// Sections sorts an agent's open items into the six detail sections, the
// same way for every front-end (the TUI's detail panel and the web page), so
// the two never disagree about where an item belongs.
type Sections struct {
	Plan []PlanStep
	// Mine waits on the principal; Inbox counts unread scheduled runs.
	Mine  []Item
	Inbox int
	// Others waits on someone else, Blocked is stuck, both by priority.
	Others  []Item
	Blocked []Item
	// Open is everything else, by priority, under way before not started.
	Open       []Item
	OpenCounts map[string]int
	Parked     []Item
}

// DetailSections places each open item in exactly one section: waiting on
// the principal, waiting on someone else, blocked, parked, or else open.
func DetailSections(ws *Workspace, a *Agent) Sections {
	s := Sections{Plan: a.PlanSteps(), Inbox: a.InboxUnprocessed, OpenCounts: map[string]int{}}
	shownElsewhere := map[string]bool{}
	for _, it := range a.ForPrincipal(ws.Principal) {
		shownElsewhere[it.ID] = true
		s.Mine = append(s.Mine, it)
	}
	for _, it := range byPriority(a.WaitingOnOthers(ws.Principal)) {
		if shownElsewhere[it.ID] {
			continue
		}
		shownElsewhere[it.ID] = true
		s.Others = append(s.Others, it)
	}
	for _, it := range byPriority(a.BlockedItems()) {
		shownElsewhere[it.ID] = true
		s.Blocked = append(s.Blocked, it)
	}

	var live []Item
	for _, it := range a.Open {
		switch {
		case it.Parked():
			s.Parked = append(s.Parked, it)
		case !shownElsewhere[it.ID]:
			live = append(live, it)
		}
	}
	for _, it := range live {
		s.OpenCounts[it.Priority]++
	}
	var ordered []Item
	for _, notYet := range []bool{false, true} {
		for _, it := range live {
			if it.NotStarted() == notYet {
				ordered = append(ordered, it)
			}
		}
	}
	s.Open = byPriority(ordered)
	s.Parked = byPriority(s.Parked)
	return s
}

// byPriority orders items P1, P2, P3, then anything else, keeping the
// tracker's order within each.
func byPriority(items []Item) []Item {
	var out []Item
	for _, p := range []string{"P1", "P2", "P3", ""} {
		for _, it := range items {
			if it.Priority == p || (p == "" && priorityRank(it.Priority) > 3) {
				out = append(out, it)
			}
		}
	}
	return out
}
