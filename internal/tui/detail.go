package tui

import (
	"fmt"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// renderDetail draws the focused card's full spec and run history.
func (m *Model) renderDetail() []string {
	card := m.FocusedCard()
	if card == nil {
		return []string{paint(m.palette.Dim, " No card selected.")}
	}
	width := m.width
	lines := m.detailLines(card, width)

	visible := m.height - 2
	if m.detailScroll > len(lines)-visible {
		m.detailScroll = len(lines) - visible
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
	end := m.detailScroll + visible
	if end > len(lines) {
		end = len(lines)
	}
	return lines[m.detailScroll:end]
}

// detailLines is the full, unscrolled detail body.
func (m *Model) detailLines(card *Card, width int) []string {
	spec := card.Spec
	inner := width - 2
	var out []string

	add := func(format string, args ...any) {
		out = append(out, " "+truncate(fmt.Sprintf(format, args...), inner))
	}
	blank := func() { out = append(out, "") }
	rule := func() { out = append(out, " "+paint(m.palette.Border, strings.Repeat("─", inner))) }

	add("%s  %s", paint(m.palette.Bold, spec.ID), paint(m.palette.Bold, spec.Title))
	rule()
	add("%s  %s", paint(m.palette.Dim, "status    "), paint(m.palette.Status(spec.Status), string(spec.Status)))
	add("%s  %s", paint(m.palette.Dim, "owner     "), orDash(spec.Owner))
	add("%s  %s   %s  %s", paint(m.palette.Dim, "priority  "), orDash(spec.Priority),
		paint(m.palette.Dim, "complexity"), orDash(spec.Complexity))
	add("%s  %s   %s  %s", paint(m.palette.Dim, "created   "), orDash(spec.Created),
		paint(m.palette.Dim, "updated   "), orDash(spec.Updated))
	if len(spec.Labels) > 0 {
		add("%s  %s", paint(m.palette.Dim, "labels    "), strings.Join(spec.Labels, " "))
	}
	if len(spec.Dependencies) > 0 {
		add("%s  %s", paint(m.palette.Dim, "depends   "), strings.Join(spec.Dependencies, " "))
	}
	if spec.Epic != "" {
		add("%s  %s", paint(m.palette.Dim, "epic      "), spec.Epic)
	}
	if strings.TrimSpace(spec.RiskNotes) != "" {
		add("%s  %s", paint(m.palette.Dim, "risk      "), spec.RiskNotes)
	}

	if criteria := spec.AcceptanceCriteria(); len(criteria) > 0 {
		blank()
		done := 0
		for _, criterion := range criteria {
			if criterion.Done {
				done++
			}
		}
		add("%s %s", paint(m.palette.Bold, "Acceptance criteria"),
			paint(m.palette.Dim, fmt.Sprintf("(%d/%d)", done, len(criteria))))
		for _, criterion := range criteria {
			box, colour := "[ ]", ""
			if criterion.Done {
				box, colour = "[x]", m.palette.Succeeded
			}
			for index, line := range wrapText(criterion.Text, inner-6) {
				if index == 0 {
					add("  %s %s", paint(colour, box), line)
					continue
				}
				add("      %s", line)
			}
		}
	}

	for _, heading := range []string{"Summary", "Problem Statement", "Implementation Notes", "Open Questions"} {
		body, ok := spec.Section(heading)
		if !ok || strings.TrimSpace(body) == "" {
			continue
		}
		blank()
		add("%s", paint(m.palette.Bold, heading))
		for _, line := range wrapText(stripUntrusted(body), inner-2) {
			add("  %s", line)
		}
	}

	history := sortRunsNewestFirst(card.Runs)
	blank()
	rule()
	if len(history) == 0 {
		add("%s", paint(m.palette.Dim, "No runs yet. Press d to dispatch an agent."))
		return out
	}
	add("%s %s", paint(m.palette.Bold, "Runs"), paint(m.palette.Dim, fmt.Sprintf("(%d)", len(history))))
	for _, run := range history {
		add("  %s %s %s %s %s",
			paint(m.palette.RunState(run.State), runGlyph(run.State)),
			paint(m.palette.RunState(run.State), pad(string(run.State), 10)),
			pad(run.Role, 26),
			pad(run.Kind, 9),
			paint(m.palette.Dim, shortDuration(run.Duration())))
		if run.Note != "" {
			for _, line := range wrapText(run.Note, inner-6) {
				add("      %s", paint(m.palette.Dim, line))
			}
		}
		if run.PendingPrompt != "" {
			for _, line := range wrapText("waiting on the harness's startup dialog — answer it in the pane (o), then hvb submits the task", inner-8) {
				add("      %s", paint(m.palette.Awaiting, line))
			}
		}
		if run.Worktree != nil {
			add("      %s %s", paint(m.palette.Dim, "branch"), run.Worktree.Branch)
			if run.Worktree.Removed {
				add("      %s", paint(m.palette.Dim, "worktree removed"))
			} else {
				add("      %s %s", paint(m.palette.Dim, "path  "), run.Worktree.Path)
			}
		}
		if pr := run.PullRequest; pr != nil {
			switch {
			case pr.Opened && pr.URL != "":
				add("      %s %s", paint(m.palette.Succeeded, "PR"), pr.URL)
			case pr.Reason != "":
				for _, line := range wrapText("no PR: "+pr.Reason, inner-8) {
					add("      %s", paint(m.palette.Warn, line))
				}
			}
		}
		if run.MovedTo != "" {
			add("      %s", paint(m.palette.Dim, "→ moved to "+run.MovedTo))
		}
		for _, comment := range run.Comments {
			add("      %s %s", paint(m.palette.Dim, comment.At.Local().Format("15:04")),
				paint(m.palette.Accent, comment.Author))
			for _, line := range wrapText(comment.Body, inner-8) {
				add("        %s", line)
			}
		}
		for _, problem := range run.LastErrors {
			add("      %s", paint(m.palette.Warn, "! "+problem))
		}
	}
	return out
}

// renderHelp draws the key reference and the environment summary.
func (m *Model) helpLines() []string {
	inner := m.width - 2
	var out []string
	add := func(format string, args ...any) {
		out = append(out, " "+truncate(fmt.Sprintf(format, args...), inner))
	}
	section := func(title string) {
		out = append(out, "")
		out = append(out, " "+paint(m.palette.Bold, title))
	}
	key := func(k, description string) {
		add("  %s  %s", paint(m.palette.Accent, pad(k, 12)), description)
	}

	add("%s", paint(m.palette.Bold, "hvb — VirtualBoard on Herdr"))

	section("Moving around")
	key("← → h l", "focus column")
	key("↑ ↓ k j", "focus card")
	key("⏎", "card detail")
	key("esc q", "back, then quit")

	section("Finding work")
	key("/ f", "search ID, title or body; filter by owner and labels")
	key("c", "clear search and filters")
	key("D", "collapse or expand completed cards")

	section("Changing the board")
	key("n", "new feature in backlog")
	key("m", "move the focused feature (lifecycle-checked)")
	key("H L", "shift the feature one column left or right")
	key("e", "set priority")
	key("r", "refresh from disk")

	section("Agents")
	key("d", "dispatch an agent onto the focused feature")
	key("o", "focus the running agent's pane")
	key("x", "cancel the active run")
	add("  %s", paint(m.palette.Dim, "moving a card into in-progress offers to start one,"))
	add("  %s", paint(m.palette.Dim, "in the project or in an isolated worktree branch"))

	section("The lifecycle")
	for _, status := range feature.Statuses {
		next := status.NextStatuses()
		labels := make([]string, len(next))
		for index, candidate := range next {
			labels[index] = string(candidate)
		}
		target := strings.Join(labels, ", ")
		if target == "" {
			target = "— terminal"
		}
		add("  %s → %s", paint(m.palette.Status(status), pad(string(status), 12)), target)
	}

	section("This board")
	add("  project   %s", m.backend.ProjectName())
	add("  owner     %s", m.backend.Owner())
	add("  harness   %s", m.backend.Config().Harness)
	add("  roles     %d charters", len(m.backend.Roles()))

	if len(m.problems) > 0 {
		section("Problems")
		for _, problem := range m.problems {
			for _, line := range wrapText(problem.Error(), inner-4) {
				add("  %s", paint(m.palette.Warn, line))
			}
		}
	}
	return out
}

// stripUntrusted removes the `<untrusted-content>` delimiters the VirtualBoard
// feature template wraps the spec body in. The board is showing the text to a
// human, who does not need the marker; the delimiters still matter in the
// dispatch prompt, where they are kept.
func stripUntrusted(text string) string {
	for _, marker := range []string{"<untrusted-content>", "</untrusted-content>"} {
		text = strings.ReplaceAll(text, marker, "")
	}
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// wrapText breaks text to width display columns on word boundaries.
func wrapText(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			if len(out) > 0 {
				out = append(out, "")
			}
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if displayWidth(line)+1+displayWidth(word) > width {
				out = append(out, line)
				line = word
				continue
			}
			line += " " + word
		}
		out = append(out, line)
	}
	return out
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" || value == feature.Unassigned {
		return "—"
	}
	return value
}

func (m *Model) renderHelp() []string {
	lines := m.helpLines()
	limit := max(0, len(lines)-(m.height-2))
	start := min(max(m.helpScroll, 0), limit)
	return lines[start:]
}
