package dash

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const registerFixture = `# Scheduled Tasks

## Tasks

### example-task
- **Schedule:** ` + "`0 9 * * 1`" + ` (Monday 09:00)
- **Agent:** <Name>
- **Enabled:** false

### friday-status
- **Schedule:** ` + "`0 10 * * 5`" + ` (Friday 10:00 AM MYT)
- **Agent:** Sigrid
- **Enabled:** true
- **Last run:** 2026-10-23T10:07+08:00

### wednesday-prep
- **Schedule:** ` + "`0 9 * * 3`" + `
- **Agent:** Sigrid
- **Enabled:** true
- **Last run:** 2026-10-14T09:08+08:00

### new-daily
- **Schedule:** ` + "`0 18 * * *`" + `
- **Agent:** sigrid
- **Enabled:** true
- **Created:** 2026-10-26

### monthly
- **Schedule:** ` + "`0 10 1 * *`" + `
- **Agent:** Sigrid
- **Enabled:** false
`

func TestLoadSchedule(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "scheduled-tasks.md"), registerFixture)
	loc := time.FixedZone("MYT", 8*3600)
	at := time.Date(2026, 10, 26, 0, 10, 0, 0, loc) // Mon 00:10
	got := loadSchedule(dir, at)
	if len(got) != 1 || len(got["sigrid"]) != 4 {
		t.Fatalf("tasks = %+v, want Sigrid's four and not the example", got)
	}
	byID := map[string]ScheduledTask{}
	for _, task := range got["sigrid"] {
		byID[task.ID] = task
	}
	fri := byID["friday-status"]
	if fri.When != "Fri 10:00" || !fri.Missed.IsZero() || !fri.Next.Equal(time.Date(2026, 10, 30, 10, 0, 0, 0, loc)) {
		t.Errorf("friday = %+v", fri)
	}
	// Due Wed 21 Oct, last ran the week before.
	if wed := byID["wednesday-prep"]; !wed.Missed.Equal(time.Date(2026, 10, 21, 9, 0, 0, 0, loc)) {
		t.Errorf("wednesday missed = %v, want Wed 21 Oct 09:00", wed.Missed)
	}
	// Never run, but created the day after its last due time: not missed.
	if d := byID["new-daily"]; d.When != "daily 18:00" || !d.Missed.IsZero() || !d.LastRun.IsZero() {
		t.Errorf("new daily = %+v", d)
	}
	if m := byID["monthly"]; m.Enabled || !m.Next.IsZero() || !m.Missed.IsZero() || m.When != "day 1, 10:00" {
		t.Errorf("disabled monthly = %+v", m)
	}
	if loadSchedule(t.TempDir(), at) != nil {
		t.Error("no register: no tasks")
	}
}

func TestCron(t *testing.T) {
	loc := time.FixedZone("MYT", 8*3600)
	at := time.Date(2026, 10, 26, 0, 10, 0, 0, loc)
	for expr, want := range map[string][3]string{
		"7 * * * *":       {"hourly at :07", "2026-10-26 00:07", "2026-10-26 01:07"},
		"30 8 * * 1-5":    {"weekdays 08:30", "2026-10-23 08:30", "2026-10-26 08:30"},
		"0 9 * * 7":       {"Sun 09:00", "2026-10-25 09:00", "2026-11-01 09:00"},
		"*/15 9-10 * * *": {"*/15 9-10 * * *", "2026-10-25 10:45", "2026-10-26 09:00"},
	} {
		c, err := parseCron(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		const f = "2006-01-02 15:04"
		if got := [3]string{c.words(), c.prev(at).Format(f), c.next(at).Format(f)}; got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
	for _, bad := range []string{"0 9 * *", "x 9 * * *", "0 9 * * 1/0"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestWebScheduledCard(t *testing.T) {
	parent := webFleet(t)
	h := NewWebHandler(WebSource{FleetRoot: parent, Options: webOptions})
	if _, body := get(t, h, "/w/alpha/Sigrid"); !strings.Contains(body, `class="card card-sched quiet"`) || !strings.Contains(body, "No scheduled tasks.") {
		t.Errorf("no tasks: want a quiet Scheduled card saying so")
	}
	write(t, filepath.Join(parent, "alpha/agents/scheduled-tasks.md"), registerFixture)
	_, body := get(t, h, "/w/alpha/Sigrid")
	for _, want := range []string{`class="card card-sched"`, `<span class="task">friday-status</span> <span class="dim">Fri 10:00</span>`, "· next ", `<li class="off"><span class="task">monthly</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("Scheduled card lacks %q:\n%s", want, body)
		}
	}
}

func TestRunWhen(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 15, 0, 0, time.Local)
	for in, want := range map[time.Time]string{
		time.Date(2026, 10, 25, 18, 7, 0, 0, time.Local): "yesterday 18:07",
		time.Date(2026, 10, 26, 18, 0, 0, 0, time.Local): "today 18:00",
		time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local):  "tomorrow 09:00",
		time.Date(2026, 10, 23, 10, 7, 0, 0, time.Local): "Fri 23 Oct",
		time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local): "Fri 30 Oct",
		time.Date(2027, 1, 1, 10, 0, 0, 0, time.Local):   "1 Jan 2027",
	} {
		if got := runWhen(now, in); got != want {
			t.Errorf("runWhen(%v) = %q, want %q", in, got, want)
		}
	}
}
