package runs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryPreservesCorruptBytes(t *testing.T) {
	store := newStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{broken JSON")
	if err := os.WriteFile(store.Path(), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := store.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(backup)
	if err != nil || string(raw) != string(corrupt) {
		t.Fatalf("quarantine lost original data: %q, %v", raw, err)
	}
	runs, err := store.List()
	if err != nil || len(runs) != 0 {
		t.Fatalf("recovered store unusable: %v, %v", runs, err)
	}
}

func TestFutureRunStoreCannotBeOverwrittenOrRecovered(t *testing.T) {
	store := newStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf(`{"version":%d,"runs":[]}`, version+1))
	if err := os.WriteFile(store.Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("future schema silently accepted")
	}
	if err := store.Append(newRun("r1", "FTR-0001", Running)); err == nil {
		t.Fatal("future schema overwritten")
	}
	if _, err := store.Recover(context.Background()); err == nil {
		t.Fatal("recovery discarded a future schema")
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || string(after) != string(raw) {
		t.Fatalf("future data changed: %s, %v", after, err)
	}
}

func TestPruningRetainsWorktreeCleanupRecords(t *testing.T) {
	store := newStore(t)
	old := newRun("retained", "FTR-0001", Succeeded)
	old.StartedAt = time.Now().Add(-24 * time.Hour)
	old.Worktree = &Worktree{Path: "/retained/checkout"}
	if err := store.Append(old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxRunsPerFeature+5; i++ {
		if err := store.Append(newRun(fmt.Sprintf("new-%d", i), "FTR-0001", Succeeded)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Get("retained"); err != nil {
		t.Fatalf("cleanup ownership was pruned: %v", err)
	}
}
