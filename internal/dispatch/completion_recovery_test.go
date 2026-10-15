package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

func TestCompletionRetriesRoutingWithoutChangingOutcome(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	marker := filepath.Join(t.TempDir(), "fail")
	if err := os.WriteFile(marker, []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VB_FAIL_MARKER", marker)
	bin, argv := stub(t, "vb", `if [ -f "$VB_FAIL_MARKER" ]; then echo unavailable >&2; exit 6; fi
echo '{"success":true,"data":{}}'`)
	h.dispatcher.VB.Bin = bin
	first, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "criteria met")
	if err != nil || !first.Pending || first.Run.Outcome != "success" {
		t.Fatalf("outcome or recovery state lost: %+v, %v", first, err)
	}
	active, err := h.dispatcher.Store.Active()
	if err != nil || len(active) != 1 {
		t.Fatalf("unfinished completion stopped being watched: %v, %v", active, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	second, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeFailure, "late conflicting report")
	if err != nil || second.Pending || second.Moved != feature.Review || second.Run.Outcome != "success" {
		t.Fatalf("recovery did not retain first outcome: %+v, %v", second, err)
	}
	count := len(argv())
	if _, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "duplicate"); err != nil {
		t.Fatal(err)
	}
	if len(argv()) != count {
		t.Fatal("completed effects ran again")
	}
}

func TestCompletionRetriesIndexWithoutRepeatingMove(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	marker := filepath.Join(t.TempDir(), "fail")
	if err := os.WriteFile(marker, []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VB_FAIL_MARKER", marker)
	bin, argv := stub(t, "vb", `case "$*" in *index*) if [ -f "$VB_FAIL_MARKER" ]; then exit 6; fi;; esac
echo '{"success":true,"data":{}}'`)
	h.dispatcher.VB.Bin = bin
	first, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "done")
	if err != nil || !first.Pending || !first.Run.Completion.Routed || first.Run.Completion.Indexed {
		t.Fatalf("index recovery state missing: %+v, %v", first, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	second, err := h.dispatcher.Retry(context.Background(), "r1")
	if err != nil || second.Pending {
		t.Fatalf("retry failed: %+v, %v", second, err)
	}
	moves := 0
	for _, call := range argv() {
		if strings.Contains(call, "move FTR-0001") {
			moves++
		}
	}
	if moves != 1 {
		t.Fatalf("retry repeated the move %d times", moves)
	}
}

func TestConcurrentCompletionKeepsOneOutcomeAndOneMove(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcome := OutcomeSuccess
			if i%2 == 0 {
				outcome = OutcomeFailure
			}
			if _, err := h.dispatcher.Complete(context.Background(), "r1", outcome, "report"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	run, err := h.dispatcher.Store.Get("r1")
	if err != nil || run.Completion == nil || !run.Completion.Done {
		t.Fatalf("concurrent completion did not settle: %+v, %v", run, err)
	}
	want := string(feature.Review)
	if run.Outcome == string(OutcomeFailure) {
		want = string(feature.Blocked)
	}
	if run.MovedTo != want {
		t.Fatalf("outcome and move disagree: %+v", run)
	}
	moves := 0
	for _, call := range h.vbArgv() {
		if strings.Contains(call, "move FTR-0001") {
			moves++
		}
	}
	if moves != 1 {
		t.Fatalf("concurrent completion performed %d moves", moves)
	}
}

func TestCancelledCompletionCannotResumeOrDispatch(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	bin, _ := stub(t, "vb", `echo unavailable >&2; exit 6`)
	h.dispatcher.VB.Bin = bin
	first, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "done")
	if err != nil || !first.Pending {
		t.Fatalf("expected pending completion: %+v, %v", first, err)
	}
	cancelled, err := h.dispatcher.Cancel(context.Background(), "r1", "stop here")
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled.Completion.Cancelled || !cancelled.Completion.Done || !cancelled.Completion.AutoChecked {
		t.Fatalf("cancellation is not durable: %+v", cancelled.Completion)
	}
	if _, err := h.dispatcher.Retry(context.Background(), "r1"); err == nil {
		t.Fatal("retry revived cancelled effects")
	}
	repeated, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeFailure, "late report")
	if err != nil || repeated.Pending || repeated.Run.Outcome != "success" || repeated.Run.AutoChildID != "" {
		t.Fatalf("cancelled completion changed: %+v, %v", repeated, err)
	}
}

func TestCancelConfirmsPaneGoneAfterCloseFailure(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "gone", true: "live"}[live], func(t *testing.T) {
			inventory := `{"id":"x","result":{"panes":[]}}`
			if live {
				inventory = `{"id":"x","result":{"panes":[{"pane_id":"w1:p1"}]}}`
			}
			script := `case "$*" in
    *"pane close"*) exit 1 ;;
    *"pane list"*) echo '` + inventory + `' ;;
    *) echo '{"id":"x","result":{}}' ;;
   esac`
			h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
			h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
			_, err := h.dispatcher.Cancel(context.Background(), "r1", "cancel")
			if (err != nil) != live {
				t.Fatalf("live=%v: %v", live, err)
			}
			run, err := h.dispatcher.Store.Get("r1")
			if err != nil {
				t.Fatal(err)
			}
			if live && run.State != runs.Running {
				t.Fatal("lost live run tracking")
			}
			if !live && run.State != runs.Cancelled {
				t.Fatal("absent pane prevented cancellation")
			}
		})
	}
}

func TestInvalidForcedDispatchDoesNotCancelLiveWork(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	spec, err := h.dispatcher.Workspace.FindSpec("FTR-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.dispatcher.Start(context.Background(), Request{Spec: spec, Kind: "notaharness", Force: true}); err == nil {
		t.Fatal("invalid forced dispatch accepted")
	}
	run, err := h.dispatcher.Store.Get("r1")
	if err != nil || run.State != runs.Running {
		t.Fatalf("invalid request cancelled live work: %+v, %v", run, err)
	}
}
