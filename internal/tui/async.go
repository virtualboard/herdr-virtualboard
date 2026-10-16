package tui

import (
	"context"
	"fmt"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// Tasks perform I/O, then return a continuation for the UI goroutine. The
// worker never changes Model: keys, frames, forms and results share one owner.
type task struct {
	refresh bool
	reload  bool
	work    func(context.Context) (func(*Model), error)
	form    *form
}

type taskResult struct {
	task     task
	after    func(*Model)
	err      error
	snapshot *boardSnapshot
}

type boardSnapshot struct {
	specs    []*feature.Spec
	runs     []*runs.Run
	problems []error
}

type taskWorker struct {
	jobs    chan task
	results chan taskResult
	done    chan struct{}
}

func newTaskWorker(ctx context.Context, backend Backend) *taskWorker {
	// There can be at most one refresh and one action outstanding. Periodic
	// refreshes coalesce rather than piling up behind a slow command.
	worker := &taskWorker{jobs: make(chan task, 2), results: make(chan taskResult, 2), done: make(chan struct{})}
	go func() {
		defer close(worker.done)
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-worker.jobs:
				if ctx.Err() != nil {
					return
				}
				result := taskResult{task: job}
				if job.work != nil {
					result.after, result.err = job.work(ctx)
				}
				if ctx.Err() != nil {
					return
				}
				if job.refresh || job.reload {
					snapshot := loadSnapshot(ctx, backend)
					result.snapshot = &snapshot
				}
				select {
				case <-ctx.Done():
					return
				case worker.results <- result:
				}
			}
		}
	}()
	return worker
}

func (w *taskWorker) submit(job task) bool {
	select {
	case w.jobs <- job:
		return true
	default:
		return false
	}
}

func loadSnapshot(ctx context.Context, backend Backend) boardSnapshot {
	specs, allRuns, problems := backend.Load(ctx)
	snapshot := boardSnapshot{problems: append([]error(nil), problems...)}
	// Backend objects may be reused or mutated by the next operation. Freeze
	// the data before handing it to a concurrently rendering UI.
	for _, spec := range specs {
		if spec != nil {
			snapshot.specs = append(snapshot.specs, cloneSpec(spec))
		}
	}
	for _, run := range allRuns {
		if run != nil {
			snapshot.runs = append(snapshot.runs, cloneRun(run))
		}
	}
	return snapshot
}

func cloneSpec(spec *feature.Spec) *feature.Spec {
	copy := *spec
	copy.Labels = append([]string(nil), spec.Labels...)
	copy.Dependencies = append([]string(nil), spec.Dependencies...)
	return &copy
}

func copyPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneRun(run *runs.Run) *runs.Run {
	copy := *run
	copy.Worktree = copyPointer(run.Worktree)
	copy.PullRequest = copyPointer(run.PullRequest)
	copy.Completion = copyPointer(run.Completion)
	copy.Revision = copyPointer(run.Revision)
	copy.ReviewOf = copyPointer(run.ReviewOf)
	copy.EndedAt = copyPointer(run.EndedAt)
	copy.Comments = append([]runs.Comment(nil), run.Comments...)
	copy.LastErrors = append([]string(nil), run.LastErrors...)
	return &copy
}

func (m *Model) actionContext() context.Context {
	if m.inputContext != nil {
		return m.inputContext
	}
	return context.Background()
}

func (m *Model) perform(ctx context.Context, label string, reload bool, work func(context.Context) (func(*Model), error)) error {
	if m.enqueue == nil {
		after, err := work(ctx)
		if reload {
			snapshot := loadSnapshot(ctx, m.backend)
			m.applySnapshot(snapshot)
		}
		if after != nil {
			after(m)
		}
		return err
	}
	if m.pendingLabel != "" {
		return fmt.Errorf("%s is still pending; wait before starting another action", m.pendingLabel)
	}
	job := task{work: work, reload: reload}
	if m.view == ViewNewFeature {
		job.form = m.form
	}
	if !m.enqueue(job) {
		return fmt.Errorf("board worker is busy; retry when its current operation finishes")
	}
	m.pendingLabel = label
	return nil
}

func (m *Model) requestReload() {
	if m.loading || m.pendingLabel != "" {
		return
	}
	if m.enqueue(task{refresh: true}) {
		m.loading = true
	}
}

func (m *Model) accept(result taskResult) {
	if result.task.refresh {
		m.loading = false
	} else {
		m.pendingLabel = ""
	}
	if result.snapshot != nil {
		m.applySnapshot(*result.snapshot)
	}
	if result.err != nil {
		m.setError(result.err)
		if result.task.form != nil {
			result.task.form.err = result.err.Error()
			if m.view == ViewBoard {
				m.form, m.view = result.task.form, ViewNewFeature
			} else {
				m.failedForm = result.task.form
			}
		}
	}
	if result.after != nil {
		result.after(m)
	}
}

func (m *Model) pendingMessage() string {
	if m.pendingLabel != "" {
		return truncate(m.pendingLabel, max(5, m.width-12)) + " · ^C quit"
	}
	if m.loading {
		return "Refreshing… · ^C quit"
	}
	return ""
}

func (m *Model) offerMovedFeature(id string, status feature.Status) {
	if m.view != ViewBoard && m.view != ViewDetail {
		return
	}
	card := m.FocusedCard()
	if card != nil && card.Spec.ID == id {
		m.offerDispatch(card, status)
	}
}

func dispatchNotice(run *runs.Run) func(*Model) {
	saved := cloneRun(run)
	return func(model *Model) {
		if saved.Worktree != nil {
			model.setStatus("Dispatched %s as %s on %s", saved.FeatureID, saved.Role, saved.Worktree.Branch)
		} else {
			model.setStatus("Dispatched %s as %s in %s", saved.FeatureID, saved.Role, saved.PaneID)
		}
	}
}
