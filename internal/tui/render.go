package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/philband/callboard/internal/api"
)

// Styles use the terminal's own 4-bit palette and never set a background, so
// they stay readable on light and dark themes.
var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	headStyle  = lipgloss.NewStyle().Faint(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Red)
	plainStyle = lipgloss.NewStyle()

	statusStyle = map[string]lipgloss.Style{
		api.StatusActive: lipgloss.NewStyle().Foreground(lipgloss.Green),
		api.StatusIdle:   lipgloss.NewStyle().Foreground(lipgloss.Yellow),
		api.StatusGone:   dimStyle,
		api.StatusHuman:  lipgloss.NewStyle().Foreground(lipgloss.Blue),
	}
)

// wideAt is the terminal width from which sessions and jobs sit side by side.
const wideAt = 100

// fullCols is the pane width from which every column is shown.
const fullCols = 64

func (m *model) View() tea.View {
	sessions := m.filteredSessions()
	jobs := m.filteredJobs()
	l := m.layout(len(sessions), len(jobs))

	var lines []string
	if l.wide {
		lw := (l.w - 1) / 2
		rw := l.w - 1 - lw
		left := sessionPane(sessions, lw, l.sessH)
		right := jobPane(jobs, rw, l.jobH)
		for i := range left {
			lines = append(lines, left[i]+" "+right[i])
		}
	} else {
		lines = append(lines, sessionPane(sessions, l.w, l.sessH)...)
		lines = append(lines, jobPane(jobs, l.w, l.jobH)...)
	}
	lines = append(lines, m.feedPane(l.w, l.feedH)...)
	lines = append(lines, m.footer(l.w))

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// layout splits the screen between the three panes and the footer.
type layout struct {
	w, h               int
	wide               bool
	sessH, jobH, feedH int
}

func (m *model) layout(ns, nj int) layout {
	l := layout{w: m.width, h: m.height}
	if l.w <= 0 {
		l.w = 80
	}
	if l.h <= 0 {
		l.h = 24
	}
	l.wide = l.w >= wideAt
	body := max(0, l.h-1) // the footer owns one line

	if l.wide {
		top := min(max(max(ns, nj)+2, 3), 12)
		if top+3 > body {
			top = max(3, body-3)
		}
		l.sessH = min(top, body)
		l.jobH = l.sessH
		l.feedH = body - l.sessH
		return l
	}

	l.sessH = min(max(ns+2, 3), 10)
	l.jobH = min(max(nj+2, 3), 8)
	for l.sessH+l.jobH+3 > body && (l.sessH > 3 || l.jobH > 3) {
		switch {
		case l.jobH >= l.sessH && l.jobH > 3:
			l.jobH--
		case l.sessH > 3:
			l.sessH--
		default:
			l.jobH--
		}
	}
	l.sessH = min(l.sessH, body)
	l.jobH = min(l.jobH, body-l.sessH)
	l.feedH = body - l.sessH - l.jobH
	return l
}

// feedHeight is the number of event lines currently visible.
func (m *model) feedHeight() int {
	l := m.layout(len(m.filteredSessions()), len(m.filteredJobs()))
	return max(1, l.feedH-1)
}

// Panes. Each returns exactly h lines, each exactly w columns wide.

func sessionPane(sessions []api.Session, w, h int) []string {
	p := newPane(w, h)
	p.title("SESSIONS", len(sessions))
	if w >= fullCols {
		p.add(row(w,
			cell{"CALLSIGN", 12, headStyle}, cell{"STATUS", 6, headStyle},
			cell{"ROLE", 9, headStyle}, cell{"PLATFORM", 9, headStyle},
			cell{"SEEN", 4, headStyle}, cell{"SCOPE", 0, headStyle}))
	} else {
		p.add(row(w,
			cell{"CALLSIGN", 12, headStyle}, cell{"STATUS", 6, headStyle},
			cell{"SEEN", 4, headStyle}, cell{"ROLE", 0, headStyle}))
	}
	for _, s := range sessions {
		st := sessionStatus(s)
		ss := statusStyle[st]
		if w >= fullCols {
			p.add(row(w,
				cell{s.Callsign, 12, plainStyle}, cell{st, 6, ss},
				cell{dash(s.Role), 9, plainStyle}, cell{dash(s.Platform), 9, plainStyle},
				cell{age(s.LastSeen), 4, dimStyle}, cell{dash(s.PrimaryScope()), 0, dimStyle}))
		} else {
			p.add(row(w,
				cell{s.Callsign, 12, plainStyle}, cell{st, 6, ss},
				cell{age(s.LastSeen), 4, dimStyle}, cell{dash(s.Role), 0, plainStyle}))
		}
	}
	return p.lines()
}

func jobPane(jobs []api.Job, w, h int) []string {
	p := newPane(w, h)
	p.title("JOBS", len(jobs))
	byW := 16
	titleW := w - (5 + 1) - (8 + 1) - (byW + 1)
	full := w >= fullCols && titleW >= 10
	if full {
		p.add(row(w,
			cell{"ID", 5, headStyle}, cell{"STATUS", 8, headStyle},
			cell{"TITLE", titleW, headStyle}, cell{"BY/CLAIMED", 0, headStyle}))
	} else {
		p.add(row(w,
			cell{"ID", 5, headStyle}, cell{"STATUS", 8, headStyle},
			cell{"TITLE", 0, headStyle}))
	}
	for _, j := range jobs {
		js := jobStyle(j.Status)
		if full {
			p.add(row(w,
				cell{j.ID, 5, plainStyle}, cell{j.Status, 8, js},
				cell{firstLine(j.Title), titleW, plainStyle}, cell{jobWho(j), 0, dimStyle}))
		} else {
			p.add(row(w,
				cell{j.ID, 5, plainStyle}, cell{j.Status, 8, js},
				cell{firstLine(j.Title), 0, plainStyle}))
		}
	}
	return p.lines()
}

func (m *model) feedPane(w, h int) []string {
	events := m.filteredEvents()
	p := newPane(w, h)
	if m.offset > 0 && w >= 30 {
		p.add(row(w, cell{fmt.Sprintf("FEED (%d)", len(events)), w - 14, titleStyle},
			cell{"scrolled up", 0, dimStyle}))
	} else {
		p.title("FEED", len(events))
	}

	rows := max(0, h-1)
	if rows == 0 {
		return p.lines()
	}
	// Clamp the scroll offset and take the visible window from the bottom.
	if m.offset > max(0, len(events)-rows) {
		m.offset = max(0, len(events)-rows)
	}
	end := len(events) - m.offset
	start := max(0, end-rows)
	for _, e := range events[start:end] {
		p.add(row(w,
			cell{e.Time.Local().Format("15:04:05"), 8, dimStyle},
			cell{e.Type, 9, plainStyle},
			cell{summarize(e), 0, plainStyle}))
	}
	return p.lines()
}

func (m *model) footer(w int) string {
	help := "q quit · j/k or arrows scroll feed · r refresh · s cycle scope"
	switch {
	case w < 50:
		help = "q · j/k scroll · r · s"
	case w < 70:
		help = "q quit · j/k scroll · r refresh · s scope"
	}
	left, style := help, dimStyle
	if m.err != "" {
		left, style = "! "+m.err, errStyle
	}
	right := "scope " + scopeLabel(m.scope)
	rw := min(len(right), w/3)
	if rw < 6 {
		return row(w, cell{left, 0, style})
	}
	return row(w, cell{left, w - rw - 1, style}, cell{right, 0, dimStyle})
}

// Filtering and ordering.

func (m *model) filteredSessions() []api.Session {
	out := make([]api.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if m.scope == "" || len(s.Scopes) == 0 || s.InScope(m.scope) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := statusRank(sessionStatus(out[i])), statusRank(sessionStatus(out[j]))
		if a != b {
			return a < b
		}
		return out[i].Callsign < out[j].Callsign
	})
	return out
}

func (m *model) filteredJobs() []api.Job {
	out := make([]api.Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		if m.scope == "" || j.Scope == m.scope {
			out = append(out, j)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := jobRank(out[i].Status), jobRank(out[j].Status)
		if a != b {
			return a < b
		}
		return out[i].PostedAt.After(out[j].PostedAt)
	})
	return out
}

// filteredEvents keeps hub-wide events (delivered, for instance) in every view.
func (m *model) filteredEvents() []api.Event {
	if m.scope == "" {
		return m.events
	}
	out := make([]api.Event, 0, len(m.events))
	for _, e := range m.events {
		if s := e.ScopeName(); s == "" || s == m.scope {
			out = append(out, e)
		}
	}
	return out
}

// summarize renders one event the way `callboard tail` does.
func summarize(e api.Event) string {
	switch e.Type {
	case api.EvCheckin:
		if e.Session == nil {
			return actor(e) + " joined"
		}
		s := e.Session
		out := s.Callsign
		if s.Role != "" {
			out += " (" + s.Role + ")"
		}
		out += " joined"
		if sc := s.PrimaryScope(); sc != "" {
			out += " scope " + sc
		}
		return out
	case api.EvCheckout:
		return actor(e) + " left"
	case api.EvMessage:
		if e.Message == nil {
			return ""
		}
		msg := e.Message
		return fmt.Sprintf("%s -> %s (%s) %s", msg.From, msg.To, msg.Kind, firstLine(msg.Body))
	case api.EvDelivered:
		return fmt.Sprintf("%s read %s", actor(e), strings.Join(e.MessageIDs, ","))
	case api.EvJobPost:
		if e.Job == nil {
			return actor(e) + " posted a job"
		}
		who := e.Job.PostedBy
		if who == "" {
			who = actor(e)
		}
		return fmt.Sprintf("%s posted %s: %s", who, e.Job.ID, firstLine(e.Job.Title))
	case api.EvJobClaim:
		return jobLine(e, "claimed")
	case api.EvJobDone:
		return jobLine(e, "finished")
	case api.EvJobFail:
		return jobLine(e, "failed")
	}
	return e.Type
}

func jobLine(e api.Event, verb string) string {
	id := ""
	who := actor(e)
	if e.Job != nil {
		id = e.Job.ID
		if who == "" {
			who = e.Job.ClaimedBy
		}
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s", who, verb, id))
}

func actor(e api.Event) string {
	if e.Callsign != "" {
		return e.Callsign
	}
	if e.Session != nil {
		return e.Session.Callsign
	}
	return "?"
}

// Small helpers.

// pane accumulates exactly h lines of exactly w columns.
type paneBuf struct {
	w, h int
	out  []string
}

func newPane(w, h int) *paneBuf {
	return &paneBuf{w: w, h: max(0, h)}
}

func (p *paneBuf) title(name string, n int) {
	p.add(row(p.w, cell{fmt.Sprintf("%s (%d)", name, n), 0, titleStyle}))
}

func (p *paneBuf) add(line string) {
	if len(p.out) < p.h {
		p.out = append(p.out, line)
	}
}

func (p *paneBuf) lines() []string {
	for len(p.out) < p.h {
		p.out = append(p.out, strings.Repeat(" ", max(0, p.w)))
	}
	return p.out
}

// cell is one column of a row. A width of 0 takes whatever is left.
type cell struct {
	text  string
	width int
	style lipgloss.Style
}

// row lays cells out left to right, separated by a space, and returns a string
// that is exactly w columns wide. Padding is added outside the styles so the
// visible width is known even though the string carries escape codes.
func row(w int, cells ...cell) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, c := range cells {
		if used >= w {
			break
		}
		if used > 0 {
			b.WriteString(" ")
			used++
			if used >= w {
				break
			}
		}
		cw := c.width
		if cw <= 0 || cw > w-used {
			cw = w - used
		}
		txt := truncate(c.text, cw)
		b.WriteString(c.style.Render(txt))
		if pad := cw - len([]rune(txt)); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		used += cw
	}
	if used < w {
		b.WriteString(strings.Repeat(" ", w-used))
	}
	return b.String()
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func scopeLabel(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

// sessionStatus falls back to the last-seen windows when the hub sent none.
func sessionStatus(s api.Session) string {
	if s.Status != "" {
		return s.Status
	}
	if s.Callsign == api.Human {
		return api.StatusHuman
	}
	switch d := time.Since(s.LastSeen); {
	case d < api.ActiveWindow:
		return api.StatusActive
	case d < api.IdleWindow:
		return api.StatusIdle
	}
	return api.StatusGone
}

func statusRank(s string) int {
	switch s {
	case api.StatusActive:
		return 0
	case api.StatusHuman:
		return 1
	case api.StatusIdle:
		return 2
	case api.StatusGone:
		return 3
	}
	return 4
}

func jobRank(s string) int {
	switch s {
	case api.JobOpen:
		return 0
	case api.JobClaimed:
		return 1
	case api.JobDone:
		return 2
	case api.JobFailed:
		return 3
	}
	return 4
}

func jobStyle(status string) lipgloss.Style {
	switch status {
	case api.JobOpen:
		return statusStyle[api.StatusIdle]
	case api.JobClaimed:
		return statusStyle[api.StatusActive]
	case api.JobFailed:
		return errStyle
	}
	return dimStyle
}

// jobWho renders the "by/claimed" column: poster, then whoever holds it.
func jobWho(j api.Job) string {
	who := j.ClaimedBy
	if who == "" {
		who = j.Assignee
	}
	if who == "" {
		return j.PostedBy
	}
	return j.PostedBy + "/" + who
}

// age is a short relative last-seen, e.g. "12s", "4m", "2h".
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
