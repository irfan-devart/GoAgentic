package dash

import (
	"hash/fnv"
	"html/template"
	"strings"
	"time"
)

// The agent page's header: an initial tile, the name and title with when
// the session began, the session's state as a pill, and on the right what
// it runs on (the maker's mark and the model) and how full its context is.

type webHead struct {
	Harness, Model, ModelHint string
	Mark                      template.HTML
	// When says when the session started and was last active.
	When string
	// Reviewed is the tracker's "Last reviewed:" day, for the footer.
	Reviewed string
	Ctx      *webCtx
}

// webCtx is how full the context window is, judged against the
// workspace's budget (session health); Tone colours its bar.
type webCtx struct {
	Pct, Budget             int
	Tone, Text, Hint, State string
	// Soon: near the budget; Over: past it, so the session should wrap.
	Soon, Over bool
}

func makeHead(ws *Workspace, a *Agent, u *sessionUsage) webHead {
	h, source := HarnessSource(ws, a)
	where := map[string]string{"agent": "set for this agent", "workspace": "the workspace's default", "default": "the default"}[source]
	head := webHead{Harness: h, ModelHint: h + ", " + where}
	if m, ok := harnessMakers[h]; ok {
		head.Mark, head.ModelHint = m.Mark, h+" by "+m.Company+", "+where
	}
	model := agentModel(ws, a)
	if u != nil && u.Model != "" {
		model = u.Model
	}
	if model != "" {
		head.Model = modelName(model)
		head.ModelHint += "; model " + model
	}
	if u != nil && u.Effort != "" {
		head.Model += " " + u.Effort
		head.ModelHint += ", " + u.Effort + " reasoning"
	}
	if head.Model == "" {
		head.Model = h
	}
	var when []string
	if !a.LastStarted.IsZero() {
		when = append(when, "Started "+shortWhen(ws.Scanned, a.LastStarted))
	}
	if !a.LastActive.IsZero() {
		active := "just now"
		if age := ws.Scanned.Sub(a.LastActive); age >= time.Minute {
			active = humanAge(age) + " ago"
		}
		if len(when) == 0 {
			when = append(when, "Active "+active)
		} else {
			when = append(when, "active "+active)
		}
	}
	head.When = strings.Join(when, ", ")
	if !a.LastReviewed.IsZero() {
		head.Reviewed = shortDay(ws.Scanned, a.LastReviewed)
	}
	head.Ctx = contextHealth(u, ws.ContextBudget)
	return head
}

// shortWhen is a time as briefly as it reads: the clock today, the day
// this week, else the date.
func shortWhen(now, t time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return "today " + t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour && now.After(t):
		return t.Format("Mon 15:04")
	case t.Year() == now.Year():
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
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

// avatarHue picks the agent's tile colour from its name, so it stays the
// same everywhere.
func avatarHue(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32() % 360)
}
