package git

import (
	"context"
	"fmt"
	"strings"
)

// WorktreeEntry is Git's registration of a checkout, including missing paths.
// Listing never prunes registrations or refreshes the index.
type WorktreeEntry struct {
	Path     string
	Branch   string
	Prunable bool
}

func (r *Repo) ListWorktrees(ctx context.Context) ([]WorktreeEntry, error) {
	out, err := r.run(ctx, DefaultTimeout, "--no-optional-locks", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(out)
}

func parseWorktrees(out string) ([]WorktreeEntry, error) {
	var entries []WorktreeEntry
	var current *WorktreeEntry
	for _, field := range strings.Split(out, "\x00") {
		if field == "" {
			current = nil
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" {
			if current != nil || value == "" {
				return nil, fmt.Errorf("invalid git worktree record")
			}
			entries = append(entries, WorktreeEntry{Path: value})
			current = &entries[len(entries)-1]
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("git worktree field outside a record")
		}
		switch key {
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "prunable":
			current.Prunable = true
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("git worktree list returned no repository record")
	}
	return entries, nil
}
