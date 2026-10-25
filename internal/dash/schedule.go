package dash

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Scheduled tasks come from the workspace's task register
// (agents/scheduled-tasks.md), the file the scheduler's gate.py reads. Each
// task names its agent; the dashboard shows an agent its own tasks, when the
// last one ran and when the next is due, and whether a run was missed.

type ScheduledTask struct {
	ID string
	// Cron is the five-field schedule; When says it in words.
	Cron, When string
	Enabled    bool
	// LastRun is zero when the task has never run.
	LastRun time.Time
	// Next is the next time it is due; Missed: a due time passed with no
	// run after it.
	Next   time.Time
	Missed time.Time
}

// missGrace allows for the hourly tick (at :07) and a slow run before a
// due time counts as missed.
const missGrace = 2 * time.Hour

var (
	regFieldRe = regexp.MustCompile(`(?m)^- \*\*([A-Za-z ]+):\*\*\s*(.+?)\s*$`)
	cronRe     = regexp.MustCompile("`([^`]+)`")
)

// loadSchedule reads the register under agentsDir, by agent name lowercased.
func loadSchedule(agentsDir string, now time.Time) map[string][]ScheduledTask {
	data, err := os.ReadFile(agentsDir + "/scheduled-tasks.md")
	if err != nil {
		return nil
	}
	out := map[string][]ScheduledTask{}
	blocks := regexp.MustCompile(`(?m)^### `).Split(string(data), -1)
	for _, block := range blocks[1:] {
		id, body, _ := strings.Cut(block, "\n")
		fields := map[string]string{}
		for _, m := range regFieldRe.FindAllStringSubmatch(body, -1) {
			if _, seen := fields[m[1]]; !seen {
				fields[m[1]] = m[2]
			}
		}
		agent := strings.ToLower(strings.TrimSpace(fields["Agent"]))
		m := cronRe.FindStringSubmatch(fields["Schedule"])
		if agent == "" || strings.HasPrefix(agent, "<") || m == nil {
			continue
		}
		spec, err := parseCron(m[1])
		if err != nil {
			continue
		}
		t := ScheduledTask{ID: strings.TrimSpace(id), Cron: m[1], Enabled: strings.EqualFold(fields["Enabled"], "true"),
			LastRun: parseRunTime(fields["Last run"], now.Location())}
		t.When = spec.words()
		if t.Enabled {
			t.Next = spec.next(now)
			created := parseRunTime(fields["Created"], now.Location())
			if due := spec.prev(now.Add(-missGrace)); !due.IsZero() && t.LastRun.Before(due) && !due.Before(created) {
				t.Missed = due
			}
		}
		out[agent] = append(out[agent], t)
	}
	return out
}

// parseRunTime reads a "Last run:" value as gate.py does; zero if never.
func parseRunTime(s string, loc *time.Location) time.Time {
	s = strings.TrimSpace(strings.Replace(s, "Z", "+00:00", 1))
	for _, layout := range []string{"2006-01-02T15:04-07:00", "2006-01-02T15:04:05-07:00", "2006-01-02 15:04-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t
	}
	return time.Time{}
}

// cronSpec is a five-field cron schedule. As in gate.py, a time matches when
// every field does (day of month and day of week both, not either).
type cronSpec struct {
	raw                      [5]string
	min, hour, dom, mon, dow map[int]bool
}

func parseCron(s string) (*cronSpec, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, fmt.Errorf("want five fields: %q", s)
	}
	c := &cronSpec{raw: [5]string(f)}
	var err error
	for i, p := range []struct {
		dst    *map[int]bool
		lo, hi int
	}{{&c.min, 0, 59}, {&c.hour, 0, 23}, {&c.dom, 1, 31}, {&c.mon, 1, 12}, {&c.dow, 0, 7}} {
		if *p.dst, err = cronField(f[i], p.lo, p.hi); err != nil {
			return nil, err
		}
	}
	c.dow[0] = c.dow[0] || c.dow[7] // cron's 7 is Sunday too
	return c, nil
}

func cronField(expr string, lo, hi int) (map[int]bool, error) {
	set := map[int]bool{}
	for _, part := range strings.Split(expr, ",") {
		step := 1
		if a, b, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(b)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("bad step %q", part)
			}
			part, step = a, n
		}
		from, to := lo, hi
		if part != "*" {
			a, b, isRange := strings.Cut(part, "-")
			var err error
			if from, err = strconv.Atoi(a); err != nil {
				return nil, fmt.Errorf("bad field %q", part)
			}
			to = from
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return nil, fmt.Errorf("bad field %q", part)
				}
			}
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return set, nil
}

func (c *cronSpec) matchDay(t time.Time) bool {
	return c.dom[t.Day()] && c.mon[int(t.Month())] && c.dow[int(t.Weekday())]
}

// next is the first due time after t, within a year.
func (c *cronSpec) next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	for end := t.AddDate(1, 0, 0); t.Before(end); {
		if !c.matchDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if c.hour[t.Hour()] && c.min[t.Minute()] {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// prev is the last due time at or before t, within a year.
func (c *cronSpec) prev(t time.Time) time.Time {
	t = t.Truncate(time.Minute)
	for end := t.AddDate(-1, 0, 0); t.After(end); {
		if !c.matchDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if c.hour[t.Hour()] && c.min[t.Minute()] {
			return t
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}
}

// words says the common schedules short ("Fri 10:00", "daily 18:00",
// "day 1, 10:00"), else the cron line itself.
func (c *cronSpec) words() string {
	m, h, dom, mon, dow := c.raw[0], c.raw[1], c.raw[2], c.raw[3], c.raw[4]
	mi, errM := strconv.Atoi(m)
	hr, errH := strconv.Atoi(h)
	if mon != "*" || errM != nil {
		return c.cron()
	}
	if h == "*" && dom == "*" && dow == "*" {
		return fmt.Sprintf("hourly at :%02d", mi)
	}
	if errH != nil {
		return c.cron()
	}
	at := fmt.Sprintf("%02d:%02d", hr, mi)
	switch d, err := strconv.Atoi(dow); {
	case dom == "*" && dow == "*":
		return "daily " + at
	case dom == "*" && dow == "1-5":
		return "weekdays " + at
	case dom == "*" && err == nil && d >= 0 && d <= 7:
		return time.Weekday(d % 7).String()[:3] + " " + at
	case dow == "*":
		if n, err := strconv.Atoi(dom); err == nil {
			return fmt.Sprintf("day %d, %s", n, at)
		}
	}
	return c.cron()
}

func (c *cronSpec) cron() string { return strings.Join(c.raw[:], " ") }

// webTask is one scheduled task as the agent page shows it.
type webTask struct {
	ID, When, Last, Next, Missed string
	Off                          bool
}

func webTasks(now time.Time, tasks []ScheduledTask) []webTask {
	var out []webTask
	for _, t := range tasks {
		w := webTask{ID: t.ID, When: t.When, Off: !t.Enabled, Last: "never run"}
		if !t.LastRun.IsZero() {
			w.Last = "ran " + runWhen(now, t.LastRun)
		}
		switch {
		case !t.Enabled:
			w.Next = "off"
		case !t.Next.IsZero():
			w.Next = "next " + runWhen(now, t.Next)
		}
		if !t.Missed.IsZero() {
			w.Missed = "missed " + runWhen(now, t.Missed)
		}
		out = append(out, w)
	}
	return out
}

// runWhen is a run time, past or to come: today, yesterday or tomorrow
// with the clock, else the day and date, so "last Fri" and "next Fri" never
// read alike.
func runWhen(now, t time.Time) string {
	t, now = t.Local(), now.Local()
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local) }
	switch int(day(t).Sub(day(now)).Round(time.Hour).Hours() / 24) {
	case 0:
		return "today " + t.Format("15:04")
	case -1:
		return "yesterday " + t.Format("15:04")
	case 1:
		return "tomorrow " + t.Format("15:04")
	}
	if t.Year() != now.Year() {
		return t.Format("2 Jan 2006")
	}
	return t.Format("Mon 2 Jan")
}
