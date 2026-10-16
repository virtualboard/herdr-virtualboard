package runs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HVB_DATA_DIR", t.TempDir())
	store, err := Open("project1")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newRun(id, featureID string, state State) *Run {
	return &Run{
		ID: id, FeatureID: featureID, Title: featureID, Status: feature.InProgress,
		Role: "backend_dev", Kind: "claude", State: state, StartedAt: time.Now().UTC(),
	}
}

func TestAppendAndList(t *testing.T) {
	store := newStore(t)
	if err := store.Append(newRun("r1", "FTR-0001", Running)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.Append(newRun("r2", "FTR-0002", Succeeded)); err != nil {
		t.Fatal(err)
	}
	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d runs", len(all))
	}
	if all[0].ID != "r2" {
		t.Errorf("List must be newest first, got %s", all[0].ID)
	}
}

func TestActiveExcludesTerminalStates(t *testing.T) {
	store := newStore(t)
	for id, state := range map[string]State{
		"r1": Running, "r2": Queued, "r3": Awaiting,
		"r4": Succeeded, "r5": Failed, "r6": Cancelled,
	} {
		if err := store.Append(newRun(id, "FTR-0001", state)); err != nil {
			t.Fatal(err)
		}
	}
	active, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	// awaiting is not terminal: a run parked for human review still holds a
	// pane the board must keep watching.
	if len(active) != 3 {
		t.Fatalf("got %d active runs, want 3 (running, queued, awaiting): %v", len(active), states(active))
	}
}

// An agent's own report must not be overwritten by a later reconciliation that
// merely noticed the pane closed.
func TestFinishIsIdempotent(t *testing.T) {
	store := newStore(t)
	if err := store.Append(newRun("r1", "FTR-0001", Running)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish("r1", Succeeded, "success", "all criteria met"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish("r1", Failed, "pane_closed", "overwritten?"); err != nil {
		t.Fatal(err)
	}
	run, err := store.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != Succeeded || run.Outcome != "success" || run.Note != "all criteria met" {
		t.Fatalf("the first outcome was overwritten: %+v", run)
	}
}

func TestGetReportsNotFound(t *testing.T) {
	store := newStore(t)
	_, err := store.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
}

func TestLatestForFeature(t *testing.T) {
	store := newStore(t)
	old := newRun("r1", "FTR-0001", Failed)
	old.StartedAt = time.Now().UTC().Add(-time.Hour)
	if err := store.Append(old); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(newRun("r2", "FTR-0001", Running)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(newRun("r3", "FTR-0002", Running)); err != nil {
		t.Fatal(err)
	}
	latest, err := store.Latest("FTR-0001")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != "r2" {
		t.Fatalf("Latest = %s, want r2", latest.ID)
	}
}

// A dispatched agent calling `hvb run done` and a TUI refresh reconciling can
// write at the same moment; neither may lose the other's change.
func TestConcurrentUpdatesDoNotLoseWrites(t *testing.T) {
	store := newStore(t)
	if err := store.Append(newRun("r1", "FTR-0001", Running)); err != nil {
		t.Fatal(err)
	}
	const writers = 12
	var wg sync.WaitGroup
	wg.Add(writers)
	for index := 0; index < writers; index++ {
		go func(n int) {
			defer wg.Done()
			if _, err := store.AddComment("r1", "agent", fmt.Sprintf("note %d", n)); err != nil {
				t.Errorf("AddComment: %v", err)
			}
		}(index)
	}
	wg.Wait()
	run, err := store.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Comments) != writers {
		t.Fatalf("got %d comments, want %d — a write was lost", len(run.Comments), writers)
	}
}

// Corrupt data must survive reads and failed writes until explicitly recovered.
func TestCorruptFileRequiresExplicitRecovery(t *testing.T) {
	store := newStore(t)
	if err := writeFile(store.Path(), "{not json at all"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("corrupt run store was silently discarded")
	}
	if err := store.Append(newRun("r1", "FTR-0001", Running)); err == nil {
		t.Fatal("write replaced corrupt data")
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil || string(raw) != "{not json at all" {
		t.Fatalf("original store lost: %q, %v", raw, err)
	}
}

func TestPruneKeepsActiveRunsAndTheNewest(t *testing.T) {
	store := newStore(t)
	base := time.Now().UTC().Add(-time.Hour)
	// One ancient active run plus more finished runs than the cap.
	ancient := newRun("active", "FTR-0001", Running)
	ancient.StartedAt = base
	if err := store.Append(ancient); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxRunsPerFeature+5; index++ {
		run := newRun(fmt.Sprintf("old%d", index), "FTR-0001", Succeeded)
		run.StartedAt = base.Add(time.Duration(index+1) * time.Minute)
		if err := store.Append(run); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ForFeature("FTR-0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) > MaxRunsPerFeature {
		t.Fatalf("got %d runs, want at most %d", len(all), MaxRunsPerFeature)
	}
	found := false
	for _, run := range all {
		if run.ID == "active" {
			found = true
		}
	}
	if !found {
		t.Fatal("pruning dropped an active run")
	}
}

func TestAgentNameSatisfiesHerdrsGrammar(t *testing.T) {
	// Herdr requires [a-z][a-z0-9_-]{0,31}, unique among live agents.
	cases := []string{"FTR-0001", "FTR-9999", "ftr-0042"}
	for _, featureID := range cases {
		runID := NewID(featureID)
		name := AgentName(featureID, runID)
		if len(name) == 0 || len(name) > 32 {
			t.Errorf("%s: name %q has length %d", featureID, name, len(name))
		}
		if name[0] < 'a' || name[0] > 'z' {
			t.Errorf("%s: name %q must start with a lowercase letter", featureID, name)
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				t.Errorf("%s: name %q contains %q", featureID, name, r)
			}
		}
	}
}

func TestNewIDIsUniqueWithinASecond(t *testing.T) {
	seen := map[string]bool{}
	for index := 0; index < 200; index++ {
		id := NewID("FTR-0001")
		if seen[id] {
			t.Fatalf("duplicate run id %q", id)
		}
		seen[id] = true
	}
}

func TestTabLabelIsStablePerFeature(t *testing.T) {
	// Every run on a feature must reuse one tab, so three dispatches do not
	// leave three tabs behind.
	if TabLabel("FTR-0001") != TabLabel("FTR-0001") {
		t.Fatal("TabLabel must be deterministic")
	}
	if !strings.Contains(TabLabel("FTR-0001"), "ftr-0001") {
		t.Fatalf("TabLabel = %q", TabLabel("FTR-0001"))
	}
}

func states(list []*Run) []State {
	out := make([]State, len(list))
	for index, run := range list {
		out[index] = run.State
	}
	return out
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestOpenAndListAbsentStoreDoNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent-data")
	t.Setenv("HVB_DATA_DIR", dir)
	store, err := Open("read-only")
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("List: %v, %v", list, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created data directory: %v", err)
	}
	if err := store.Append(newRun("r1", "FTR-0001", Running)); err != nil {
		t.Fatal(err)
	}
	list, err = store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("first mutation: %v, %v", list, err)
	}
}
