package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// Render produces the whole frame: exactly height lines, each at most width
// display columns. It is pure, so the golden tests drive it directly.
func (m *Model) Render() []string {
	if m.width < minWidth || m.height < minHeight {
		return []string{truncate("Terminal too small for the board.", m.width)}
	}
	switch m.view {
	case ViewHelp:
		return m.frame(m.renderHelp())
	case ViewDetail:
		return m.frame(m.renderDetail())
	case ViewNewFeature, ViewFilters, ViewMovePicker, ViewDispatchPicker, ViewConfirm:
		// Overlays draw on top of the board so the user keeps their place.
		return m.frame(m.overlay(m.renderBoard()))
	default:
		return m.frame(m.renderBoard())
	}
}

// frame wraps a body in the header and footer and pads to the full height.
func (m *Model) frame(body []string) []string {
	out := make([]string, 0, m.height)
	out = append(out, m.renderHeader())
	bodyHeight := m.height - 2
	for index := 0; index < bodyHeight; index++ {
		if index < len(body) {
			out = append(out, body[index])
			continue
		}
		out = append(out, "")
	}
	out = append(out, m.renderFooter())
	return out
}

func (m *Model) renderHeader() string {
	counts := m.Counts()
	total := 0
	for _, count := range counts {
		total += count
	}
	left := paint(m.palette.Bold, " VirtualBoard ") + paint(m.palette.Accent, m.backend.ProjectName())

	var segments []string
	for _, status := range feature.Statuses {
		if counts[status] == 0 && m.compact() {
			continue
		}
		segments = append(segments, paint(m.palette.Status(status),
			fmt.Sprintf("%s %d", shortStatus(status), counts[status])))
	}
	right := strings.Join(segments, paint(m.palette.Border, " · "))
	if active := m.ActiveRunCount(); active > 0 {
		right = paint(m.palette.Running, fmt.Sprintf("▶%d ", active)) + right
	}
	if m.filters.active() {
		right += fmt.Sprintf("  %d/%d", total, m.totalCards())
	} else {
		right += fmt.Sprintf("  %d", total)
	}

	gap := m.width - displayWidth(left) - displayWidth(right) - 1
	if gap < 1 {
		return fit(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

func (m *Model) renderFooter() string {
	if message, isError, ok := m.footerMessage(); ok {
		colour := m.palette.Dim
		if isError {
			colour = m.palette.Warn
		}
		return paint(colour, fit(" "+message, m.width))
	}
	return paint(m.palette.Dim, fit(" "+m.keyHint(), m.width))
}

func (m *Model) footerMessage() (string, bool, bool) {
	if message := m.pendingMessage(); message != "" {
		return message, false, true
	}
	if message, isError := m.statusMessage(); message != "" {
		return message, isError, true
	}
	if len(m.problems) > 0 {
		return fmt.Sprintf("%d spec(s) could not be read — press ? for detail", len(m.problems)), true, true
	}
	if m.filters.active() {
		return m.filterMessage(), false, true
	}
	return "", false, false
}

func (m *Model) keyHint() string {
	switch m.view {
	case ViewDetail:
		if m.width < 50 {
			return "esc back · ? help"
		}
		return "esc back · e edit · d dispatch · m move · o focus pane · x cancel run · ? help"
	case ViewHelp:
		if m.width < 50 {
			return "↑↓ scroll · esc back"
		}
		return "↑↓/pgup/pgdn scroll · esc back"
	default:
		if m.width < 50 {
			return "/ search · ? help · q quit"
		}
		if m.compact() {
			return "←→ cols · ↑↓ cards · / search · ? help · q quit"
		}
		return "←→ cols · ↑↓ cards · ⏎ detail · / search · f filters · D done · n new · m move · d dispatch · ? help · q quit"
	}
}

// compact reports whether the board is on a narrow terminal — a phone, a
// split pane — and should show one column at a time instead of five.
func (m *Model) compact() bool { return m.width < compactWidth }

// compactWidth is where five columns stop being readable: each needs about 22
// columns plus borders.
const compactWidth = 108

func (m *Model) renderBoard() []string {
	if m.compact() {
		return m.renderSingleColumn()
	}
	return m.renderColumns()
}

// renderColumns draws all five lifecycle columns side by side.
func (m *Model) renderColumns() []string {
	statuses := feature.Statuses
	height := m.height - 2
	widths := splitWidth(m.width, len(statuses))

	rendered := make([][]string, len(statuses))
	for index, status := range statuses {
		rendered[index] = m.renderColumn(status, widths[index], height, index == m.column)
	}

	out := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		for index := range statuses {
			if row < len(rendered[index]) {
				line.WriteString(rendered[index][row])
			} else {
				line.WriteString(strings.Repeat(" ", widths[index]))
			}
		}
		out[row] = line.String()
	}
	return out
}

// renderSingleColumn is the narrow layout: one column, with the lifecycle shown
// as a breadcrumb so the user still knows where they are.
func (m *Model) renderSingleColumn() []string {
	status := m.FocusedStatus()
	height := m.height - 2
	body := m.renderColumn(status, m.width, height-1, true)

	var crumbs []string
	for index, candidate := range feature.Statuses {
		label := shortStatus(candidate)
		if index == m.column {
			label = paint(m.palette.Invert, " "+label+" ")
		} else {
			label = paint(m.palette.Dim, " "+label+" ")
		}
		crumbs = append(crumbs, label)
	}
	out := make([]string, 0, height)
	out = append(out, fit(strings.Join(crumbs, ""), m.width))
	out = append(out, body...)
	return out
}

// renderColumn draws one lifecycle column into a fixed width and height.
//
// The body is built at the inner width and the separator is appended to every
// row at the end, including the blank ones. Appending it only to rows that have
// content leaves the vertical rule stopping below the last card, which reads as
// a broken frame rather than an empty column.
func (m *Model) renderColumn(status feature.Status, width, height int, focused bool) []string {
	cards := m.Cards(status)
	inner := width - 1
	if inner < 8 {
		inner = width
	}

	body := make([]string, 0, height)
	title := fmt.Sprintf("%s %d", strings.ToUpper(status.Title()), m.matchedCounts[status])
	if status == feature.Done && m.hideDone {
		title += " ▸"
	}
	titleColour := m.palette.Status(status)
	if focused {
		titleColour += m.palette.Bold
	}
	body = append(body, paint(titleColour, fit(title, inner)))
	body = append(body, paint(m.palette.Border, strings.Repeat("─", inner)))

	if column := m.backend.Config().Column(status); column.Auto {
		body = append(body, paint(m.palette.Dim, fit(" auto → "+column.OnSuccess, inner)))
	}

	if status == feature.Done && m.hideDone {
		body = append(body, paint(m.palette.Dim, fit(" D to expand completed", inner)))
	}
	if visible := height - len(body); visible > 0 && !(status == feature.Done && m.hideDone) {
		selected := m.card[status]
		start := scrollWindow(selected, len(cards), visible/cardHeight)
		for index := start; index < len(cards); index++ {
			block := m.renderCard(cards[index], inner, focused && index == selected)
			if len(body)+len(block) > height {
				break
			}
			body = append(body, block...)
		}
		if len(cards) == 0 && len(body) < height {
			body = append(body, paint(m.palette.Dim, fit(" —", inner)))
		}
	}

	out := make([]string, height)
	gutter := m.gutter(width, inner)
	for index := 0; index < height; index++ {
		line := strings.Repeat(" ", inner)
		if index < len(body) {
			line = body[index]
		}
		out[index] = line + gutter
	}
	return out
}

// cardHeight is how many rows one card occupies: title, meta, spacer.
const cardHeight = 3

func (m *Model) gutter(width, inner int) string {
	if width <= inner {
		return ""
	}
	return paint(m.palette.Border, strings.Repeat("│", 1)) + strings.Repeat(" ", width-inner-1)
}

// renderCard draws one card as three rows.
func (m *Model) renderCard(card *Card, width int, selected bool) []string {
	marker := " "
	markerColour := ""
	if card.Active != nil {
		marker, markerColour = runGlyph(card.Active.State), m.palette.RunState(card.Active.State)
	} else if latest := latestRun(card.Runs); latest != nil {
		marker, markerColour = runGlyph(latest.State), m.palette.Dim
	}

	id := card.Spec.ID
	if strings.HasPrefix(id, "FTR-") {
		id = id[4:]
	}
	head := fmt.Sprintf("%s %s %s", paint(markerColour, marker), paint(m.palette.Dim, id),
		truncate(card.Spec.Title, width-len(id)-4))

	var meta []string
	if card.Spec.Priority != "" {
		meta = append(meta, paint(m.palette.Priority(card.Spec.Priority), card.Spec.Priority))
	}
	if card.Spec.Complexity != "" {
		meta = append(meta, paint(m.palette.Dim, card.Spec.Complexity))
	}
	if card.Spec.Claimed() {
		meta = append(meta, paint(m.palette.Accent, "@"+card.Spec.Owner))
	}
	if card.Active != nil {
		label := card.Active.Role + " " + shortDuration(card.Active.Duration())
		if card.Active.Worktree != nil {
			// A worktree run is editing a different directory from the one
			// the user is looking at; that is worth one character.
			label = "⑂ " + label
		}
		meta = append(meta, paint(m.palette.RunState(card.Active.State), label))
	} else if latest := latestRun(card.Runs); latest != nil && latest.PullRequest != nil && latest.PullRequest.Opened {
		meta = append(meta, paint(m.palette.Succeeded, "PR"))
	} else if criteria := card.Spec.AcceptanceCriteria(); len(criteria) > 0 {
		done := 0
		for _, criterion := range criteria {
			if criterion.Done {
				done++
			}
		}
		meta = append(meta, paint(m.palette.Dim, fmt.Sprintf("%d/%d", done, len(criteria))))
	}
	metaLine := "   " + strings.Join(meta, " ")

	lines := []string{fit(head, width), fit(metaLine, width)}
	if selected {
		for index, line := range lines {
			lines[index] = paint(m.palette.Invert, line)
		}
	}
	// A blank row between cards, at the same width so the column's separator
	// lands in the same place on every line.
	return append(lines, strings.Repeat(" ", width))
}

// splitWidth divides total across count columns, giving the remainder to the
// leftmost columns so the whole width is used exactly.
func splitWidth(total, count int) []int {
	base := total / count
	extra := total % count
	widths := make([]int, count)
	for index := range widths {
		widths[index] = base
		if index < extra {
			widths[index]++
		}
	}
	return widths
}

// scrollWindow returns the first index to draw so selected stays visible.
func scrollWindow(selected, total, visible int) int {
	if visible <= 0 || total <= visible {
		return 0
	}
	start := selected - visible/2
	if start < 0 {
		start = 0
	}
	if start > total-visible {
		start = total - visible
	}
	return start
}

func shortStatus(status feature.Status) string {
	switch status {
	case feature.Backlog:
		return "BACK"
	case feature.InProgress:
		return "WIP"
	case feature.Blocked:
		return "BLOCK"
	case feature.Review:
		return "REVIEW"
	case feature.Done:
		return "DONE"
	default:
		return strings.ToUpper(string(status))
	}
}

func runGlyph(state runs.State) string {
	switch state {
	case runs.Running:
		return "▶"
	case runs.Queued:
		return "·"
	case runs.Awaiting:
		return "?"
	case runs.Succeeded:
		return "✓"
	case runs.Failed:
		return "✗"
	case runs.Cancelled:
		return "⊘"
	default:
		return " "
	}
}

func latestRun(list []*runs.Run) *runs.Run {
	sorted := sortRunsNewestFirst(list)
	if len(sorted) == 0 {
		return nil
	}
	return sorted[0]
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
