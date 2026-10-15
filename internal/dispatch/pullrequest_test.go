package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// gitRepo turns the harness's workspace root into a real git repository with a
// bare remote on disk, so push runs for real with no network.
func (h *harness) gitRepo(t *testing.T) string {
	t.Helper()
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git(h.root, "init", "-q", "-b", "main")
	git(h.root, "config", "user.email", "t@example.com")
	git(h.root, "config", "user.name", "T")
	git(h.root, "add", "-A")
	git(h.root, "commit", "-qm", "initial")

	bare := filepath.Join(t.TempDir(), "origin.git")
	git(t.TempDir(), "init", "-q", "--bare", bare)
	git(h.root, "remote", "add", "origin", bare)
	git(h.root, "push", "-q", "--set-upstream", "origin", "main")
	return bare
}

func (h *harness) commitOn(t *testing.T, branch, file string) {
	t.Helper()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = h.root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("checkout", "-q", "-B", branch)
	if err := os.WriteFile(filepath.Join(h.root, file), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "FTR-0001: "+file)
}

// worktreeRun attaches a worktree to a run, pointing at the harness repo
// itself: the PR path only cares that the directory is a checkout of the
// branch, which makes this an honest stand-in for a real worktree.
func (h *harness) worktreeRun(t *testing.T, branch string) *runs.Run {
	t.Helper()
	run := h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	updated, err := h.dispatcher.Store.Update(run.ID, func(r *runs.Run) error {
		r.Worktree = &runs.Worktree{
			Path: h.root, Branch: branch, Base: "main", Remote: "origin",
			WorkspaceID: "w9", RepoRoot: h.root,
		}
		r.PullRequest = &runs.PullRequest{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// The whole point of this path: the agent's work survives even when the pull
// request cannot be opened.
func TestSuccessfulRunPushesAndDegradesWithoutAForgeClient(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	bare := h.gitRepo(t)
	h.commitOn(t, "feature/FTR-0001/x", "retry.go")
	h.worktreeRun(t, "feature/FTR-0001/x")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "done")
	if err != nil {
		t.Fatal(err)
	}
	pr := completion.PullRequest
	if pr == nil {
		t.Fatal("a run that asked for a pull request must report an outcome")
	}
	if !pr.Pushed {
		t.Fatalf("the branch should have been pushed: %+v", pr)
	}
	// A bare path remote has no forge, so there is nothing to open — but the
	// run still succeeded and the feature still moved.
	if pr.Opened {
		t.Errorf("a local bare remote has no forge: %+v", pr)
	}
	if completion.Moved != feature.Review {
		t.Errorf("Moved = %q, want review — a failed PR must not block routing", completion.Moved)
	}
	if completion.Run.State != runs.Succeeded {
		t.Errorf("State = %q", completion.Run.State)
	}

	out, err := exec.Command("git", "-C", bare, "branch", "--list", "feature/FTR-0001/x").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("the branch did not reach the remote: %v %s", err, out)
	}
}

// An agent that reported success without committing has produced nothing to
// open a pull request for. That is a normal outcome, explained, not an error.
func TestNoCommitsMeansNoPullRequest(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.gitRepo(t)
	h.commitOn(t, "feature/FTR-0001/x", "unused.txt")
	// Reset the branch back onto main so it is level with the base.
	cmd := exec.Command("git", "reset", "--hard", "main")
	cmd.Dir = h.root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reset: %v %s", err, out)
	}
	h.worktreeRun(t, "feature/FTR-0001/x")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "")
	if err != nil {
		t.Fatal(err)
	}
	pr := completion.PullRequest
	if pr.Opened || pr.Pushed {
		t.Fatalf("nothing to push or open: %+v", pr)
	}
	if !strings.Contains(pr.Reason, "no commits") {
		t.Errorf("Reason = %q", pr.Reason)
	}
	if completion.Moved != feature.Review {
		t.Errorf("the feature should still move: %q", completion.Moved)
	}
}

// Uncommitted work means the branch does not contain everything the agent did,
// so a pull request from it would be misleading.
func TestDirtyWorktreeBlocksThePullRequest(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.gitRepo(t)
	h.commitOn(t, "feature/FTR-0001/x", "retry.go")
	if err := os.WriteFile(filepath.Join(h.root, "forgotten.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.worktreeRun(t, "feature/FTR-0001/x")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "")
	if err != nil {
		t.Fatal(err)
	}
	pr := completion.PullRequest
	if pr.Pushed || pr.Opened {
		t.Fatalf("a dirty worktree must not be published: %+v", pr)
	}
	if !completion.Pending || !strings.Contains(completion.TransitionError, "commit") || completion.Moved != "" {
		t.Errorf("dirty implementation must remain pending before publication or review: %+v", completion)
	}
}

// A failed run never opens a pull request, however many commits it made.
func TestFailedRunOpensNoPullRequest(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.gitRepo(t)
	h.commitOn(t, "feature/FTR-0001/x", "half-done.go")
	h.worktreeRun(t, "feature/FTR-0001/x")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeFailure, "could not finish")
	if err != nil {
		t.Fatal(err)
	}
	if completion.PullRequest != nil && completion.PullRequest.Pushed {
		t.Fatalf("a failed run must not push: %+v", completion.PullRequest)
	}
	if completion.Moved != feature.Blocked {
		t.Errorf("Moved = %q, want blocked", completion.Moved)
	}
}

// A run with no worktree has no branch, so a pull request is meaningless.
func TestPullRequestWithoutAWorktreeIsRefused(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	run := h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	if _, err := h.dispatcher.Store.Update(run.ID, func(r *runs.Run) error {
		r.PullRequest = &runs.PullRequest{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "")
	if err != nil {
		t.Fatal(err)
	}
	if completion.PullRequest.Opened {
		t.Fatal("there is no branch to open from")
	}
	if !strings.Contains(completion.PullRequest.Reason, "worktree") {
		t.Errorf("Reason = %q", completion.PullRequest.Reason)
	}
}

// Cleanup must never silently discard work that exists nowhere else.
func TestCleanupRefusesADirtyWorktree(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.gitRepo(t)
	h.commitOn(t, "feature/FTR-0001/x", "retry.go")
	h.worktreeRun(t, "feature/FTR-0001/x")
	if _, err := h.dispatcher.Store.Finish("r1", runs.Succeeded, "success", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "uncommitted.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := h.dispatcher.CleanupWorktree(context.Background(), "r1", false); err == nil {
		t.Fatal("cleanup should refuse while work is uncommitted")
	}
	if _, err := h.dispatcher.CleanupWorktree(context.Background(), "r1", true); err != nil {
		t.Fatalf("--force should discard it: %v", err)
	}
	run, err := h.dispatcher.Store.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if !run.Worktree.Removed {
		t.Error("the run should record that the worktree is gone")
	}
}

func TestCleanupRefusesWhileTheRunIsActive(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.gitRepo(t)
	h.worktreeRun(t, "main")
	if _, err := h.dispatcher.CleanupWorktree(context.Background(), "r1", false); err == nil {
		t.Fatal("cleanup should refuse while the run is still going")
	}
}

func TestCleanupRefusesWhenGitCannotInspectCheckout(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.worktreeRun(t, "main")
	if _, err := h.dispatcher.Store.Finish("r1", runs.Succeeded, "success", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dispatcher.CleanupWorktree(context.Background(), "r1", false); err == nil || !strings.Contains(err.Error(), "inspect worktree") {
		t.Fatalf("cleanup did not fail closed: %v", err)
	}
}

func TestCleanupMarksSharedHistoryAndStopsActiveRun(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	first := h.worktreeRun(t, "main")
	if _, err := h.dispatcher.Store.Finish(first.ID, runs.Succeeded, "success", ""); err != nil {
		t.Fatal(err)
	}
	second := h.addRun("r2", "FTR-0001", feature.Review, runs.Running, "w1:p2")
	if _, err := h.dispatcher.Store.Update(second.ID, func(r *runs.Run) error { wt := *first.Worktree; r.Worktree = &wt; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dispatcher.CleanupWorktree(context.Background(), first.ID, true); err == nil {
		t.Fatal("discarded a checkout shared by another active run")
	}
	if _, err := h.dispatcher.CleanupWorktree(context.Background(), second.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ID, second.ID} {
		run, err := h.dispatcher.Store.Get(id)
		if err != nil || !run.Worktree.Removed {
			t.Fatalf("stale checkout reference %s: %+v, %v", id, run, err)
		}
		if id == second.ID && run.State != runs.Cancelled {
			t.Fatal("forced cleanup left its run active")
		}
	}
}
