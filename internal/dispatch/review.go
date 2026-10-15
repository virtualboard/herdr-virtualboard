package dispatch

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/git"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

func (d *Dispatcher) reviewSource(spec *feature.Spec) (*runs.Run, error) {
	if spec.Status != feature.Review {
		return nil, nil
	}
	history, err := d.Store.ForFeature(spec.ID)
	if err != nil {
		return nil, err
	}
	for _, run := range history {
		if run.Status == feature.InProgress && run.Outcome == string(OutcomeSuccess) && run.Revision != nil && run.Worktree != nil {
			commit := run.Revision.Commit
			if len(commit) != 40 && len(commit) != 64 {
				return nil, fmt.Errorf("implementation run %s has an invalid revision", run.ID)
			}
			if _, err := hex.DecodeString(commit); err != nil {
				return nil, fmt.Errorf("implementation run %s has an invalid revision", run.ID)
			}
			return run, nil
		}
	}
	return nil, nil
}

func (d *Dispatcher) prepareReviewWorktree(ctx context.Context, spec *feature.Spec, source *runs.Run) (*runs.Worktree, error) {
	previous := source.Worktree
	if info, err := os.Stat(previous.Path); err == nil && info.IsDir() && !previous.Removed {
		if err := d.verifyReviewCheckout(ctx, previous.Path, previous.Branch, source.Revision.Commit); err != nil {
			return nil, err
		}
		opened, err := d.Herdr.OpenWorktree(ctx, previous.RepoRoot, previous.Branch, previous.Path, runs.TabLabel(spec.ID), false)
		if err != nil {
			return nil, err
		}
		copy := *previous
		copy.WorkspaceID = opened.Workspace.WorkspaceID
		return &copy, nil
	}
	// If cleanup removed the original checkout, rebuild at the immutable commit
	// instead of cutting a review branch from today's default branch.
	branch := fmt.Sprintf("review/%s/%s", spec.ID, source.Revision.Commit[:12])
	var path, workspaceID string
	if existing, _, err := d.Herdr.Worktrees(ctx, previous.RepoRoot); err == nil {
		for _, candidate := range existing {
			if candidate.Branch != branch || !candidate.IsLinked {
				continue
			}
			opened, err := d.Herdr.OpenWorktree(ctx, previous.RepoRoot, branch, candidate.Path, runs.TabLabel(spec.ID), false)
			if err != nil {
				return nil, err
			}
			path, workspaceID = candidate.Path, opened.Workspace.WorkspaceID
			break
		}
	}
	if path == "" {
		created, err := d.Herdr.CreateWorktree(ctx, previous.RepoRoot, branch, source.Revision.Commit, runs.TabLabel(spec.ID), false)
		if err != nil {
			return nil, fmt.Errorf("rebuild review checkout at %s: %w", source.Revision.Commit, err)
		}
		path = created.Worktree.Path
		if path == "" && created.Workspace.Worktree != nil {
			path = created.Workspace.Worktree.CheckoutPath
		}
		if path == "" {
			path = created.RootPane.CWD
		}
		workspaceID = created.Workspace.WorkspaceID
	}
	if err := d.verifyReviewCheckout(ctx, path, branch, source.Revision.Commit); err != nil {
		return nil, err
	}
	return &runs.Worktree{Path: path, Branch: branch, Base: previous.Base, Remote: previous.Remote, RepoRoot: previous.RepoRoot, WorkspaceID: workspaceID}, nil
}

func (d *Dispatcher) verifyReviewCheckout(ctx context.Context, path, branch, commit string) error {
	repo := &git.Repo{Root: path, Bin: d.GitBin}
	clean, err := repo.Clean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("review checkout %s has uncommitted changes; commit or preserve them before reviewing %s", path, commit)
	}
	head, err := repo.Head(ctx)
	if err != nil {
		return err
	}
	current, err := repo.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	if head != commit || current != branch {
		return fmt.Errorf("review requires %s at %s; checkout is %s at %s", branch, commit, current, head)
	}
	return nil
}
