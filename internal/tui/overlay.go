package tui

import (
	"fmt"
	"strings"
)

// picker is a single-choice list overlay: move destinations, roles, harnesses.
type picker struct {
	title    string
	subtitle string
	options  []option
	index    int
	// onChoose runs with the chosen option's value. Returning an error keeps
	// the picker open with the error shown, so a refused move does not lose
	// the user's place.
	onChoose func(value string) error
}

// option is one row of a picker.
type option struct {
	Value       string
	Label       string
	Description string
	// Disabled rows are shown but cannot be chosen — a lifecycle transition
	// VirtualBoard forbids is more useful visible and greyed out than absent,
	// because absence looks like a bug.
	Disabled bool
	Reason   string
}

func (p *picker) move(delta int) {
	if len(p.options) == 0 {
		return
	}
	for attempt := 0; attempt < len(p.options); attempt++ {
		p.index = (p.index + delta + len(p.options)) % len(p.options)
		if !p.options[p.index].Disabled {
			return
		}
	}
}

func (p *picker) current() (option, bool) {
	if p.index < 0 || p.index >= len(p.options) {
		return option{}, false
	}
	return p.options[p.index], true
}

// form is a small field-entry overlay: the new-feature dialog.
type form struct {
	title  string
	fields []field
	index  int
	// onSubmit receives the field values by key.
	onSubmit func(values map[string]string) error
	err      string
}

// field is one form input.
type field struct {
	Key   string
	Label string
	Value string
	Help  string
	// Choices makes the field a cycler rather than free text.
	Choices []string
	choice  int
}

func (f *form) current() *field {
	if f.index < 0 || f.index >= len(f.fields) {
		return nil
	}
	return &f.fields[f.index]
}

func (f *form) values() map[string]string {
	out := map[string]string{}
	for _, entry := range f.fields {
		out[entry.Key] = entry.Value
	}
	return out
}

func (f *form) moveField(delta int) {
	if len(f.fields) == 0 {
		return
	}
	f.index = (f.index + delta + len(f.fields)) % len(f.fields)
}

func (f *form) cycle(delta int) {
	current := f.current()
	if current == nil || len(current.Choices) == 0 {
		return
	}
	current.choice = (current.choice + delta + len(current.Choices)) % len(current.Choices)
	current.Value = current.Choices[current.choice]
}

func (f *form) typeRune(r rune) {
	current := f.current()
	if current == nil || len(current.Choices) > 0 {
		return
	}
	current.Value += string(r)
}

func (f *form) backspace() {
	current := f.current()
	if current == nil || len(current.Choices) > 0 || current.Value == "" {
		return
	}
	runes := []rune(current.Value)
	current.Value = string(runes[:len(runes)-1])
}

// confirmation is a yes/no overlay for an irreversible action.
type confirmation struct {
	title   string
	body    string
	confirm string
	onYes   func() error
}

// overlay composites the active overlay onto the board.
func (m *Model) overlay(background []string) []string {
	var box []string
	switch m.view {
	case ViewNewFeature, ViewFilters:
		box = m.renderForm()
	case ViewMovePicker, ViewDispatchPicker:
		box = m.renderPicker()
	case ViewConfirm:
		box = m.renderConfirm()
	default:
		return background
	}
	return composite(background, box, m.width, m.height-2)
}

// composite centres box over background.
func composite(background, box []string, width, height int) []string {
	if len(box) == 0 {
		return background
	}
	boxWidth := 0
	for _, line := range box {
		if w := displayWidth(line); w > boxWidth {
			boxWidth = w
		}
	}
	top := (height - len(box)) / 2
	if top < 0 {
		top = 0
	}
	left := (width - boxWidth) / 2
	if left < 0 {
		left = 0
	}

	out := make([]string, len(background))
	copy(out, background)
	for index, line := range box {
		row := top + index
		if row < 0 || row >= len(out) {
			continue
		}
		// Overlay rows are replaced rather than blended: computing which
		// background cells a variable-width, escape-laden line covers is a
		// source of off-by-one corruption, and the box is the focus anyway.
		out[row] = strings.Repeat(" ", left) + pad(line, boxWidth)
	}
	return out
}

// boxWidth is the overlay width, wide enough for a title and a hint line but
// narrow enough to sit inside a phone-width board.
func (m *Model) boxWidth() int {
	width := m.width - 8
	if width > 72 {
		width = 72
	}
	if width < 24 {
		width = m.width
	}
	return width
}

func (m *Model) boxTop(title string, width int) []string {
	return []string{
		paint(m.palette.Border, "┌"+strings.Repeat("─", width-2)+"┐"),
		paint(m.palette.Border, "│") + paint(m.palette.Bold, fit(" "+title, width-2)) + paint(m.palette.Border, "│"),
		paint(m.palette.Border, "├"+strings.Repeat("─", width-2)+"┤"),
	}
}

func (m *Model) boxRow(text string, width int) string {
	return paint(m.palette.Border, "│") + fit(" "+text, width-2) + paint(m.palette.Border, "│")
}

func (m *Model) boxBottom(hint string, width int) []string {
	return []string{
		paint(m.palette.Border, "├"+strings.Repeat("─", width-2)+"┤"),
		paint(m.palette.Border, "│") + paint(m.palette.Dim, fit(" "+hint, width-2)) + paint(m.palette.Border, "│"),
		paint(m.palette.Border, "└"+strings.Repeat("─", width-2)+"┘"),
	}
}

func (m *Model) renderPicker() []string {
	if m.picker == nil {
		return nil
	}
	width := m.boxWidth()
	out := m.boxTop(m.picker.title, width)
	if m.picker.subtitle != "" {
		out = append(out, m.boxRow(paint(m.palette.Dim, m.picker.subtitle), width), m.boxRow("", width))
	}
	// Size the label column to the longest label rather than a fixed width:
	// statuses are short and role keys are long, and the same picker renders
	// both. A fixed column either wastes half the box or truncates the reason
	// that explains why a row is disabled.
	labelWidth := 0
	for _, entry := range m.picker.options {
		if got := displayWidth(pickerLabel(entry)); got > labelWidth {
			labelWidth = got
		}
	}
	for index, entry := range m.picker.options {
		label := pad(pickerLabel(entry), labelWidth)
		detail := entry.Description
		if entry.Disabled {
			detail = entry.Reason
		}
		row := fmt.Sprintf("%s  %s", label, paint(m.palette.Dim, detail))
		switch {
		case entry.Disabled:
			row = "  " + paint(m.palette.Dim, fmt.Sprintf("%s  %s", label, detail))
		case index == m.picker.index:
			row = paint(m.palette.Invert, " ▸ "+row)
		default:
			row = "   " + row
		}
		out = append(out, m.boxRow(row, width))
	}
	return append(out, m.boxBottom("↑↓ choose · ⏎ confirm · esc cancel", width)...)
}

func pickerLabel(entry option) string {
	if entry.Label != "" {
		return entry.Label
	}
	return entry.Value
}

func (m *Model) renderForm() []string {
	if m.form == nil {
		return nil
	}
	width := m.boxWidth()
	out := m.boxTop(m.form.title, width)
	for index, entry := range m.form.fields {
		marker := "  "
		if index == m.form.index {
			marker = paint(m.palette.Accent, "▸ ")
		}
		value := entry.Value
		if value == "" {
			value = paint(m.palette.Dim, "—")
		}
		if index == m.form.index && len(entry.Choices) == 0 {
			value += paint(m.palette.Accent, "▏")
		}
		out = append(out, m.boxRow(fmt.Sprintf("%s%s %s", marker, paint(m.palette.Dim, pad(entry.Label, 11)), value), width))
		if index == m.form.index && entry.Help != "" {
			out = append(out, m.boxRow(paint(m.palette.Dim, "             "+entry.Help), width))
		}
	}
	if m.form.err != "" {
		out = append(out, m.boxRow("", width), m.boxRow(paint(m.palette.Warn, m.form.err), width))
	}
	hint := "tab field · ←→ cycle · ⏎ create · esc cancel"
	if m.view == ViewFilters {
		hint = "tab field · ⏎ apply · esc cancel"
	}
	return append(out, m.boxBottom(hint, width)...)
}

func (m *Model) renderConfirm() []string {
	if m.confirm == nil {
		return nil
	}
	width := m.boxWidth()
	out := m.boxTop(m.confirm.title, width)
	for _, line := range wrapText(m.confirm.body, width-4) {
		out = append(out, m.boxRow(line, width))
	}
	return append(out, m.boxBottom("y confirm · esc cancel", width)...)
}
