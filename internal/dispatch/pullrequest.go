package dispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/forge"
	"github.com/virtualboard/herdr-virtualboard/internal/git"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// openPullRequest pushes a successful run's branch and opens a pull request.
//
// Nothing here is allowed to fail the run. The agent has already done the work
// and committed it; losing that outcome because a token was missing or a remote
// was unreachable would be absurd. Every failure becomes a Reason on the run
// that the board shows, alongside a compare URL wherever one can be built.
func (d *Dispatcher) openPullRequest(ctx context.Context, run *runs.Run) *runs.PullRequest {
	result := &runs.PullRequest{}
	if run.Worktree == nil {
		result.Reason = "the run did not use a worktree, so there is no branch to open a pull request from"
		return result
	}

	repo := d.repoFor(run)
	worktree := run.Worktree

	// An agent that left changes uncommitted has not finished, and a pull
	// request built from that branch would silently omit them. Say so rather
	// than opening something misleading.
	if clean, err := repo.Clean(ctx); err == nil && !clean {
		result.Reason = fmt.Sprintf("the worktree at %s has uncommitted changes — "+
			"commit or discard them, then push and open the pull request by hand", worktree.Path)
		return result
	}

	ahead, err := repo.CommitsAhead(ctx, worktree.Branch, worktree.Base)
	if err != nil {
		result.Reason = fmt.Sprintf("could not compare %s with %s: %v", worktree.Branch, worktree.Base, err)
		return result
	}
	if ahead == 0 {
		// A perfectly ordinary outcome: the agent decided nothing needed
		// changing, or reported success without committing anything.
		result.Reason = fmt.Sprintf("%s has no commits beyond %s — nothing to open a pull request for",
			worktree.Branch, worktree.Base)
		return result
	}

	remoteURL, err := repo.RemoteURL(ctx, worktree.Remote)
	if err != nil {
		result.Reason = fmt.Sprintf("the repository has no %q remote, so the branch cannot be published (%d commit(s) are waiting on %s)",
			worktree.Remote, ahead, worktree.Branch)
		return result
	}
	remote, err := git.ParseRemote(remoteURL)
	if err != nil {
		result.Reason = fmt.Sprintf("could not read the remote URL %q: %v", remoteURL, err)
		return result
	}

	if err := repo.Push(ctx, worktree.Remote, worktree.Branch); err != nil {
		result.Reason = fmt.Sprintf("pushing %s to %s failed: %v", worktree.Branch, worktree.Remote, err)
		return result
	}
	result.Pushed = true

	body := forge.Body(d.pullRequestBody(ctx, run, repo, ahead))
	opened := forge.Open(ctx, forge.Request{
		Remote: remote,
		Base:   worktree.Base,
		Head:   worktree.Branch,
		Title:  forge.TitleFor(run.FeatureID, run.Title),
		Body:   body,
		Draft:  d.Config.Forge.Draft,
	}, forge.Options{
		Token:   d.Config.Forge.ResolveToken(),
		BaseURL: d.Config.Forge.BaseURL,
		Kind:    git.Kind(strings.ToLower(d.Config.Forge.Kind)),
	})

	result.Opened, result.URL, result.Number, result.Reason = opened.Opened, opened.URL, opened.Number, opened.Reason
	return result
}

// pullRequestBody gathers what the description is built from. The spec is
// re-read rather than taken from the run, so the criteria reflect any boxes the
// agent ticked while it worked.
func (d *Dispatcher) pullRequestBody(ctx context.Context, run *runs.Run, repo *git.Repo, ahead int) forge.BodyInput {
	input := forge.BodyInput{
		FeatureID: run.FeatureID,
		Title:     run.Title,
		Role:      run.Role,
		Harness:   run.Kind,
		RunID:     run.ID,
	}
	if spec, err := d.Workspace.FindSpec(run.FeatureID); err == nil {
		input.Title = spec.Title
		input.Status = string(spec.Status)
		input.SpecPath = d.Workspace.Rel(spec.Path)
		if summary, ok := spec.Section("Summary"); ok {
			input.Summary = stripUntrustedMarkers(summary)
		}
		for _, criterion := range spec.AcceptanceCriteria() {
			input.Criteria = append(input.Criteria, forge.Criterion{Text: criterion.Text, Done: criterion.Done})
		}
	}
	if subjects, err := repo.Log(ctx, run.Worktree.Branch, run.Worktree.Base, maxCommitsInBody); err == nil {
		input.Commits = subjects
	}
	if ahead > maxCommitsInBody {
		input.Commits = append(input.Commits, fmt.Sprintf("…and %d more", ahead-maxCommitsInBody))
	}
	return input
}

// maxCommitsInBody caps the commit list so a long-running branch does not
// produce a pull-request description nobody will read.
const maxCommitsInBody = 20

// recordPullRequestLink writes the pull request into the feature spec, through
// vb, so the link survives in the repository rather than only in hvb's local
// run store.
func (d *Dispatcher) recordPullRequestLink(ctx context.Context, run *runs.Run, pr *runs.PullRequest) error {
	if pr == nil || !pr.Opened || pr.URL == "" {
		return nil
	}
	spec, err := d.Workspace.FindSpec(run.FeatureID)
	if err != nil {
		return err
	}
	existing, _ := spec.Section("Links")
	if strings.Contains(existing, pr.URL) {
		return nil
	}
	entry := fmt.Sprintf("- Pull request: %s", pr.URL)
	updated := entry
	if trimmed := strings.TrimSpace(existing); trimmed != "" {
		updated = trimmed + "\n" + entry
	}
	if err := d.vbForRun(run).Update(ctx, run.FeatureID, nil, map[string]string{"Links": updated}); err != nil {
		return fmt.Errorf("could not write the pull-request link into the spec: %w", err)
	}
	return nil
}

func stripUntrustedMarkers(text string) string {
	for _, marker := range []string{"<untrusted-content>", "</untrusted-content>"} {
		text = strings.ReplaceAll(text, marker, "")
	}
	return strings.TrimSpace(text)
}

// CleanupWorktree removes a finished run's checkout and closes its workspace.
//
// It refuses while work is uncommitted unless forced, because that work exists
// nowhere else — the whole point of the worktree is that it is separate from
// the main checkout.
func (d *Dispatcher) CleanupWorktree(ctx context.Context, runID string, force bool) (*runs.Run, error) {
	var run *runs.Run
	err := d.Store.WithRunTransaction(ctx, "@dispatch", func() error {
		var err error
		run, err = d.cleanupWorktree(ctx, runID, force)
		return err
	})
	return run, err
}

func (d *Dispatcher) cleanupWorktree(ctx context.Context, runID string, force bool) (*runs.Run, error) {
	run, err := d.Store.Get(runID)
	if err != nil {
		return nil, err
	}
	if run.Worktree == nil {
		return nil, fmt.Errorf("run %s did not use a worktree", runID)
	}
	if run.Worktree.Removed {
		return run, nil
	}
	allRuns, err := d.Store.List()
	if err != nil {
		return nil, err
	}
	for _, other := range allRuns {
		if other.ID != run.ID && other.Active() && other.Worktree != nil && other.Worktree.Path == run.Worktree.Path {
			return nil, fmt.Errorf("checkout is still used by active run %s; cancel that run before cleanup", other.ID)
		}
	}
	if run.Active() && !force {
		return nil, fmt.Errorf("run %s is still %s — cancel it first, or pass --force", runID, run.State)
	}
	if run.Active() {
		if _, err := d.Cancel(ctx, runID, "cancelled for forced worktree cleanup"); err != nil {
			return nil, err
		}
	}
	if !force {
		repo := d.repoFor(run)
		clean, err := repo.Clean(ctx)
		if err != nil {
			return nil, fmt.Errorf("inspect worktree before cleanup: %w", err)
		}
		if !clean {
			return nil, fmt.Errorf("the worktree at %s has uncommitted changes; pass --force to discard them",
				run.Worktree.Path)
		}
	}
	if err := d.Herdr.RemoveWorktree(ctx, run.Worktree.WorkspaceID, force); err != nil {
		return nil, fmt.Errorf("remove the worktree: %w", err)
	}
	// Implementation and review can share a checkout. Mark every reference
	// after removal so history does not offer cleanup for an absent resource.
	for _, other := range allRuns {
		if other.Worktree != nil && other.Worktree.Path == run.Worktree.Path {
			if _, err := d.Store.Update(other.ID, func(r *runs.Run) error {
				r.Worktree.Removed = true
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}
	return d.Store.Get(runID)
}
