package dispatch

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

func TestAwaitingLeaseFailureIsDurableAndRetriedWithoutHerdr(t *testing.T) {
	t.Setenv("HVB_OWNER", "agent")
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "gone-pane")
	_, err := h.dispatcher.Store.Update("r1", func(run *runs.Run) error { run.LockToken = "old-private-token"; run.LockOwner = "agent"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir() + "/fail-release"
	if err := os.WriteFile(marker, []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEASE_FAILURE_MARKER", marker)
	bin, argv := stub(t, "vb", `case "$*" in
 *--status*) echo '{"success":true,"data":{"id":"FTR-0001","owner":"agent","token":"old-private-token"}}' ;;
 *--release*) if [ -f "$LEASE_FAILURE_MARKER" ];then echo 'release unavailable' >&2;exit 6;fi;echo '{"success":true,"data":{}}' ;;
 *) echo 'unexpected command' >&2;exit 99 ;;
esac`)
	h.dispatcher.VB.Bin = bin
	if _, err := h.dispatcher.Reconcile(context.Background()); err == nil || strings.Contains(err.Error(), "old-private-token") {
		t.Fatalf("pending release not reported or token disclosed: %v", err)
	}
	pending, err := h.dispatcher.Store.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != runs.Awaiting || pending.LeaseReleased || pending.Outcome != "pane_closed" {
		t.Fatalf("lost pending state: %+v", pending)
	}
	ended := *pending.EndedAt
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	// Simulate a fresh process, with Herdr now completely unavailable.
	reopened, err := runs.Open(h.dispatcher.Workspace.ID())
	if err != nil {
		t.Fatal(err)
	}
	resumed := *h.dispatcher
	resumed.Store = reopened
	resumed.Herdr.Bin = t.TempDir() + "/no-herdr"
	if _, err := resumed.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	settled, err := reopened.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if !settled.LeaseReleased || settled.State != runs.Awaiting || settled.Outcome != "pane_closed" || !settled.EndedAt.Equal(ended) {
		t.Fatalf("cleanup changed the observed outcome: %+v", settled)
	}
	calls := len(argv())
	if changed, err := resumed.Reconcile(context.Background()); err != nil || len(changed) != 0 || len(argv()) != calls {
		t.Fatalf("settled awaiting run renewed or released again: %v %v", changed, err)
	}
	releases := 0
	for _, call := range argv() {
		if strings.Contains(call, "--release") {
			releases++
		}
		if strings.Contains(call, "--renew") {
			t.Fatalf("awaiting lease renewed: %s", call)
		}
	}
	if releases != 2 {
		t.Fatalf("release attempts=%d, want original failure plus retry", releases)
	}
}

func TestCancellationAndCompletedEffectsPreserveSuccessorLease(t *testing.T) {
	for _, action := range []string{"cancel", "complete"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
			h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "old-pane")
			_, err := h.dispatcher.Store.Update("r1", func(run *runs.Run) error {
				run.LockToken = "old-token"
				run.LockOwner = "agent"
				if action == "complete" {
					run.State = runs.Succeeded
					run.Outcome = "success"
					run.Completion = &runs.CompletionProgress{Captured: true, Published: true, Linked: true, Routed: true, Indexed: true}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			bin, argv := stub(t, "vb", `case "$*" in *--status*) echo '{"success":true,"data":{"id":"FTR-0001","owner":"successor","token":"successor-token"}}';;*) exit 99;;esac`)
			h.dispatcher.VB.Bin = bin
			if action == "cancel" {
				_, err = h.dispatcher.Cancel(context.Background(), "r1", "stop old run")
			} else {
				var result *Completion
				result, err = h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "")
				if err == nil && result.Pending {
					t.Fatal("successor lease stranded completed effects")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			settled, err := h.dispatcher.Store.Get("r1")
			if err != nil || !settled.LeaseReleased || settled.Active() {
				t.Fatalf("old ledger stranded: %+v %v", settled, err)
			}
			for _, call := range argv() {
				if !strings.Contains(call, "--status") {
					t.Fatalf("successor lease or feature mutated: %s", call)
				}
			}
		})
	}
}

func TestHumanOutcomeReclaimsAwaitingLeaseWithoutForce(t *testing.T) {
	t.Setenv("HVB_OWNER", "agent")
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.dispatcher.Config.LockTTLMinutes = 60
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Awaiting, "old-pane")
	_, err := h.dispatcher.Store.Update("r1", func(run *runs.Run) error {
		run.LeaseReleased = true
		run.LockToken = "old-token"
		run.LockOwner = "agent"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bin, argv := stub(t, "vb", `case "$*" in
 *" lock "*) echo '{"success":true,"data":{"id":"FTR-0001","owner":"agent","token":"fresh-token","expires_at":"2099-01-01T00:00:00Z","ttl_minutes":60}}' ;;
 *) echo '{"success":true,"data":{}}' ;;
esac`)
	h.dispatcher.VB.Bin = bin
	result, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "verified by human")
	if err != nil || result.Pending || !result.Run.Completion.Done || result.Run.LockToken != "fresh-token" || !result.Run.LeaseReleased {
		t.Fatalf("awaiting completion failed: %+v %v", result, err)
	}
	calls := argv()
	acquire, move := -1, -1
	for i, call := range calls {
		if strings.Contains(call, "--force") {
			t.Fatalf("human outcome stole a lease: %s", call)
		}
		if strings.Contains(call, "lock FTR-0001 --owner agent") {
			acquire = i
		}
		if strings.Contains(call, " move ") {
			move = i
		}
	}
	if acquire < 0 || move <= acquire {
		t.Fatalf("board changed before new ownership: %v", calls)
	}
}
