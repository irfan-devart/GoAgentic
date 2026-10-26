package dash

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A team is a lead and the agents whose context.md names it (lead: <Name>).
// Everything here is read from the files the delegate protocol already
// writes (reference/task.md): the lead's outcome files, and the work orders
// in each member's peer/ folder with their timestamped logs. Nothing new is
// recorded for the dashboard.

// Team is one lead and its members.
type Team struct {
	Lead     *Agent
	Members  []*Agent
	Outcomes []Outcome
	// Orders is every work order the lead has handed its members, newest
	// first.
	Orders []WorkOrder
}

// Outcome is one file in the lead's outcomes/ folder.
type Outcome struct {
	// File is the file's base name without .md ("ci-minutes").
	File     string
	Title    string
	Status   string
	Approved string
	// Next is the first entry of its ## Next section: where it stands.
	Next      string
	Plan      []string
	Decisions []LogLine
}

// WorkOrder is one work order file.
type WorkOrder struct {
	// Path is the file, relative to the member's folder.
	Path   string
	Member string
	// No is its number ("WO-53"), when it has one.
	No     string
	Title  string
	Status string
	Start  time.Time
	Due    time.Time
	// Timebox is how long it may run; 0 when unset.
	Timebox time.Duration
	Rounds  int
	// Outcome is the outcome file it serves (Outcome.File), or "".
	Outcome  string
	TaskPane string
	Log      []LogLine
}

// LogLine is one timestamped entry of a work order's ## Log, or of an
// outcome's ## Decisions.
type LogLine struct {
	At   time.Time
	Who  string
	Text string
	// NoClock: the entry gave a date but no time.
	NoClock bool
}

// InFlight is true while the member or the lead still has work to do on it.
func (o WorkOrder) InFlight() bool {
	switch o.Status {
	case "done", "stopped", "":
		return false
	}
	return true
}

// LastAt is when the order last moved: its newest log line, else its start.
func (o WorkOrder) LastAt() time.Time {
	t := o.Start
	for _, l := range o.Log {
		if l.At.After(t) {
			t = l.At
		}
	}
	return t
}

// Teams finds the workspace's teams, in the order of their leads.
func Teams(ws *Workspace) []*Team {
	var teams []*Team
	byLead := map[string]*Team{}
	for _, a := range ws.Agents {
		if a.Lead == "" || strings.EqualFold(a.Lead, a.Name) {
			continue
		}
		t := byLead[strings.ToLower(a.Lead)]
		if t == nil {
			lead := ws.Agent(a.Lead)
			if lead == nil {
				continue
			}
			t = &Team{Lead: lead}
			byLead[strings.ToLower(a.Lead)] = t
		}
		t.Members = append(t.Members, a)
	}
	for _, a := range ws.Agents {
		if t := byLead[strings.ToLower(a.Name)]; t != nil {
			t.Outcomes = loadOutcomes(filepath.Join(a.Dir, "outcomes"))
			for _, m := range t.Members {
				t.Orders = append(t.Orders, loadOrders(m, a.Name, t.Outcomes)...)
			}
			sort.SliceStable(t.Orders, func(i, j int) bool { return t.Orders[i].Start.After(t.Orders[j].Start) })
			teams = append(teams, t)
		}
	}
	return teams
}

// Agent finds an agent by name, any case.
func (ws *Workspace) Agent(name string) *Agent {
	for _, a := range ws.Agents {
		if strings.EqualFold(a.Name, name) {
			return a
		}
	}
	return nil
}

// TeamOf is the team led by lead, or nil.
func TeamOf(ws *Workspace, lead string) *Team {
	for _, t := range Teams(ws) {
		if strings.EqualFold(t.Lead.Name, lead) {
			return t
		}
	}
	return nil
}

var (
	woNoRe     = regexp.MustCompile(`\bWO-(\d+)\b`)
	logTimeRe  = regexp.MustCompile(`^-\s+(\d{1,2}):(\d{2})\s+(.*)$`)
	decisionRe = regexp.MustCompile(`^-\s+(\d{4}-\d{2}-\d{2})(?:\s+(\d{1,2}):(\d{2}))?\s+(.*)$`)
	logWhoRe   = regexp.MustCompile(`^([A-Z][A-Za-z]+)\b\s*(?:\([^)]*\))?\s*(?:—|-|:)?\s*`)
	headTitle  = regexp.MustCompile(`^##\s+Work order\s*\(WO-\d+\)\s*:\s*(.+)$`)
)

// loadOrders reads the work orders from lead in a member's peer/ folder and
// its archive.
func loadOrders(m *Agent, lead string, outcomes []Outcome) []WorkOrder {
	var out []WorkOrder
	for _, sub := range []string{"peer", filepath.Join("peer", "archive")} {
		for _, f := range listFiles(filepath.Join(m.Dir, sub), "*.md") {
			rel := filepath.Join(sub, f.Name)
			path := filepath.Join(m.Dir, rel)
			fm := readFrontmatter(path)
			if !strings.EqualFold(fm["type"], "task") || !strings.EqualFold(fm["from"], lead) {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			o := parseOrder(string(data), fm)
			o.Path, o.Member = rel, m.Name
			o.Outcome = outcomeFor(fm["outcome"], outcomes)
			out = append(out, o)
		}
	}
	return out
}

// parseOrder reads a work order's body: its number, a title, and its log.
func parseOrder(text string, fm map[string]string) WorkOrder {
	o := WorkOrder{
		Status:   strings.ToLower(fm["status"]),
		Start:    parseStamp(fm["date"]),
		Due:      parseStamp(fm["due"]),
		TaskPane: fm["task_pane"],
	}
	o.Rounds, _ = strconv.Atoi(fm["rounds"])
	if d, err := time.ParseDuration(fm["timebox"]); err == nil {
		o.Timebox = d
	}
	sections := markdownSections(text)
	order := sections["work order"]
	if m := woNoRe.FindString(order.head + "\n" + firstN(order.body, 800)); m != "" {
		o.No = m
	}
	if m := headTitle.FindStringSubmatch(order.head); m != nil {
		o.Title = m[1]
	} else {
		for _, line := range strings.Split(order.body, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				o.Title = line
				break
			}
		}
	}
	o.Title = clip(stripWOPrefix(plainText(o.Title)), 120)
	o.Log = parseLog(sections["log"].body, o.Start)
	return o
}

// stripWOPrefix drops a "WO-5 (team-pulse-live): " lead-in from a title.
func stripWOPrefix(s string) string {
	if m := regexp.MustCompile(`^WO-\d+\s*(?:\([^)]*\))?\s*[:—-]\s*`).FindStringIndex(s); m != nil {
		return s[m[1]:]
	}
	return s
}

// parseLog reads "- 08:30 Balbus — text" lines. Log times carry no date: a
// line is on the order's start day, or a later one when its time falls well
// before the line above it (the log ran past midnight).
func parseLog(body string, start time.Time) []LogLine {
	var out []LogLine
	if start.IsZero() {
		return nil
	}
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	prev := start
	for _, line := range strings.Split(body, "\n") {
		m := logTimeRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		at := day.Add(time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute)
		for at.Before(prev.Add(-3 * time.Hour)) {
			day = day.AddDate(0, 0, 1)
			at = at.AddDate(0, 0, 1)
		}
		prev = at
		who, text := splitWho(m[3])
		out = append(out, LogLine{At: at, Who: who, Text: text})
	}
	return out
}

// splitWho takes the speaker off the front of a log entry: "Balbus — text",
// "Probus (WO-9) — text", "Frontinus: text".
func splitWho(s string) (string, string) {
	m := logWhoRe.FindStringSubmatchIndex(s)
	if m == nil {
		return "", plainText(s)
	}
	who := s[m[2]:m[3]]
	rest := s[m[1]:]
	// A capitalised first word that was not followed by a dash or colon is
	// the start of the text, not a speaker.
	sep := strings.TrimSpace(s[m[3]:m[1]])
	if !strings.HasSuffix(sep, "—") && !strings.HasSuffix(sep, ":") && !strings.HasSuffix(sep, "-") {
		return "", plainText(s)
	}
	return who, plainText(rest)
}

// loadOutcomes reads the lead's outcome files.
func loadOutcomes(dir string) []Outcome {
	var out []Outcome
	for _, f := range listFiles(dir, "*.md") {
		path := filepath.Join(dir, f.Name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fm := readFrontmatter(path)
		o := Outcome{
			File:     strings.TrimSuffix(f.Name, ".md"),
			Title:    fm["outcome"],
			Status:   strings.ToLower(fm["status"]),
			Approved: fm["approved"],
		}
		if i := strings.Index(o.Title, " — "); i > 0 {
			o.Title = o.Title[:i]
		}
		o.Title = plainText(o.Title)
		if o.Title == "" {
			o.Title = o.File
		}
		sections := markdownSections(string(data))
		o.Next = clip(plainText(firstEntry(sections["next"].body)), 320)
		for _, line := range strings.Split(sections["plan"].body, "\n") {
			if m := regexp.MustCompile(`^\d+\.\s+(.*)$`).FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				o.Plan = append(o.Plan, clip(plainText(m[1]), 140))
			}
		}
		for _, line := range strings.Split(sections["decisions"].body, "\n") {
			m := decisionRe.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			hour := orDefault(m[2], "00")
			if len(hour) == 1 {
				hour = "0" + hour
			}
			at := parseStamp(m[1] + " " + hour + ":" + orDefault(m[3], "00"))
			who, text := splitWho(m[4])
			o.Decisions = append(o.Decisions, LogLine{At: at, Who: who, Text: text, NoClock: m[2] == ""})
		}
		out = append(out, o)
	}
	return out
}

// outcomeFor matches a work order's outcome: field to an outcome file by
// the file's path in it.
func outcomeFor(field string, outcomes []Outcome) string {
	for _, o := range outcomes {
		if strings.Contains(field, "outcomes/"+o.File+".md") {
			return o.File
		}
	}
	return ""
}

type mdSection struct{ head, body string }

// markdownSections splits a document at its ## headings, keyed by the
// heading's first words lowercased ("work order", "log", "next").
func markdownSections(text string) map[string]mdSection {
	out := map[string]mdSection{}
	var key, head string
	var body []string
	flush := func() {
		if key != "" {
			if _, seen := out[key]; !seen {
				out[key] = mdSection{head: head, body: strings.Join(body, "\n")}
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			head, body = strings.TrimSpace(line), nil
			key = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			for _, k := range []string{"work order", "log", "next", "plan", "decisions"} {
				if strings.HasPrefix(key, k) {
					key = k
				}
			}
			continue
		}
		body = append(body, line)
	}
	flush()
	return out
}

// firstEntry is the first bullet or paragraph of a section.
func firstEntry(body string) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		if len(lines) > 0 && strings.HasPrefix(t, "- ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(t, "- "))
	}
	return strings.Join(lines, " ")
}

// clip cuts s to about n characters at a word.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-") + "…"
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// parseStamp reads "2006-01-02 15:04" (or a bare date) in local time.
func parseStamp(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}
