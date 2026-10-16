package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// A failed/cancelled RPC can leave the harness already running. Stop the
// recorded pane before releasing its lease or marking the run as terminal.
// Failed cleanup retains the active ledger entry so reconciliation and cancel
// can retry, rather than hiding an agent which can still write.
func (d *Dispatcher) failDispatch(ctx context.Context, runID string, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	run, err := d.Store.Get(runID)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("dispatch state could not be read; lease retained: %w", err))
	}
	d.note(runID, "dispatch interrupted: "+cause.Error())
	if run.PaneID != "" {
		if err := d.Herdr.ClosePane(cleanup, run.PaneID); err != nil {
			panes, listErr := d.Herdr.Panes(cleanup, "")
			alive := listErr != nil
			for _, pane := range panes {
				alive = alive || pane.PaneID == run.PaneID
			}
			if alive {
				d.note(runID, "managed pane could not be stopped; lease retained: "+err.Error())
				return errors.Join(cause, fmt.Errorf("dispatch run %s could not be stopped; its lease and active record were retained; retry cancellation: %w", runID, errors.Join(err, listErr)))
			}
		}
	}
	if err := d.releaseRecordedLease(cleanup, run); err != nil {
		return errors.Join(cause, fmt.Errorf("dispatch run %s stopped but lease release failed; retry cancellation: %w", runID, err))
	}
	if _, err := d.Store.Finish(runID, runs.Failed, "dispatch_error", cause.Error()); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
