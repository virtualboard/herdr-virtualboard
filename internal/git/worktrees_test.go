package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestListWorktreesPreservesUnusualPathsAndMissingRegistrations(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(t.TempDir(), "space and\nnewline")
	run(t, repo.Root, "worktree", "add", "-q", "-b", "feature/FTR-0001/test", path)
	entries, err := repo.ListWorktrees(context.Background())
	if err != nil || len(entries) != 2 {
		t.Fatalf("list: %v %v", entries, err)
	}
	found := false
	for _, entry := range entries {
		if resolve(t, entry.Path) == resolve(t, path) {
			found = entry.Branch == "feature/FTR-0001/test"
		}
	}
	if !found {
		t.Fatalf("unusual path lost: %v", entries)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	entries, err = repo.ListWorktrees(context.Background())
	if err != nil || len(entries) != 2 {
		t.Fatalf("missing registration pruned: %v %v", entries, err)
	}
}

func TestParseWorktreesRejectsMalformedRecords(t *testing.T) {
	for _, raw := range []string{"", "branch refs/heads/main\x00", "worktree \x00", "worktree /a\x00worktree /b\x00"} {
		if _, err := parseWorktrees(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
