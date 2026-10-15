package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// harness builds a dispatcher over a real temp workspace with stubbed vb and
// herdr binaries. The stubs record argv, so a test can assert that a completion
// actually asked vb to move the feature rather than just updating hvb's own
// bookkeeping.
type harness struct {
	t          *testing.T
	dispatcher *Dispatcher
	root       string
	vbArgv     func() []string
}

func newHarness(t *testing.T, statuses map[string]feature.Status, herdrScript string) *harness {
	t.Helper()
	root := t.TempDir()
	for _, status := range feature.Statuses {
		if err := os.MkdirAll(filepath.Join(root, workspace.Dir, "features", string(status)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, workspace.Dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for id, status := range statuses {
		path := filepath.Join(root, workspace.Dir, "features", string(status), id+".md")
		body := "---\nid: " + id + "\ntitle: " + id + "\nstatus: " + string(status) +
			"\nowner: agent\ncreated: 2026-01-01\nupdated: 2026-01-01\n---\n\n## Summary\nbody\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	vbClient, vbArgv := stub(t, "vb", dispatchPreflightVB+`echo '{"success":true,"message":"ok","data":{"id":"x","status":"","owner":"","path":"","summary":""}}'`)
	herdrBin, _ := stub(t, "herdr", herdrScript)

	t.Setenv("HVB_DATA_DIR", t.TempDir())
	store, err := runs.Open(ws.ID())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	// Locking is exercised in its own test; leaving it on here would make
	// every stub script also have to answer `vb lock`.
	cfg.LockTTLMinutes = 0

	return &harness{
		t:    t,
		root: ws.Root,
		dispatcher: &Dispatcher{
			Workspace: ws,
			Config:    &cfg,
			Herdr:     &herdrcli.Client{Bin: herdrBin},
			VB:        &vb.Client{Bin: vbClient, Root: root},
			Store:     store,
			Roles:     []roles.Role{{Key: "backend_dev"}, {Key: "qa"}},
		},
		vbArgv: vbArgv,
	}
}

func stub(t *testing.T, name, script string) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argvLog + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, func() []string {
		raw, err := os.ReadFile(argvLog)
		if err != nil {
			return nil
		}
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
}

func (h *harness) addRun(id, featureID string, status feature.Status, state runs.State, paneID string) *runs.Run {
	h.t.Helper()
	run := &runs.Run{
		ID: id, FeatureID: featureID, Title: featureID, Status: status,
		Role: "backend_dev", Kind: "claude", State: state,
		PaneID: paneID, StartedAt: time.Now().UTC(),
	}
	if err := h.dispatcher.Store.Append(run); err != nil {
		h.t.Fatal(err)
	}
	return run
}

const noPanes = `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[]}}' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`

func TestCompleteSuccessRoutesToReview(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "done")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Moved != feature.Review {
		t.Fatalf("Moved = %q, want review (%s)", completion.Moved, completion.TransitionError)
	}
	if completion.Run.State != runs.Succeeded {
		t.Errorf("State = %q", completion.Run.State)
	}
	assertVBCalled(t, h.vbArgv(), "move FTR-0001 review")
}

func TestCompleteFailureRoutesToBlocked(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeFailure, "could not")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Moved != feature.Blocked {
		t.Fatalf("Moved = %q, want blocked", completion.Moved)
	}
	if completion.Run.State != runs.Failed {
		t.Errorf("State = %q", completion.Run.State)
	}
}

// A review run that fails goes back to in-progress, not to blocked: review can
// reach in-progress but not blocked.
func TestCompleteFailureFromReviewGoesBackToInProgress(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.Review}, noPanes)
	h.addRun("r1", "FTR-0001", feature.Review, runs.Running, "w1:p1")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeFailure, "criteria unmet")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Moved != feature.InProgress {
		t.Fatalf("Moved = %q, want in-progress", completion.Moved)
	}
}

// An agent saying it is blocked is reporting a fact about the world, not a
// verdict on the work, so it beats the column's failure route.
func TestBlockedOutcomeOverridesTheFailureRoute(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeBlocked, "waiting on an API key")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Moved != feature.Blocked {
		t.Fatalf("Moved = %q, want blocked", completion.Moved)
	}
}

// review → blocked is illegal, so a blocked report from review falls back to
// the column's failure route rather than attempting an impossible move.
func TestBlockedFromReviewFallsBackToTheFailureRoute(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.Review}, noPanes)
	h.addRun("r1", "FTR-0001", feature.Review, runs.Running, "w1:p1")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeBlocked, "stuck")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Moved != feature.InProgress {
		t.Fatalf("Moved = %q, want the review failure route", completion.Moved)
	}
}

// A human may move the feature while the agent works. The transition is
// validated against where the feature actually is, not against the stale
// status the run recorded at dispatch.
func TestCompleteRevalidatesAgainstTheCurrentStatus(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.Done}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	completion, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Moved != "" {
		t.Fatalf("Moved = %q; a done feature must not be moved", completion.Moved)
	}
	if completion.TransitionError == "" {
		t.Fatal("the refused transition must be explained")
	}
	// The run still ends: the agent did its work, and a routing rule the
	// lifecycle forbids is a configuration problem, not lost work.
	if completion.Run.State != runs.Succeeded {
		t.Errorf("State = %q, want the outcome to survive", completion.Run.State)
	}
}

func TestCompleteIsIdempotent(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	first, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeSuccess, "done")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.dispatcher.Complete(context.Background(), "r1", OutcomeFailure, "changed my mind")
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.Outcome != first.Run.Outcome {
		t.Fatalf("a second report changed the outcome: %q → %q", first.Run.Outcome, second.Run.Outcome)
	}
}

// A run whose pane is gone ended without a report. That is `awaiting` — the
// board saying "the agent stopped and nobody knows why yet" — not a failure,
// and the feature must not be routed anywhere.
func TestReconcileParksARunWhosePaneVanished(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	changed, err := h.dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].State != runs.Awaiting {
		t.Fatalf("changed = %+v, want one awaiting run", changed)
	}
	if changed[0].Outcome != "pane_closed" {
		t.Errorf("Outcome = %q", changed[0].Outcome)
	}
	if changed[0].MovedTo != "" {
		t.Errorf("an unexplained stop must not move the feature, got %q", changed[0].MovedTo)
	}
}

// A harness that reports `done` without calling `hvb run done` also parks:
// only a human knows whether it succeeded.
func TestReconcileParksAnAgentThatFinishedWithoutReporting(t *testing.T) {
	script := `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1"}]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[{"pane_id":"w1:p1","agent":"claude","agent_status":"done"}]}}' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	changed, err := h.dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Outcome != "agent_done" {
		t.Fatalf("changed = %+v, want one agent_done run", changed)
	}
}

// A working agent in a live pane must be left completely alone.
func TestReconcileLeavesAWorkingRunAlone(t *testing.T) {
	script := `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1"}]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[{"pane_id":"w1:p1","agent":"claude","agent_status":"working"}]}}' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	changed, err := h.dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %+v, want nothing", changed)
	}
}

// Without a trustworthy pane list there is nothing to reconcile against.
// Guessing that every run died would be far worse than doing nothing.
func TestReconcileDoesNotGuessWhenHerdrIsUnreachable(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress},
		"echo 'connection refused' >&2\nexit 1")
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	if _, err := h.dispatcher.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile should report the failure rather than settling runs blindly")
	}
	run, err := h.dispatcher.Store.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != runs.Running {
		t.Fatalf("State = %q, want the run untouched", run.State)
	}
}

func TestCancelClosesThePaneAndEndsTheRun(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
	h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")

	cancelled, err := h.dispatcher.Cancel(context.Background(), "r1", "user asked")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != runs.Cancelled {
		t.Fatalf("State = %q", cancelled.State)
	}
}

func assertVBCalled(t *testing.T, argv []string, want string) {
	t.Helper()
	for _, line := range argv {
		if strings.Contains(line, want) {
			return
		}
	}
	t.Fatalf("vb was never called with %q; calls were:\n  %s", want, strings.Join(argv, "\n  "))
}

// Read the fixture's actual lifecycle so a canned success cannot conceal a stale
// target. Other commands still use each test's mutation/failure behavior.
const dispatchPreflightVB = `case "$*" in
 *"doctor --capabilities-only"*) echo '{"success":true,"data":{"format_version":1,"ok":true,"version":"test","capabilities":["owner-leases","lease-tokens","lease-renewal","raw-schema-validation","validation-exit-status"],"checks":[{"name":"runtime","ok":true,"fatal":false,"detail":"test runtime"}]}}'; exit 0 ;;
 *"validate "*)
  root=""; id=""
  while [ "$#" -gt 0 ]; do
   case "$1" in --root) root="$2"; shift ;; validate) id="$2"; shift ;; esac
   shift
  done
  path="$(find "$root/.virtualboard/features" -name "$id*.md" | head -1)"
  [ -n "$path" ] || exit 2
  status="$(sed -n 's/^status: //p' "$path" | head -1)"
  printf '{"success":true,"data":{"id":"%s","status":"%s","errors":[]}}\n' "$id" "$status"
  exit 0 ;;
esac
`
