package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
)

// Outcome is what a finished agent reports.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomeBlocked Outcome = "blocked"
)

// ParseOutcome resolves an outcome name.
func ParseOutcome(value string) (Outcome, bool) {
	switch Outcome(strings.ToLower(strings.TrimSpace(value))) {
	case OutcomeSuccess:
		return OutcomeSuccess, true
	case OutcomeFailure:
		return OutcomeFailure, true
	case OutcomeBlocked:
		return OutcomeBlocked, true
	default:
		return "", false
	}
}

// Completion is the result of finishing a run.
type Completion struct {
	Run *runs.Run `json:"run"`
	// PullRequest is the outcome of opening one, when the run asked for it.
	PullRequest *runs.PullRequest `json:"pull_request,omitempty"`
	// Moved is the status the feature was transitioned to, empty when the
	// column routed nowhere or the transition was refused.
	Moved feature.Status `json:"moved,omitempty"`
	// TransitionError explains a refused transition. The run still ends:
	// the agent did its work, and a routing rule the lifecycle forbids is a
	// configuration problem, not a reason to lose the outcome.
	TransitionError string `json:"transition_error,omitempty"`
	Pending         bool   `json:"pending,omitempty"`
	AutomationError string `json:"automation_error,omitempty"`
}

// Complete ends a run, applies the column's routing, and releases the lock.
//
// It is the single place where an agent's report turns into a board change, and
// it is idempotent: a run that already ended keeps its first outcome.
func (d *Dispatcher) Complete(ctx context.Context, runID string, outcome Outcome, note string) (*Completion, error) {
	var completion *Completion
	err := d.Store.WithRunTransaction(ctx, runID, func() error {
		var err error
		completion, err = d.complete(ctx, runID, outcome, note)
		return err
	})
	return d.afterCompletion(ctx, completion, err)
}

func (d *Dispatcher) complete(ctx context.Context, runID string, outcome Outcome, note string) (*Completion, error) {
	run, err := d.Store.Get(runID)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() && (run.Completion == nil || run.Completion.Done) {
		return &Completion{Run: run, Moved: feature.Status(run.MovedTo), PullRequest: run.PullRequest}, nil
	}
	if run.State == runs.Awaiting && run.Completion == nil {
		run, err = d.reclaimAwaitingLease(ctx, run)
		if err != nil {
			return nil, err
		}
	}
	if run.Completion == nil {
		target, _ := d.route(run, outcome)
		run, err = d.Store.Update(runID, func(r *runs.Run) error {
			if r.State.Terminal() {
				return nil
			}
			r.State = runs.Succeeded
			if outcome != OutcomeSuccess {
				r.State = runs.Failed
			}
			now := time.Now().UTC()
			r.EndedAt, r.Outcome, r.Note = &now, string(outcome), note
			r.Completion = &runs.CompletionProgress{Target: string(target)}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if run.Completion == nil {
		return &Completion{Run: run}, nil
	}
	outcome = Outcome(run.Outcome) // the first accepted report owns all retries
	update := func(apply func(*runs.Run)) error {
		var err error
		run, err = d.Store.Update(runID, func(r *runs.Run) error { apply(r); return nil })
		return err
	}
	pending := func(err error) (*Completion, error) {
		if saveErr := update(func(r *runs.Run) { r.Completion.LastError = err.Error() }); saveErr != nil {
			return nil, errors.Join(err, saveErr)
		}
		return &Completion{Run: run, PullRequest: run.PullRequest, Moved: feature.Status(run.MovedTo), TransitionError: err.Error(), Pending: true}, nil
	}
	if !run.Completion.Linked || !run.Completion.Routed {
		if err := d.refreshLease(ctx, run); err != nil {
			return pending(fmt.Errorf("completion lease check: %w", err))
		}
	}
	if !run.Completion.Captured {
		var revision *runs.Revision
		if outcome == OutcomeSuccess && run.Worktree != nil {
			repo := d.repoFor(run)
			clean, err := repo.Clean(ctx)
			if err != nil {
				return pending(fmt.Errorf("inspect completed worktree: %w", err))
			}
			if !clean {
				return pending(fmt.Errorf("commit the worktree changes before completing %s, then run `hvb run retry`", run.FeatureID))
			}
			branch, err := repo.CurrentBranch(ctx)
			if err != nil {
				return pending(err)
			}
			if branch != run.Worktree.Branch {
				return pending(fmt.Errorf("worktree changed branches: expected %s, found %s", run.Worktree.Branch, branch))
			}
			commit, err := repo.Head(ctx)
			if err != nil {
				return pending(err)
			}
			if run.ReviewOf != nil && commit != run.ReviewOf.Commit {
				return pending(fmt.Errorf("review checkout changed from %s to %s; review the assigned implementation revision", run.ReviewOf.Commit, commit))
			}
			revision = &runs.Revision{Commit: commit, Branch: branch, Repository: run.Worktree.RepoRoot}
		}
		if err := update(func(r *runs.Run) { r.Revision = revision; r.Completion.Captured = true }); err != nil {
			return nil, err
		}
	}
	if !run.Completion.Published {
		pr := run.PullRequest
		if outcome == OutcomeSuccess && pr != nil && !pr.Opened {
			pr = d.openPullRequest(ctx, run)
		}
		if err := update(func(r *runs.Run) { r.PullRequest = pr; r.Completion.Published = true }); err != nil {
			return nil, err
		}
	}
	if !run.Completion.Linked {
		if err := d.recordPullRequestLink(ctx, run, run.PullRequest); err != nil {
			return pending(err)
		}
		if err := update(func(r *runs.Run) { r.Completion.Linked = true }); err != nil {
			return nil, err
		}
	}
	if !run.Completion.Routed {
		target := feature.Status(run.Completion.Target)
		if target != "" {
			if _, err := d.transition(ctx, run, target); err != nil {
				return pending(err)
			}
		}
		if err := update(func(r *runs.Run) { r.MovedTo = string(target); r.Completion.Routed = true }); err != nil {
			return nil, err
		}
	}
	if !run.Completion.Indexed {
		if run.Completion.Target != "" {
			if err := d.VB.Index(ctx); err != nil {
				return pending(fmt.Errorf("index regeneration failed: %w", err))
			}
		}
		if err := update(func(r *runs.Run) { r.Completion.Indexed = true }); err != nil {
			return nil, err
		}
	}
	if !run.Completion.Released {
		if err := d.releaseRecordedLease(ctx, run); err != nil {
			return pending(fmt.Errorf("release feature lease: %w", err))
		}
		if err := update(func(r *runs.Run) { r.Completion.Released = true; r.LeaseReleased = true }); err != nil {
			return nil, err
		}
	}
	if err := update(func(r *runs.Run) { r.Completion.Done = true; r.Completion.LastError = "" }); err != nil {
		return nil, err
	}
	return &Completion{Run: run, PullRequest: run.PullRequest, Moved: feature.Status(run.MovedTo)}, nil
}

// Retry resumes unfinished effects and can retry a degraded publishing attempt.
// It never changes the agent's accepted outcome or repeats a recorded move.
func (d *Dispatcher) Retry(ctx context.Context, runID string) (*Completion, error) {
	var completion *Completion
	err := d.Store.WithRunTransaction(ctx, runID, func() error {
		run, err := d.Store.Get(runID)
		if err != nil {
			return err
		}
		if run.Completion == nil {
			return fmt.Errorf("run %s has no resumable completion", runID)
		}
		if run.Completion.Cancelled {
			return fmt.Errorf("run %s completion was cancelled; start a new run to resume work", runID)
		}
		if _, err := d.Store.Update(runID, func(r *runs.Run) error {
			if !r.Completion.Routed {
				target, _ := d.route(r, Outcome(r.Outcome))
				r.Completion.Target = string(target)
			}
			if r.AutoChildID == "" {
				r.Completion.AutoChecked = false
			}
			if r.PullRequest != nil && !r.PullRequest.Opened {
				r.Completion.Published = false
				r.Completion.Linked = false
			}
			r.Completion.Done = false
			return nil
		}); err != nil {
			return err
		}
		completion, err = d.complete(ctx, runID, Outcome(run.Outcome), run.Note)
		return err
	})
	return d.afterCompletion(ctx, completion, err)
}

// route picks the destination status for a finished run. A `blocked` outcome
// goes to `blocked` when the lifecycle allows it, regardless of the column's
// failure route — an agent that says it is blocked is reporting a fact about
// the world, not a verdict on the work.
func (d *Dispatcher) route(run *runs.Run, outcome Outcome) (feature.Status, bool) {
	column := d.Config.Column(run.Status)
	var want string
	switch outcome {
	case OutcomeSuccess:
		want = column.OnSuccess
	case OutcomeBlocked:
		if feature.CanTransition(run.Status, feature.Blocked) {
			return feature.Blocked, true
		}
		want = column.OnFailure
	default:
		want = column.OnFailure
	}
	if want == "" {
		return "", false
	}
	status, ok := feature.ParseStatus(want)
	return status, ok
}

// transition moves the feature through vb, from wherever it actually is now.
//
// The run recorded the status it started from, but a human may have moved the
// feature while the agent worked. vb is re-read here so the transition is
// validated against the truth rather than against a stale assumption.
func (d *Dispatcher) transition(ctx context.Context, run *runs.Run, target feature.Status) (feature.Status, error) {
	spec, err := d.Workspace.FindSpec(run.FeatureID)
	if err != nil {
		return "", err
	}
	if spec.Status == target {
		return target, nil
	}
	if !feature.CanTransition(spec.Status, target) {
		return "", fmt.Errorf("cannot move %s from %s to %s: VirtualBoard does not allow that transition", run.FeatureID, spec.Status, target)
	}
	// Hand the feature back when it leaves an agent's hands, so the board
	// does not show work as owned by an agent that has stopped.
	owner := vb.ClearOwner
	if target == feature.InProgress {
		owner = d.Config.ResolveOwner()
	}
	if _, err := d.vbForRun(run).Move(ctx, run.FeatureID, target, owner); err != nil {
		return "", err
	}

	return target, nil
}

// Cancel stops a run and closes its pane.
func (d *Dispatcher) Cancel(ctx context.Context, runID, note string) (*runs.Run, error) {
	var run *runs.Run
	err := d.Store.WithRunTransaction(ctx, runID, func() error {
		var err error
		run, err = d.cancel(ctx, runID, note)
		return err
	})
	return run, err
}

func (d *Dispatcher) cancel(ctx context.Context, runID, note string) (*runs.Run, error) {
	run, err := d.Store.Get(runID)
	if err != nil {
		return nil, err
	}
	if !run.Active() {
		return run, nil
	}
	// Pane cleanup stays ungated on purpose: hvb must always be able to tidy
	// up a pane it created, even against a Herdr it would refuse to dispatch
	// into.
	if run.PaneID != "" {
		if err := d.Herdr.ClosePane(ctx, run.PaneID); err != nil {
			// A previously closed pane is safe to forget only when a fresh
			// inventory confirms it is gone. An API failure proves nothing.
			panes, listErr := d.Herdr.Panes(ctx, "")
			alive := listErr != nil
			for _, pane := range panes {
				alive = alive || pane.PaneID == run.PaneID
			}
			if alive {
				return nil, fmt.Errorf("could not stop run %s; its lease was retained: %w", runID, errors.Join(err, listErr))
			}
		}
	}
	if run.AnchorPane != "" {
		_ = d.Herdr.ClosePane(ctx, run.AnchorPane)
	}
	if err := d.releaseRecordedLease(ctx, run); err != nil {
		return nil, fmt.Errorf("run stopped but lease release failed; retry cancellation: %w", err)
	}
	if run.State.Terminal() && run.Completion != nil {
		return d.Store.Update(runID, func(r *runs.Run) error {
			r.LeaseReleased = true
			r.Completion.Released = true
			r.Completion.Done = true
			r.Completion.Cancelled = true
			r.Completion.AutoChecked = true
			r.Completion.LastError = "remaining completion effects cancelled: " + note
			return nil
		})
	}
	return d.Store.Update(runID, func(r *runs.Run) error {
		r.LeaseReleased = true
		if !r.State.Terminal() {
			now := time.Now().UTC()
			r.State = runs.Cancelled
			r.EndedAt = &now
			r.Outcome = "cancelled"
			r.Note = note
		}
		return nil
	})
}

// Focus brings a run's pane into view.
func (d *Dispatcher) Focus(ctx context.Context, runID string) error {
	run, err := d.Store.Get(runID)
	if err != nil {
		return err
	}
	if run.PaneID == "" {
		return fmt.Errorf("run %s has no pane", runID)
	}
	if run.AgentName != "" {
		if err := d.Herdr.FocusAgent(ctx, run.AgentName); err == nil {
			return nil
		}
	}
	return d.Herdr.FocusPane(ctx, run.PaneID)
}

// Reconcile re-reads Herdr and settles every active run against what is really
// there. It is what makes the board honest without a daemon: the TUI calls it
// on each refresh, and the CLI calls it before listing runs.
//
// Three things can have happened to an active run since hvb last looked:
//
//   - its pane is gone — the user closed it, or the harness exited. The run
//     ended without a report; that is `awaiting`, not a failure.
//   - the harness reports `done` but no outcome arrived. Also `awaiting`: the
//     agent stopped and only a human knows whether it succeeded.
//   - the column's timeout elapsed. The run is failed and routed accordingly.
func (d *Dispatcher) Reconcile(ctx context.Context) ([]*runs.Run, error) {
	active, err := d.Store.Active()
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	var changed []*runs.Run
	var liveRuns []*runs.Run
	for _, run := range active {
		if run.State == runs.Awaiting && run.Completion == nil {
			updated, err := d.awaiting(ctx, run, run.Outcome, run.Note)
			if err != nil {
				return changed, err
			}
			if updated != nil {
				changed = append(changed, updated)
			}
			continue
		}
		if run.Completion != nil && !run.Completion.Done {
			completion, err := d.Complete(ctx, run.ID, Outcome(run.Outcome), run.Note)
			if err != nil {
				return changed, err
			}
			changed = append(changed, completion.Run)
			continue
		}
		liveRuns = append(liveRuns, run)
	}
	if len(liveRuns) == 0 {
		return changed, nil
	}
	panes, err := d.Herdr.Panes(ctx, "")
	if err != nil {
		// Without a pane list there is nothing trustworthy to reconcile
		// against; leaving runs alone is strictly better than guessing
		// they died.
		return nil, fmt.Errorf("reconcile: list panes: %w", err)
	}
	live := map[string]herdrcli.Pane{}
	for _, pane := range panes {
		live[pane.PaneID] = pane
	}
	agents, err := d.Herdr.Agents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: list agents: %w", err)
	}
	agentByPane := map[string]herdrcli.Agent{}
	for _, agent := range agents {
		agentByPane[agent.PaneID] = agent
	}

	for _, run := range liveRuns {
		updated, err := d.reconcileOne(ctx, run, live, agentByPane)
		if err != nil {
			return changed, err
		}
		if updated != nil {
			changed = append(changed, updated)
		}
	}
	return changed, nil
}

func (d *Dispatcher) reconcileOne(ctx context.Context, run *runs.Run, live map[string]herdrcli.Pane, agents map[string]herdrcli.Agent) (*runs.Run, error) {
	if run.Completion != nil && !run.Completion.Done {
		completion, err := d.Complete(ctx, run.ID, Outcome(run.Outcome), run.Note)
		if err != nil {
			return nil, err
		}
		return completion.Run, nil
	}
	if run.ID != "" && run.ID == d.SelfRunID {
		// The agent running this very command owns this run. Whatever Herdr
		// says about its pane, it is alive: it is the thing asking.
		return nil, nil
	}
	if run.State == runs.Queued && run.PaneID == "" {
		// A queued run with no pane never got off the ground; leave it for
		// the dispatch path to finish or fail.
		return nil, nil
	}
	if _, alive := live[run.PaneID]; !alive {
		updated, err := d.awaiting(ctx, run, "pane_closed", "the run's pane is gone; no outcome was reported")
		return updated, err
	}
	if column := d.Config.Column(run.Status); column.Timeout.Duration() > 0 {
		if time.Since(run.StartedAt) > column.Timeout.Duration() {
			note := fmt.Sprintf("run exceeded the %s timeout for %s", column.Timeout.Duration(), run.Status)
			// Stop the process before releasing its lease or routing the card;
			// recording failure alone would leave an untracked writer running.
			if run.PaneID != "" {
				if err := d.Herdr.ClosePane(ctx, run.PaneID); err != nil {
					return nil, fmt.Errorf("stop timed-out run: %w", err)
				}
			}
			if _, err := d.Complete(ctx, run.ID, OutcomeFailure, note); err != nil {
				return nil, err
			}
			return d.Store.Get(run.ID)
		}
	}
	if err := d.refreshLease(ctx, run); err != nil {
		if closeErr := d.Herdr.ClosePane(ctx, run.PaneID); closeErr != nil {
			return nil, fmt.Errorf("lease lost and run could not be stopped: %v; %w", err, closeErr)
		}
		return d.awaiting(ctx, run, "lease_lost", "feature lease lost; agent stopped: "+err.Error())
	}
	agent, ok := agents[run.PaneID]
	if !ok {
		return nil, nil
	}
	// A task parked because the harness was blocked at startup goes in as
	// soon as the agent is ready for input. This is what makes a worktree
	// dispatch self-heal after the user answers a trust prompt.
	if run.PendingPrompt != "" {
		switch agent.AgentStatus {
		case herdrcli.StatusIdle, herdrcli.StatusDone:
			return d.submitPending(ctx, run)
		}
		return nil, nil
	}
	if agent.AgentStatus == herdrcli.StatusDone && run.State == runs.Running {
		return d.awaiting(ctx, run, "agent_done", "the harness finished its turn without reporting an outcome")
	}
	return nil, nil
}

// submitPending sends a prompt that was parked while the harness was blocked.
func (d *Dispatcher) submitPending(ctx context.Context, run *runs.Run) (*runs.Run, error) {
	target := run.AgentName
	if target == "" {
		target = run.PaneID
	}
	if err := d.Herdr.PromptAgent(ctx, target, run.PendingPrompt, false, 0); err != nil {
		if herdrcli.IsAgentBlocked(err) {
			// Still at a dialog; try again on the next pass.
			return nil, nil
		}
		d.note(run.ID, fmt.Sprintf("could not submit the parked task: %v", err))
		return nil, nil
	}
	return d.Store.Update(run.ID, func(r *runs.Run) error {
		r.PendingPrompt = ""
		return nil
	})
}

// awaiting parks a run for human review without routing the feature. The
// feature keeps its status and its owner: a run that stopped unexplained has
// not earned a transition in either direction.
func (d *Dispatcher) awaiting(ctx context.Context, run *runs.Run, outcome, note string) (*runs.Run, error) {
	var updated *runs.Run
	err := d.Store.WithRunTransaction(ctx, run.ID, func() error {
		current, err := d.Store.Get(run.ID)
		if errors.Is(err, runs.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.State.Terminal() || (current.State == runs.Awaiting && current.LeaseReleased) {
			return nil
		}
		if current.State != runs.Awaiting {
			current, err = d.Store.Update(run.ID, func(r *runs.Run) error {
				now := time.Now().UTC()
				r.State = runs.Awaiting
				r.EndedAt = &now
				r.Outcome = outcome
				r.Note = note
				return nil
			})
			if err != nil {
				return err
			}
		}
		updated, err = d.finishAwaitingLease(ctx, current)
		return err
	})
	return updated, err
}
