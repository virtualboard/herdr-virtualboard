package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	gitrepo "github.com/virtualboard/herdr-virtualboard/internal/git"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

var featurePaneLabel = regexp.MustCompile(`(?i)^ftr-[0-9]{4}(?:$|[ ·-])`)
var featureBranch = regexp.MustCompile(`^(?:feature|review)/FTR-[0-9]{4}(?:/|$)`)

// resourceChecks observes resources without reconciling, stopping panes, renewing
// leases, or pruning worktrees. Missing evidence is a warning, never a repair.
func resourceChecks(ctx context.Context, app *App, herdrReady, vbReady bool) []check {
	history, err := app.Store().List()
	if err != nil {
		return nil
	} // The run-store check already explains the failure.
	var checks []check
	knownPanes := map[string]bool{}
	knownPaths := map[string]bool{}
	roots := []string{app.Workspace().Root}
	specs, problems := app.Workspace().LoadSpecs()
	featureIDs := map[string]bool{}
	for _, spec := range specs {
		featureIDs[spec.ID] = true
	}
	if len(problems) > 0 {
		checks = append(checks, check{Name: "run specifications", Detail: fmt.Sprintf("%d specifications could not be read", len(problems)), Remedy: "Run vb validate and restore invalid specifications before interpreting missing-feature warnings."})
	}
	for _, run := range history {
		if run.PaneID != "" {
			knownPanes[run.PaneID] = true
		}
		if run.AnchorPane != "" {
			knownPanes[run.AnchorPane] = true
		}
		if run.Worktree != nil && !run.Worktree.Removed {
			path := canonicalPath(run.Worktree.Path)
			knownPaths[path] = true
			roots = append(roots, path)
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				checks = append(checks, check{Name: "retained worktree", Detail: run.ID + ": checkout is missing or unreadable", Remedy: "Inspect hvb run show " + run.ID + " and Git worktree registrations; restore the checkout or explicitly clean up this run after review."})
			}
		}
		if !run.Active() {
			continue
		}
		if run.State == runs.Awaiting && !run.LeaseReleased {
			checks = append(checks, check{Name: "awaiting lease cleanup", Detail: run.ID + ": lease release has not been confirmed", Remedy: "Refresh the board or inspect hvb run show " + run.ID + "; cancellation is explicit and never removes a successor lease."})
		}
		if !featureIDs[run.FeatureID] {
			checks = append(checks, check{Name: "run feature", Detail: run.ID + ": feature is missing or unreadable", Remedy: "Inspect the run and restore its specification from version control, or explicitly cancel the run."})
		}
		if run.Completion != nil && !run.Completion.Done && !run.Completion.Cancelled {
			checks = append(checks, check{Name: "pending completion", Detail: run.ID + ": accepted outcome has unfinished effects", Remedy: "Inspect hvb run show " + run.ID + "; resume with hvb run retry " + run.ID + "."})
		}
		// Awaiting runs deliberately give back their lease for human intervention.
		if vbReady && run.State != runs.Awaiting && (run.Completion == nil || !run.Completion.Released) {
			lease, err := app.VB().LockStatus(ctx, run.FeatureID)
			detail := ""
			switch {
			case err != nil:
				detail = "lease could not be inspected"
			case lease == nil:
				detail = "lease is absent"
			case lease.Expired:
				detail = "lease has expired"
			case run.LockToken == "":
				detail = "run has no ownership token"
			case lease.Token != run.LockToken || lease.Owner != run.LockOwner:
				detail = "lease no longer matches this run"
			default:
				expiry, err := time.Parse(time.RFC3339, lease.ExpiresAt)
				if err != nil {
					detail = "lease expiry is invalid"
				} else if !expiry.After(time.Now()) {
					detail = "lease has expired"
				}
			}
			if detail != "" {
				checks = append(checks, check{Name: "run lease", Detail: run.ID + ": " + detail, Remedy: "Inspect the current owner and agent before cancelling or retrying; do not force-release another run's lease."})
			}
		}
	}
	if herdrReady {
		panes, err := app.Herdr().Panes(ctx, "")
		if err != nil {
			checks = append(checks, check{Name: "pane inventory", Detail: "Herdr panes could not be listed", Remedy: "Inspect Herdr connectivity and rerun hvb doctor; no pane was changed."})
		} else {
			checks = append(checks, paneResourceChecks(history, panes, knownPanes, roots)...)
		}
	}
	repo, err := gitrepo.Open(ctx, app.Workspace().Root)
	if err != nil {
		checks = append(checks, check{Name: "worktree inventory", Detail: "Git repository could not be inspected", Remedy: "Run doctor from the project checkout; no worktree was changed."})
		return checks
	}
	entries, err := repo.ListWorktrees(ctx)
	if err != nil {
		checks = append(checks, check{Name: "worktree inventory", Detail: "Git worktree registrations could not be listed", Remedy: "Inspect git worktree list --porcelain; no registration was pruned."})
		return checks
	}
	registered := map[string]bool{}
	for _, entry := range entries {
		path := canonicalPath(entry.Path)
		registered[path] = true
		if path != canonicalPath(repo.Root) && !knownPaths[path] && featureBranch.MatchString(entry.Branch) {
			checks = append(checks, check{Name: "untracked worktree", Detail: entry.Path + " (" + entry.Branch + ") has no retained run record", Remedy: "Inspect its branch and contents. It may belong to standalone feature work; no cleanup is implied."})
		}
	}
	for _, run := range history {
		if run.Worktree != nil && !run.Worktree.Removed && !registered[canonicalPath(run.Worktree.Path)] {
			checks = append(checks, check{Name: "worktree registration", Detail: run.ID + ": retained checkout is not registered with this repository", Remedy: "Inspect hvb run show " + run.ID + " and git worktree list before explicit recovery or cleanup."})
		}
	}
	return checks
}

func paneResourceChecks(history []*runs.Run, panes []herdrcli.Pane, known map[string]bool, roots []string) []check {
	var checks []check
	live := map[string]bool{}
	for _, pane := range panes {
		live[pane.PaneID] = true
		if known[pane.PaneID] || !featurePaneLabel.MatchString(pane.Label) {
			continue
		}
		for _, root := range roots {
			if withinRoot(pane.CWD, root) || withinRoot(pane.ForegroundCWD, root) {
				checks = append(checks, check{Name: "untracked pane", Detail: pane.PaneID + ": feature-labelled pane has no run record", Remedy: "Inspect the pane and its owner before taking action; it may be a manually managed agent."})
				break
			}
		}
	}
	for _, run := range history {
		if !run.Active() || run.State == runs.Queued || run.Completion != nil {
			continue
		}
		if run.PaneID == "" || !live[run.PaneID] {
			checks = append(checks, check{Name: "run pane", Detail: run.ID + ": recorded agent pane is missing", Remedy: "Inspect hvb run show " + run.ID + "; report the observed outcome or explicitly cancel the run."})
		}
	}
	return checks
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func withinRoot(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	relative, err := filepath.Rel(canonicalPath(root), canonicalPath(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
