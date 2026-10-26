package dash

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// teamFixture gives alpha's Sigrid two members, Astrid and Bo, with an
// outcome and three work orders: one working, one past due, one done.
func teamFixture(t *testing.T, ws string) {
	t.Helper()
	member := "---\ntitle: Engineer\nlead: Sigrid\n---\n"
	write(t, filepath.Join(ws, "agents/Astrid/context.md"), member)
	write(t, filepath.Join(ws, "agents/Astrid/actions.md"), sectionedTracker)
	write(t, filepath.Join(ws, "agents/Bo/context.md"), member)
	write(t, filepath.Join(ws, "agents/Bo/actions.md"), sectionedTracker)
	write(t, filepath.Join(ws, "agents/Sigrid/outcomes/billing.md"), `---
outcome: Billing moved to Acme — every invoice from the new system
approved: 2026-09-27 by Norman ("Yes")
status: active
---

## Plan

1. WO-1 Astrid: move the invoices.
2. WO-2 Bo: check the totals.

## Decisions

- 2026-09-28 Norman: "Yes" to the definition.
- 2026-09-28 16:05 Sigrid (L3): split the check out of WO-1.

## Next

- **WO-1 working** since 08:00 — invoices half moved.
- An older entry.
`)
	write(t, filepath.Join(ws, "agents/Astrid/peer/2026-09-29-0800-task-from-Sigrid.md"), `---
from: Sigrid
to: Astrid
type: task
date: 2026-09-29 08:00
status: working
outcome: agents/Sigrid/outcomes/billing.md — Billing moved
task_pane: w1:p9
rounds: 1
timebox: 4h
due: 2026-09-29 12:00
---

## Work order (WO-1): move the invoices

Context.

## Log
- 08:01 Astrid — working. Plan: **one PR**.
- 08:30 Sigrid — Rework (round 1): add the totals.
`)
	write(t, filepath.Join(ws, "agents/Bo/peer/2026-09-28-2300-task-from-Sigrid.md"), `---
from: Sigrid
to: Bo
type: task
date: 2026-09-28 23:00
status: question
outcome: agents/Sigrid/outcomes/billing.md
rounds: 0
timebox: 4h
due: 2026-09-28 23:30
---

## Work order

**WO-2 (billing): check the totals against the old system.**

## Log
- 23:10 Bo — plan: compare March.
- 00:20 Bo — Q1: which month counts?
`)
	write(t, filepath.Join(ws, "agents/Bo/peer/archive/2026-09-20-1000-task-from-Sigrid.md"),
		"---\nfrom: Sigrid\nto: Bo\ntype: task\ndate: 2026-09-20 10:00\nstatus: done\n---\n\n## Work order (WO-0): set up\n\n## Log\n- 10:05 Bo — Hand-back.\n- 10:10 Sigrid — Accepted.\n")
	// A peer request, not a work order: left out.
	write(t, filepath.Join(ws, "agents/Bo/peer/2026-09-21-from-Sigrid.md"), "---\nfrom: Sigrid\nto: Bo\nstatus: answered\n---\n")
}

func TestTeams(t *testing.T) {
	ws := filepath.Join(webFleet(t), "alpha")
	teamFixture(t, ws)
	w, err := Scan(ws, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	teams := Teams(w)
	if len(teams) != 1 || teams[0].Lead.Name != "Sigrid" || len(teams[0].Members) != 2 {
		t.Fatalf("teams = %+v, want Sigrid with two members", teams)
	}
	tm := teams[0]
	if len(tm.Orders) != 3 {
		t.Fatalf("orders = %d, want 3 (the peer request is not one)", len(tm.Orders))
	}
	wo1 := tm.Orders[0]
	if wo1.No != "WO-1" || wo1.Title != "move the invoices" || wo1.Member != "Astrid" || wo1.Outcome != "billing" ||
		wo1.Timebox != 4*time.Hour || wo1.Rounds != 1 || !wo1.InFlight() {
		t.Errorf("WO-1 = %+v", wo1)
	}
	if len(wo1.Log) != 2 || wo1.Log[0].Who != "Astrid" || wo1.Log[0].Text != "working. Plan: one PR." {
		t.Errorf("WO-1 log = %+v", wo1.Log)
	}
	wo2 := tm.Orders[1]
	if wo2.No != "WO-2" || wo2.Title != "check the totals against the old system." {
		t.Errorf("WO-2 number/title from its bold first line = %q %q", wo2.No, wo2.Title)
	}
	// The log ran past midnight: 00:20 is the next day.
	if got := wo2.Log[1].At.Format("01-02 15:04"); got != "09-29 00:20" {
		t.Errorf("past-midnight line at %s, want 09-29 00:20", got)
	}
	o := tm.Outcomes[0]
	if o.Title != "Billing moved to Acme" || o.Next != "WO-1 working since 08:00 — invoices half moved." || len(o.Plan) != 2 {
		t.Errorf("outcome = %+v", o)
	}
	if len(o.Decisions) != 2 || !o.Decisions[0].NoClock || o.Decisions[1].NoClock || o.Decisions[1].Who != "Sigrid" {
		t.Errorf("decisions = %+v", o.Decisions)
	}
}

func TestWebTeam(t *testing.T) {
	parent := webFleet(t)
	teamFixture(t, filepath.Join(parent, "alpha"))
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions})

	// The sidebar lists the team between the workspaces and the agents,
	// with what is in flight; the home has its card.
	_, body := get(t, h, "/w/alpha")
	ti, ai := strings.Index(body, "<h4>Teams</h4>"), strings.Index(body, ">Agents</h4>")
	if ti < 0 || ai < ti || !strings.Contains(body, `href="/t/alpha/Sigrid"`) || !strings.Contains(body, "2 members · WO-1 Astrid, WO-2 Bo") {
		t.Errorf("sidebar team row missing or misplaced:\n%s", body)
	}
	if !strings.Contains(body, `class="acard tcard" href="/t/alpha/Sigrid"`) {
		t.Error("the home lacks the team card")
	}
	// A workspace with no lead has no Teams heading.
	if _, body := get(t, h, "/w/Beta"); strings.Contains(body, "<h4>Teams</h4>") {
		t.Error("Beta has no team, so no Teams section")
	}

	code, body := get(t, h, "/t/alpha/Sigrid")
	if code != http.StatusOK {
		t.Fatalf("team page = %d", code)
	}
	for _, want := range []string{
		"Sigrid team", "watch only",
		`<span class="wo">WO-1</span> move the invoices`, // Astrid's live card
		"Question to lead", "Needs you", "⏱ overdue", // WO-2 is past its due time
		`class="mcard off" href="/w/alpha/Bo"`, // every member has a card; Bo has no pane
		"Billing moved to Acme", "invoices half moved",
		"handed Astrid WO-1: move the invoices", // the feed
		`<span class="fwho" style="--hue:`,
		"Rework (round 1): add the totals.",
		"1 closed outcome", // none: the only outcome is active
	} {
		if want == "1 closed outcome" {
			if strings.Contains(body, want) {
				t.Errorf("no closed outcomes, yet %q shown", want)
			}
			continue
		}
		if !strings.Contains(body, want) {
			t.Errorf("team page lacks %q", want)
		}
	}
	// Watch only: no forms on the page.
	if strings.Contains(body, "<form") {
		t.Error("the team page must not offer forms")
	}
	if !strings.Contains(body, `class="current" href="/t/alpha/Sigrid"`) {
		t.Error("the sidebar must mark the team shown")
	}
	if code, _ := get(t, h, "/t/alpha/Astrid"); code != http.StatusNotFound {
		t.Errorf("a member is not a team: %d", code)
	}
}
