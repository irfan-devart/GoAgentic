package dash

import (
	"hash/fnv"
	"html/template"
	"time"
)

// The agent page's header: an initial tile, the name and title, the
// session's state as a pill, and the facts as label and value pairs.

type webFact struct {
	Label, Value, Hint string
	// Mark is a small logo before the value: the harness's maker.
	Mark template.HTML
}

// webFacts are detailFacts for the page: short values, with the detail
// that would crowd the header in Hint (shown on hover).
func webFacts(ws *Workspace, a *Agent) []webFact {
	h, source := HarnessSource(ws, a)
	where := map[string]string{"agent": "set for this agent", "workspace": "the workspace's default", "default": "the default"}[source]
	harness := webFact{Label: "Harness", Value: h, Hint: h + " is " + where}
	if m, ok := harnessMakers[h]; ok {
		harness.Mark, harness.Hint = m.Mark, h+" by "+m.Company+", "+where
	}
	facts := []webFact{harness}
	if m := agentModel(ws, a); m != "" {
		facts = append(facts, webFact{Label: "Model", Value: m})
	}
	if !a.LastStarted.IsZero() {
		facts = append(facts, webFact{Label: "Started", Value: shortWhen(ws.Scanned, a.LastStarted), Hint: a.LastStarted.Local().Format("Mon 02 Jan 2006 15:04")})
	}
	if !a.LastActive.IsZero() {
		v := "just now"
		if age := ws.Scanned.Sub(a.LastActive); age >= time.Minute {
			v = humanAge(age) + " ago"
		}
		facts = append(facts, webFact{Label: "Active", Value: v, Hint: a.LastActive.Local().Format("Mon 02 Jan 2006 15:04")})
	}
	if !a.LastReviewed.IsZero() {
		facts = append(facts, webFact{Label: "Tracker reviewed", Value: shortDay(ws.Scanned, a.LastReviewed), Hint: a.LastReviewed.Format("2006-01-02")})
	}
	return facts
}

// shortWhen is a time as briefly as it reads: the clock today, the day
// this week, else the date.
func shortWhen(now, t time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		if t.Hour() == 0 && t.Minute() == 0 {
			return "today"
		}
		return "today " + t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour && now.After(t):
		return t.Format("Mon 15:04")
	case t.Year() == now.Year():
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

// avatarHue picks the agent's tile colour from its name, so it stays the
// same everywhere.
func avatarHue(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32() % 360)
}

// shortDay is a date with no time of day (a "Last reviewed:" line): today,
// the day this week, else the date.
func shortDay(now, day time.Time) string {
	now = now.Local()
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	switch days := int(today.Sub(d).Hours() / 24); {
	case days == 0:
		return "today"
	case days == 1:
		return "yesterday"
	case days > 1 && days < 6:
		return d.Format("Monday")
	case d.Year() == now.Year():
		return d.Format("2 Jan")
	}
	return d.Format("2 Jan 2006")
}
