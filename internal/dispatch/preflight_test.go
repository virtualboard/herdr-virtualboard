package dispatch

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

const preflightHerdr = `case "$*" in
 *status*) printf 'client:\n  version: 0.9.0\n  protocol: 22\n\nserver:\n  status: running\n  version: 0.9.0\n  socket: /tmp/test.sock\n' ;;
 *) echo 'unexpected mutating Herdr command' >&2; exit 99 ;;
esac`

const preflightDoctor = `echo '{"success":true,"data":{"format_version":1,"ok":true,"version":"test","capabilities":["owner-leases","lease-tokens","lease-renewal","raw-schema-validation","validation-exit-status"],"checks":[{"name":"runtime","ok":true,"fatal":false,"detail":"test runtime"}]}}'`

func TestForcedDispatchPreflightPreservesActiveRun(t *testing.T) {
	for _, test := range []struct{ name, doctor, validation string }{
		{"old binary", `echo 'unknown command doctor' >&2; exit 1`, ``},
		{"missing capabilities", `echo '{"success":true,"data":{"format_version":1,"ok":true,"version":"old","capabilities":[],"checks":[{"name":"runtime","ok":true,"fatal":false,"detail":"old runtime"}]}}'`, ``},
		{"invalid target", preflightDoctor, `echo '{"success":false,"data":{"id":"FTR-0001","status":"in-progress","errors":["invalid schema"]}}'; exit 1`},
		{"missing results", preflightDoctor, `echo '{"success":true,"data":{}}'`},
		{"wrong target", preflightDoctor, `echo '{"success":true,"data":{"id":"FTR-9999","status":"in-progress","errors":[]}}'`},
		{"stale status", preflightDoctor, `echo '{"success":true,"data":{"id":"FTR-0001","status":"review","errors":[]}}'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, preflightHerdr)
			h.addRun("existing", "FTR-0001", feature.InProgress, runs.Running, "old-pane")
			before, err := os.ReadFile(h.dispatcher.Store.Path())
			if err != nil {
				t.Fatal(err)
			}
			if test.validation == "" {
				test.validation = "exit 99"
			}
			bin, argv := stub(t, "vb", `case "$*" in *doctor*) `+test.doctor+`; exit 0 ;; *validate*) `+test.validation+`; exit 0 ;; *) exit 99 ;; esac`)
			h.dispatcher.VB.Bin = bin
			spec, err := h.dispatcher.Workspace.FindSpec("FTR-0001")
			if err != nil {
				t.Fatal(err)
			}
			_, startErr := h.dispatcher.Start(context.Background(), Request{Spec: spec, Force: true})
			if startErr == nil {
				t.Fatal("unsafe replacement launched")
			}
			expected := map[string]string{"old binary": "compatibility check failed", "missing capabilities": "lacks dispatch capabilities", "invalid target": "fails validation", "missing results": "omitted results", "wrong target": "did not confirm", "stale status": "did not confirm"}[test.name]
			if !strings.Contains(startErr.Error(), expected) {
				t.Fatalf("wrong refusal: %v; want %q", startErr, expected)
			}
			after, err := os.ReadFile(h.dispatcher.Store.Path())
			if err != nil || string(after) != string(before) {
				t.Fatalf("active run changed: %v", err)
			}
			for _, call := range argv() {
				if !strings.Contains(call, "doctor") && !strings.Contains(call, "validate") {
					t.Fatalf("preflight mutated board: %s", call)
				}
			}
		})
	}
}

func TestDispatchRevalidatesBeforeAgentLaunch(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, preflightHerdr)
	marker := t.TempDir() + "/validated"
	t.Setenv("PREFLIGHT_MARKER", marker)
	bin, _ := stub(t, "vb", `case "$*" in
 *doctor*) `+preflightDoctor+` ;;
 *validate*)
  if [ -f "$PREFLIGHT_MARKER" ]; then echo '{"success":false,"data":{"id":"FTR-0001","status":"in-progress","errors":["changed after preflight"]}}'; exit 1; fi
  echo checked > "$PREFLIGHT_MARKER"
  echo '{"success":true,"data":{"id":"FTR-0001","status":"in-progress","errors":[]}}' ;;
 *) exit 99 ;;
 esac`)
	h.dispatcher.VB.Bin = bin
	spec, err := h.dispatcher.Workspace.FindSpec("FTR-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.dispatcher.Start(context.Background(), Request{Spec: spec}); err == nil || !strings.Contains(err.Error(), "fails validation") {
		t.Fatalf("changed target accepted: %v", err)
	}
	all, err := h.dispatcher.Store.List()
	if err != nil || len(all) != 0 {
		t.Fatalf("agent recorded before revalidation: %v %v", all, err)
	}
}

func TestAutomaticDispatchAlsoRequiresCompatibleVB(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, preflightHerdr)
	column := h.dispatcher.Config.Columns[string(feature.InProgress)]
	column.Auto = true
	h.dispatcher.Config.Columns[string(feature.InProgress)] = column
	bin, argv := stub(t, "vb", `echo 'unknown command doctor' >&2; exit 1`)
	h.dispatcher.VB.Bin = bin
	if _, err := h.dispatcher.AutoStart(context.Background(), "FTR-0001"); err == nil || !strings.Contains(err.Error(), "compatibility") {
		t.Fatalf("automatic dispatch bypassed compatibility: %v", err)
	}
	all, err := h.dispatcher.Store.List()
	if err != nil || len(all) != 0 {
		t.Fatalf("incompatible automatic run recorded: %v %v", all, err)
	}
	for _, call := range argv() {
		if !strings.Contains(call, "doctor --capabilities-only") {
			t.Fatalf("unexpected vb call: %s", call)
		}
	}
}

func TestClaimRejectsUnusableLeaseResponses(t *testing.T) {
	t.Setenv("HVB_OWNER", "agent")
	for _, data := range []string{
		`{}`, `null`,
		`{"id":"FTR-0001","owner":"agent","expires_at":"2099-01-01T00:00:00Z","ttl_minutes":60}`,
		`{"id":"FTR-0001","owner":"other","token":"private","expires_at":"2099-01-01T00:00:00Z","ttl_minutes":60}`,
		`{"id":"FTR-9999","owner":"agent","token":"private","expires_at":"2099-01-01T00:00:00Z","ttl_minutes":60}`,
		`{"id":"FTR-0001","owner":"agent","token":"private","expires_at":"2000-01-01T00:00:00Z","ttl_minutes":60}`,
	} {
		t.Run(data, func(t *testing.T) {
			h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, preflightHerdr)
			h.dispatcher.Config.LockTTLMinutes = 60
			bin, _ := stub(t, "vb", "echo '{\"success\":true,\"data\":"+data+"}'")
			h.dispatcher.VB.Bin = bin
			_, err := h.dispatcher.claim(context.Background(), Request{Spec: &feature.Spec{Frontmatter: feature.Frontmatter{ID: "FTR-0001"}}})
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe lease accepted or disclosed: %v", err)
			}
		})
	}
}

func TestClaimAcceptsMatchingRenewableLease(t *testing.T) {
	t.Setenv("HVB_OWNER", "agent")
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, preflightHerdr)
	h.dispatcher.Config.LockTTLMinutes = 60
	bin, _ := stub(t, "vb", `echo '{"success":true,"data":{"id":"FTR-0001","owner":"agent","token":"owned-token","expires_at":"2099-01-01T00:00:00Z","ttl_minutes":60}}'`)
	h.dispatcher.VB.Bin = bin
	token, err := h.dispatcher.claim(context.Background(), Request{Spec: &feature.Spec{Frontmatter: feature.Frontmatter{ID: "FTR-0001"}}})
	if err != nil || token != "owned-token" {
		t.Fatalf("matching lease rejected: %v", err)
	}
}
