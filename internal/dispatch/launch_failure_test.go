package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

func TestCancelledLaunchStopsPaneBeforeReleasingLease(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	if _, err := h.dispatcher.Store.Update("r1", func(r *runs.Run) error { r.LockToken = "test-token"; r.LockOwner = "agent"; return nil }); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(t.TempDir(), "events")
	t.Setenv("LAUNCH_TEST_EVENTS", journal)
	herdr, _ := stub(t, "herdr", `printf 'stop\n' >> "$LAUNCH_TEST_EVENTS"; echo '{"id":"x","result":{}}'`)
	vb, _ := stub(t, "vb", `[ "$VB_OWNER" = agent ] || exit 1
case "$*" in
 *--status*) echo '{"success":true,"data":{"id":"FTR-0001","owner":"agent","token":"test-token"}}' ;;
 *"--release --token test-token"*) printf 'release\n' >> "$LAUNCH_TEST_EVENTS"; echo '{"success":true,"data":{}}' ;;
 *) exit 99 ;;
esac`)
	h.dispatcher.Herdr.Bin = herdr
	h.dispatcher.VB.Bin = vb
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.dispatcher.failDispatch(ctx, "r1", context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation was lost: %v", err)
	}
	events, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(events) != "stop\nrelease\n" {
		t.Fatalf("lease released before stopping writer: %s", events)
	}
	run, err := h.dispatcher.Store.Get("r1")
	if err != nil || run.State != runs.Failed || run.Outcome != "dispatch_error" {
		t.Fatalf("failed launch was not recorded: %+v, %v", run, err)
	}
}

func TestFailedLaunchCleanupRetainsWriterRecordAndLease(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	herdr, _ := stub(t, "herdr", `case "$*" in
 *"pane close"*) exit 1 ;;
 *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p1"}]}}' ;;
 *) echo '{"id":"x","result":{}}' ;;
esac`)
	h.dispatcher.Herdr.Bin = herdr
	err := h.dispatcher.failDispatch(context.Background(), "r1", errors.New("prompt failed"))
	if err == nil || !strings.Contains(err.Error(), "lease and active record were retained") {
		t.Fatalf("unsafe cleanup result: %v", err)
	}
	run, err := h.dispatcher.Store.Get("r1")
	if err != nil || !run.Active() {
		t.Fatal("live writer was hidden by failed cleanup")
	}
	if len(h.vbArgv()) != 0 {
		t.Fatal("failed cleanup released the lease")
	}
}
