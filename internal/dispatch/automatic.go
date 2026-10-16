package dispatch

import (
	"context"
	"fmt"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// AutoStart attempts the configured next action after an explicit board move.
// Dispatches are serialized project-wide, including manual starts, so the run
// limit and one-active-run-per-feature checks hold across processes.
func (d *Dispatcher) AutoStart(ctx context.Context, featureID string) (*runs.Run, error) {
	var run *runs.Run
	err := d.Store.WithRunTransaction(ctx, "@dispatch", func() error {
		spec, err := d.Workspace.FindSpec(featureID)
		if err != nil {
			return err
		}
		if spec.Status.Terminal() || !d.Config.Column(spec.Status).Auto || d.Config.MaxAutoStages == 0 {
			return nil
		}
		run, err = d.autoStart(ctx, spec, "", 1)
		return err
	})
	return run, err
}

func (d *Dispatcher) autoStart(ctx context.Context, spec *feature.Spec, parent string, depth int) (*runs.Run, error) {
	active, err := d.Store.Active()
	if err != nil {
		return nil, err
	}
	for _, run := range active {
		if run.FeatureID == spec.ID {
			return run, nil
		}
	}
	if d.Config.MaxAutoStages > 0 && depth > d.Config.MaxAutoStages {
		return nil, fmt.Errorf("automatic stage limit %d reached for %s; inspect it before starting another run", d.Config.MaxAutoStages, spec.ID)
	}
	if d.Config.MaxConcurrentRuns > 0 && len(active) >= d.Config.MaxConcurrentRuns {
		return nil, fmt.Errorf("automatic dispatch stopped at the concurrent run limit %d; start %s when capacity is available", d.Config.MaxConcurrentRuns, spec.ID)
	}
	return d.start(ctx, Request{Spec: spec, AutoParentID: parent, AutoDepth: depth})
}

// afterCompletion runs outside the source run's transaction, avoiding a lock
// inversion with forced dispatch/cancellation. A durable child reference makes
// duplicate outcome reports harmless even after the child has already ended.
func (d *Dispatcher) afterCompletion(ctx context.Context, completion *Completion, err error) (*Completion, error) {
	if err != nil || completion == nil || completion.Pending || completion.Run.Completion == nil {
		return completion, err
	}
	parentID := completion.Run.ID
	autoErr := d.Store.WithRunTransaction(ctx, "@dispatch", func() error {
		parent, err := d.Store.Get(parentID)
		if err != nil {
			return err
		}
		if parent.Completion.Cancelled || parent.Completion.AutoChecked {
			return nil
		}
		finish := func(child string, problem error) error {
			_, err := d.Store.Update(parentID, func(r *runs.Run) error {
				r.Completion.AutoChecked = true
				r.AutoChildID = child
				return nil
			})
			if err != nil {
				return err
			}
			return problem
		}
		history, err := d.Store.ForFeature(parent.FeatureID)
		if err != nil {
			return err
		}
		for _, child := range history {
			if child.AutoParentID == parentID {
				return finish(child.ID, nil)
			}
		}
		spec, err := d.Workspace.FindSpec(parent.FeatureID)
		if err != nil {
			return finish("", err)
		}
		if string(spec.Status) != parent.MovedTo || spec.Status.Terminal() || !d.Config.Column(spec.Status).Auto || d.Config.MaxAutoStages == 0 {
			return finish("", nil)
		}
		child, startErr := d.autoStart(ctx, spec, parentID, parent.AutoDepth+1)
		if child != nil {
			return finish(child.ID, startErr)
		}
		// A launch can fail after its queued run was recorded. Preserve that
		// evidence and do not relaunch it on every board refresh.
		history, err = d.Store.ForFeature(parent.FeatureID)
		if err != nil {
			return err
		}
		for _, child := range history {
			if child.AutoParentID == parentID {
				return finish(child.ID, startErr)
			}
		}
		return finish("", startErr)
	})
	if autoErr != nil {
		completion.AutomationError = autoErr.Error()
		d.note(parentID, "automatic handoff: "+autoErr.Error())
	}
	if latest, err := d.Store.Get(parentID); err == nil {
		completion.Run = latest
	}
	return completion, nil
}
