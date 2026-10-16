package dispatch

import (
	"context"
	"fmt"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"time"
)

func (d *Dispatcher) vbForRun(run *runs.Run) *vb.Client {
	client := *d.VB
	client.LeaseToken = run.LockToken
	if run.LockOwner != "" {
		client.Actor = run.LockOwner
	}
	return &client
}

func (d *Dispatcher) refreshLease(ctx context.Context, run *runs.Run) error {
	if run.LockToken == "" {
		return nil
	} // legacy runs have no renewable token
	client := d.vbForRun(run)
	lease, err := client.LockStatus(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	if lease == nil || lease.Owner == "" || lease.ID != run.FeatureID || lease.Token != run.LockToken || (run.LockOwner != "" && lease.Owner != run.LockOwner) || lease.TTLMinutes <= 0 {
		return fmt.Errorf("feature lease was released, replaced, or returned incomplete ownership metadata")
	}
	expires, err := time.Parse(time.RFC3339, lease.ExpiresAt)
	if err != nil {
		return fmt.Errorf("invalid lease expiry: %w", err)
	}
	margin := time.Duration(lease.TTLMinutes) * time.Minute / 3
	if time.Until(expires) > margin {
		return nil
	}
	renewed, err := client.Renew(ctx, run.FeatureID, run.LockToken)
	if err != nil {
		return err
	}
	if renewed == nil || renewed.Owner == "" || renewed.ID != run.FeatureID || renewed.Token != run.LockToken || renewed.TTLMinutes <= 0 || renewed.Expired || (run.LockOwner != "" && renewed.Owner != run.LockOwner) {
		return fmt.Errorf("lease renewal did not confirm this run's ownership")
	}
	renewedExpiry, err := time.Parse(time.RFC3339, renewed.ExpiresAt)
	if err != nil || !renewedExpiry.After(time.Now()) {
		return fmt.Errorf("lease renewal returned an invalid or expired deadline")
	}
	return nil
}

// releaseRecordedLease releases only the recorded nonce. A missing or successor
// lease proves this run no longer owns it; neither case authorizes deletion.
func (d *Dispatcher) releaseRecordedLease(ctx context.Context, run *runs.Run) error {
	if run.LeaseReleased || (run.LockToken == "" && d.Config.LockTTLMinutes <= 0) {
		return nil
	}
	client := d.vbForRun(run)
	lease, err := client.LockStatus(ctx, run.FeatureID)
	if err != nil {
		return fmt.Errorf("inspect recorded lease: %w", err)
	}
	if lease == nil || lease.Token != run.LockToken || (run.LockOwner != "" && lease.Owner != run.LockOwner) {
		return nil
	}
	if run.LockToken == "" {
		return fmt.Errorf("legacy lease has no ownership token; inspect vb lock %s --status and explicitly resolve legacy ownership before retrying", run.FeatureID)
	}
	if lease.ID != run.FeatureID {
		return fmt.Errorf("lease status did not confirm feature %s", run.FeatureID)
	}
	if run.LockOwner == "" {
		client.Actor = lease.Owner
	}
	return client.Release(ctx, run.FeatureID, run.LockToken)
}

// finishAwaitingLease runs with the run transaction held. Persisting the flag
// after the external effect makes a crash/retry safe: an absent lease is success.
func (d *Dispatcher) finishAwaitingLease(ctx context.Context, run *runs.Run) (*runs.Run, error) {
	if run.LeaseReleased {
		return run, nil
	}
	if err := d.releaseRecordedLease(ctx, run); err != nil {
		return run, fmt.Errorf("run %s is awaiting review with lease release pending; retry a refresh or cancellation: %w", run.ID, err)
	}
	return d.Store.Update(run.ID, func(current *runs.Run) error { current.LeaseReleased = true; return nil })
}

// reclaimAwaitingLease obtains fresh ownership before a human's eventual outcome
// mutates the board. It never forces a lease away from another run or owner.
func (d *Dispatcher) reclaimAwaitingLease(ctx context.Context, run *runs.Run) (*runs.Run, error) {
	var err error
	run, err = d.finishAwaitingLease(ctx, run)
	if err != nil {
		return nil, err
	}
	spec, err := d.Workspace.FindSpec(run.FeatureID)
	if err != nil {
		return nil, err
	}
	token, err := d.claim(ctx, Request{Spec: spec})
	if err != nil {
		return nil, fmt.Errorf("claim lease before completing awaiting run %s: %w", run.ID, err)
	}
	return d.Store.Update(run.ID, func(current *runs.Run) error {
		current.LockToken = token
		current.LockOwner = d.Config.ResolveOwner()
		current.LeaseReleased = false
		return nil
	})
}
