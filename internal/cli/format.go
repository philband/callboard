package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/philband/callboard/internal/api"
)

// Text formatting shared by every command that prints messages, jobs or
// sessions. Output is meant to be read by an agent or a person, so it is
// stable, compact and free of colour.

func stamp(t time.Time) string {
	t = t.Local()
	if t.YearDay() == time.Now().YearDay() && t.Year() == time.Now().Year() {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatMessage renders one message as a header line plus the body.
func formatMessage(m api.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s %s from %s to %s at %s", m.ID, m.Kind, m.From, m.To, stamp(m.Time))
	if m.Scope != "" && !strings.HasPrefix(m.To, api.ScopePrefix) {
		fmt.Fprintf(&b, " (scope %s)", m.Scope)
	}
	if m.Ref != "" {
		fmt.Fprintf(&b, " [job %s]", m.Ref)
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimRight(m.Body, "\n"))
	b.WriteString("\n")
	return b.String()
}

// formatMessages renders many messages separated by blank lines.
func formatMessages(ms []api.Message) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = formatMessage(m)
	}
	return strings.Join(parts, "\n")
}

// formatJob renders one job as a single line plus an indented body.
func formatJob(j api.Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-5s %-8s %s", j.ID, j.Status, j.Title)
	fmt.Fprintf(&b, "  (by %s", j.PostedBy)
	switch {
	case j.ClaimedBy != "":
		fmt.Fprintf(&b, ", %s %s", j.Status, j.ClaimedBy)
	case j.Assignee != "":
		fmt.Fprintf(&b, ", for %s", j.Assignee)
	}
	fmt.Fprintf(&b, ", scope %s)", j.Scope)
	if j.Body != "" {
		b.WriteString("\n      " + strings.ReplaceAll(strings.TrimRight(j.Body, "\n"), "\n", "\n      "))
	}
	if j.Result != "" {
		b.WriteString("\n      result: " + strings.ReplaceAll(strings.TrimRight(j.Result, "\n"), "\n", "\n      "))
	}
	return b.String()
}

// formatSession renders one roster line.
func formatSession(s api.Session) string {
	role := s.Role
	if role == "" {
		role = "-"
	}
	platform := s.Platform
	if platform == "" {
		platform = "-"
	}
	scope := s.PrimaryScope()
	if scope == "" {
		scope = "-"
	}
	seen := "-"
	if !s.LastSeen.IsZero() {
		seen = stamp(s.LastSeen)
	}
	line := fmt.Sprintf("%-14s %-7s %-12s %-12s %-9s %s", s.Callsign, s.Status, role, platform, seen, scope)
	if len(s.Scopes) > 1 {
		line += " +" + strings.Join(s.Scopes[1:], " +")
	}
	return line
}

// sessionHeader is the column header matching formatSession.
const sessionHeader = "CALLSIGN       STATUS  ROLE         PLATFORM     SEEN      SCOPE"
