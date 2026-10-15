package tui

import (
	"fmt"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

type boardFilters struct {
	Search string
	Owner  string
	Labels []string
}

func (f boardFilters) active() bool { return f.Search != "" || f.Owner != "" || len(f.Labels) > 0 }

func (f boardFilters) matches(spec *feature.Spec) bool {
	owner := spec.Owner
	if !spec.Claimed() {
		owner = feature.Unassigned
	}
	if f.Owner != "" && !strings.EqualFold(f.Owner, owner) {
		return false
	}
	for _, label := range f.Labels {
		found := false
		for _, candidate := range spec.Labels {
			if strings.EqualFold(label, candidate) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Search == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{spec.ID, spec.Title, spec.Body, owner, strings.Join(spec.Labels, " ")}, " "))
	for _, word := range strings.Fields(strings.ToLower(f.Search)) {
		if !strings.Contains(haystack, word) {
			return false
		}
	}
	return true
}

func (f boardFilters) summary() string {
	parts := []string{"Filters"}
	if f.Search != "" {
		parts = append(parts, "search: "+f.Search)
	}
	if f.Owner != "" {
		parts = append(parts, "owner: "+f.Owner)
	}
	if len(f.Labels) > 0 {
		parts = append(parts, "labels: "+strings.Join(f.Labels, ","))
	}
	return strings.Join(parts, " · ") + " · c clear"
}

func (m *Model) rebuildFiltered() {
	columns := map[feature.Status][]*Card{}
	counts := map[feature.Status]int{}
	for _, status := range feature.Statuses {
		for _, card := range m.allColumns[status] {
			if !m.filters.matches(card.Spec) {
				continue
			}
			counts[status]++
			if status != feature.Done || !m.hideDone {
				columns[status] = append(columns[status], card)
			}
		}
	}
	m.columns, m.matchedCounts = columns, counts
}

func (m *Model) applyFilters(filters boardFilters) {
	focused := m.FocusedCard()
	id := ""
	if focused != nil {
		id = focused.Spec.ID
	}
	filters.Search = strings.TrimSpace(filters.Search)
	filters.Owner = strings.TrimSpace(filters.Owner)
	m.filters = filters
	m.rebuildFiltered()
	m.clampSelection()
	if id == "" || !m.focusFeature(id) {
		m.focusFirstPopulatedColumn()
	}
	m.setStatus("%s", m.filterMessage())
	if !filters.active() {
		m.setStatus("Filters cleared.")
	}
}

func (m *Model) totalCards() int {
	count := 0
	for _, cards := range m.allColumns {
		count += len(cards)
	}
	return count
}

func (m *Model) openFilters() {
	m.form = &form{
		title: "Search and filters",
		fields: []field{
			{Key: "search", Label: "search", Value: m.filters.Search, Help: "words match ID, title, body, owner, or labels"},
			{Key: "owner", Label: "owner", Value: m.filters.Owner, Help: "exact handle; unassigned includes empty owners"},
			{Key: "labels", Label: "labels", Value: strings.Join(m.filters.Labels, " "), Help: "space-separated labels; all must match"},
		},
		onSubmit: func(values map[string]string) error {
			m.applyFilters(boardFilters{Search: values["search"], Owner: values["owner"], Labels: strings.Fields(values["labels"])})
			return nil
		},
	}
	m.view = ViewFilters
}

func (m *Model) filterMessage() string {
	if m.compact() {
		matched := 0
		for _, count := range m.matchedCounts {
			matched += count
		}
		return fmt.Sprintf("%d/%d · / edit · c clear", matched, m.totalCards())
	}
	return m.filters.summary()
}
