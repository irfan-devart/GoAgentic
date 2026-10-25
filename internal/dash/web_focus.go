package dash

import (
	"regexp"
	"strings"
	"time"
)

// The session's agreed priorities. At its start an agent declares a
// session focus, up to three items in order, and the principal agrees
// (conventions § Session Priority Declaration). Nothing records it but the
// chat, so it is read from there: the latest agent message with a focus or
// priorities heading over a numbered list.

type sessionFocus struct {
	Items []focusItem
	// At is when it was declared; Agreed: the principal has replied since.
	At     time.Time
	Agreed bool
}

var (
	focusHeadRe = regexp.MustCompile(`(?i)^\W*(proposed |session |today's |agreed )?(focus|priorities)\b`)
	focusItemRe = regexp.MustCompile(`^\s*\d+[.)]\s+(.+)$`)
	boldLeadRe  = regexp.MustCompile(`^\*\*(.+?)\*\*`)
	idOnlyRe    = regexp.MustCompile(`^\s*#?\w{1,6}[:.]?\s*$`)
)

// focusItem is one priority: its lead, the rest of what the agent said
// about it, and the tracker item it names, if any (filled by the page).
type focusItem struct {
	Lead, Text string
	Ref        *webItem
}

// itemRefRe finds the tracker item a priority names ("#63", "#18d").
var itemRefRe = regexp.MustCompile(`#(\d+[a-z]?)\b`)

// maxFocus bounds the items shown; the convention asks for three.
const maxFocus = 5

// findFocus is the session's latest declared focus, or nil.
func findFocus(chat []ChatEntry) *sessionFocus {
	for i := len(chat) - 1; i >= 0; i-- {
		if chat[i].Kind != "agent" {
			continue
		}
		items := focusItems(chat[i].Text)
		if len(items) == 0 {
			continue
		}
		f := &sessionFocus{Items: items, At: chat[i].At}
		for _, later := range chat[i+1:] {
			f.Agreed = f.Agreed || later.Kind == "you"
		}
		return f
	}
	return nil
}

// focusItems reads the numbered list under a focus heading in a message.
func focusItems(text string) []focusItem {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !focusHeadRe.MatchString(strings.TrimSpace(line)) {
			continue
		}
		var items []focusItem
		for _, l := range lines[i+1:] {
			if strings.TrimSpace(l) == "" && len(items) == 0 {
				continue
			}
			m := focusItemRe.FindStringSubmatch(l)
			if m == nil {
				if len(items) > 0 && strings.TrimSpace(l) == "" {
					break
				}
				if len(items) > 0 && strings.HasPrefix(l, " ") {
					last := &items[len(items)-1] // the item's own second line
					last.Text = strings.TrimSpace(last.Text + " " + plainText(l))
					continue
				}
				break
			}
			items = append(items, focusItem{Lead: focusLead(m[1]), Text: focusRest(m[1])})
		}
		if len(items) > 0 {
			return items[:min(len(items), maxFocus)]
		}
	}
	return nil
}

// focusLead is an item's bold lead if it has one, else its first sentence,
// without Markdown, cut short.
func focusLead(s string) string {
	// A bold lead that is only a number ("**#86** handover: …") keeps the
	// words after it.
	if m := boldLeadRe.FindStringSubmatch(s); m != nil && !idOnlyRe.MatchString(m[1]) {
		s = m[1]
	} else if i := strings.Index(s, ". "); i > 0 {
		s = s[:i]
	}
	s = strings.NewReplacer("**", "", "`", "").Replace(s)
	return cut(strings.TrimRight(strings.TrimSpace(s), ".:"), 110)
}

// focusRest is what an item says after its lead.
func focusRest(s string) string {
	if m := boldLeadRe.FindStringSubmatch(s); m != nil && !idOnlyRe.MatchString(m[1]) {
		s = s[len(m[0]):]
	} else if i := strings.Index(s, ". "); i > 0 {
		s = s[i+2:]
	} else {
		s = ""
	}
	return strings.TrimLeft(plainText(s), ":;,.—– ")
}

func plainText(s string) string {
	return strings.TrimSpace(strings.NewReplacer("**", "", "`", "").Replace(s))
}

// linkFocus points each priority that names a tracker item at it.
func linkFocus(f *sessionFocus, lists ...[]webItem) {
	if f == nil {
		return
	}
	byID := map[string]*webItem{}
	for _, list := range lists {
		for i := range list {
			byID[strings.TrimPrefix(list[i].ID, "#")] = &list[i]
		}
	}
	for i := range f.Items {
		if m := itemRefRe.FindStringSubmatch(f.Items[i].Lead); m != nil {
			f.Items[i].Ref = byID[m[1]]
		}
	}
}
