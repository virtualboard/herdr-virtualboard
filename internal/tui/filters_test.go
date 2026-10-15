package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

func TestSearchOwnerAndLabelsCombineWithoutMutatingBoard(t *testing.T) {
	first := spec("FTR-0001", "Retry uploader", feature.InProgress, "backend", "reliability")
	first.Owner = "Alice"
	first.Body = "Uses exponential backoff."
	second := spec("FTR-0002", "Retry download", feature.Backlog, "backend")
	second.Owner = "bob"
	third := spec("FTR-0003", "UI retry", feature.Review, "frontend")
	third.Owner = "alice"
	backend := newFakeBackend(first, second, third)
	model := newTestModel(t, backend, 140, 30)
	model.applyFilters(boardFilters{Search: "BACKOFF FTR-0001", Owner: "ALICE", Labels: []string{"BACKEND", "reliability"}})
	if model.totalCards() != 3 || len(model.Cards(feature.InProgress)) != 1 || model.Counts()[feature.Review] != 0 {
		t.Fatal("filters did not intersect")
	}
	if model.FocusedCard() == nil || model.FocusedCard().Spec.ID != "FTR-0001" {
		t.Fatal("filter did not focus the matching work")
	}
	if !strings.Contains(screen(model), "1/3") {
		t.Fatal("filtered count does not expose the hidden work")
	}
	model.applyFilters(boardFilters{Search: "does not exist"})
	if model.FocusedCard() != nil {
		t.Fatal("zero-match view retained a stale selectable card")
	}
	model.Handle(context.Background(), Key{Rune: 'c'})
	if model.totalCards() != 3 || len(model.Cards(feature.Backlog)) != 1 || len(backend.moves) != 0 {
		t.Fatal("clearing filters changed repository data")
	}
}

func TestFilterFormCancelsAndAppliesWithoutReload(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "Café search", feature.Backlog), spec("FTR-0002", "Other work", feature.Review))
	model := newTestModel(t, backend, 100, 25)
	before := model.lastLoad
	model.Handle(context.Background(), Key{Rune: '/'})
	if !strings.Contains(screen(model), "Search and filters") {
		t.Fatal("filter overlay not rendered")
	}
	for _, r := range "café" {
		model.Handle(context.Background(), Key{Rune: r})
	}
	model.Handle(context.Background(), Key{Name: KeyEsc})
	if model.filters.active() {
		t.Fatal("cancelling applied draft filters")
	}
	model.Handle(context.Background(), Key{Rune: 'f'})
	for _, r := range "CAFÉ" {
		model.Handle(context.Background(), Key{Rune: r})
	}
	model.Handle(context.Background(), Key{Name: KeyEnter})
	if len(model.Cards(feature.Backlog)) != 1 || len(model.Cards(feature.Review)) != 0 {
		t.Fatal("Unicode search did not match")
	}
	if model.lastLoad != before {
		t.Fatal("filtering triggered backend I/O")
	}
}

func TestUnassignedOwnersAndEveryLabelMustMatch(t *testing.T) {
	a := spec("FTR-0001", "Empty owner", feature.Backlog, "a", "b")
	b := spec("FTR-0002", "Explicit unassigned", feature.Review, "a")
	b.Owner = feature.Unassigned
	model := newTestModel(t, newFakeBackend(a, b), 140, 30)
	model.applyFilters(boardFilters{Owner: "unassigned", Labels: []string{"a", "b"}})
	if model.Counts()[feature.Backlog] != 1 || model.Counts()[feature.Review] != 0 {
		t.Fatal("owner/label filters are inconsistent")
	}
	model.applyFilters(boardFilters{Owner: "UNASSIGNED"})
	if model.Counts()[feature.Backlog]+model.Counts()[feature.Review] != 2 {
		t.Fatal("empty and unassigned owners differ")
	}
}

func TestCompletedCollapseRetainsCountsAndSurvivesRefresh(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "Active work", feature.InProgress), spec("FTR-0002", "Finished work", feature.Done))
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'D'})
	if !model.hideDone || len(model.Cards(feature.Done)) != 0 || model.Counts()[feature.Done] != 1 {
		t.Fatal("collapse lost completed work counts")
	}
	if strings.Contains(screen(model), "Finished work") || !strings.Contains(screen(model), "D to expand") {
		t.Fatal("collapse did not explain hidden work")
	}
	model.Reload(context.Background())
	if len(model.Cards(feature.Done)) != 0 {
		t.Fatal("refresh expanded completed work")
	}
	model.Handle(context.Background(), Key{Rune: 'D'})
	if len(model.Cards(feature.Done)) != 1 {
		t.Fatal("completed work could not be expanded")
	}
}

func TestFilteredFramesFitNarrowAndWideTerminals(t *testing.T) {
	for _, width := range []int{30, 50, 80, 108, 160} {
		model := newTestModel(t, newFakeBackend(spec("FTR-0001", "Feature", feature.Done)), width, 20)
		model.hideDone = true
		model.applyFilters(boardFilters{Search: "feature"})
		model.openFilters()
		lines := model.Render()
		if len(lines) != 20 {
			t.Fatalf("width %d: wrong frame height", width)
		}
		for _, line := range lines {
			if displayWidth(line) > width {
				t.Fatalf("width %d: overflow: %q", width, line)
			}
		}
	}
}

func TestHelpCanScrollToProblemsOnSmallTerminal(t *testing.T) {
	model := newTestModel(t, newFakeBackend(), 60, 15)
	model.problems = []error{fmt.Errorf("actionable run-store problem")}
	model.Handle(context.Background(), Key{Rune: '?'})
	model.Handle(context.Background(), Key{Name: KeyEnd})
	if !strings.Contains(screen(model), "actionable run-store problem") {
		t.Fatal("help clipped the diagnostic without a way to reach it")
	}
	model.Handle(context.Background(), Key{Name: KeyHome})
	if !strings.Contains(screen(model), "hvb — VirtualBoard on Herdr") {
		t.Fatal("help could not return to top")
	}
}

func TestNarrowHintsKeepSearchClearAndQuitVisible(t *testing.T) {
	model := newTestModel(t, newFakeBackend(spec("FTR-0001", "Example", feature.Backlog)), 30, 20)
	footer := model.renderFooter()
	for _, hint := range []string{"/ search", "? help", "q quit"} {
		if !strings.Contains(footer, hint) {
			t.Fatalf("narrow hint %q clipped: %s", hint, footer)
		}
	}
	model.applyFilters(boardFilters{Search: "example"})
	footer = model.renderFooter()
	if !strings.Contains(footer, "1/1") || !strings.Contains(footer, "c clear") {
		t.Fatalf("narrow filtering is not discoverable: %s", footer)
	}
	model.pendingLabel = "Dispatching FTR-0001"
	footer = model.renderFooter()
	if !strings.Contains(footer, "^C quit") {
		t.Fatalf("narrow pending action hides quit: %s", footer)
	}
}
