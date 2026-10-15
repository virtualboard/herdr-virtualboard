package dispatch

import (
	"context"
	"fmt"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
)

// preflight performs only reads and runs before forced cancellation, claims,
// worktree creation, or agent launch. An unrelated invalid feature does not
// prevent dispatch: binary capabilities and this target are checked separately.
func (d *Dispatcher) preflight(ctx context.Context, req *Request) error {
	if req.Spec == nil {
		return fmt.Errorf("dispatch: no feature given")
	}
	if err := d.validateHarness(*req); err != nil {
		return err
	}
	if err := d.Herdr.Gate(ctx); err != nil {
		return err
	}
	if err := d.VB.CheckDispatchCompatibility(ctx); err != nil {
		return err
	}
	fresh, err := d.Workspace.FindSpec(req.Spec.ID)
	if err != nil {
		return err
	}
	if fresh.Status != req.Spec.Status {
		return fmt.Errorf("%s changed from %s to %s before dispatch; refresh and retry", fresh.ID, req.Spec.Status, fresh.Status)
	}
	if fresh.Status.Terminal() {
		return fmt.Errorf("cannot dispatch completed feature %s", fresh.ID)
	}
	if err := validateDispatchSpec(ctx, d.VB, fresh); err != nil {
		return err
	}
	review, err := d.reviewSource(fresh)
	if err != nil {
		return err
	}
	if review != nil && req.Worktree != nil && !*req.Worktree {
		return fmt.Errorf("review must inspect implementation commit %s in its worktree; omit --no-worktree", review.Revision.Commit)
	}
	req.Spec = fresh
	req.prepared = true
	return nil
}

func validateDispatchSpec(ctx context.Context, client *vb.Client, spec *feature.Spec) error {
	report, err := client.Validate(ctx, spec.ID, false)
	if err != nil {
		return fmt.Errorf("validate dispatch target %s: %w", spec.ID, err)
	}
	if !report.OK() {
		return fmt.Errorf("%s fails validation; run vb validate %s and resolve its diagnostics before dispatch", spec.ID, spec.ID)
	}
	entry, found := report.Features.Results[spec.ID]
	if !found || report.Features.Total != 1 || report.Features.Valid != 1 || report.Specs.Total != 0 || entry.Status != string(spec.Status) {
		return fmt.Errorf("vb validation did not confirm %s in %s; inspect vb validate %s before dispatch", spec.ID, spec.Status, spec.ID)
	}
	return nil
}
