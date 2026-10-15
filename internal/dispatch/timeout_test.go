package dispatch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

func TestTimeoutStopsWriterBeforeRoutingAndRelease(t *testing.T) {
	for _, stopFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "stop-failed"}[stopFails], func(t *testing.T) {
			events := t.TempDir() + "/events"
			t.Setenv("TIMEOUT_EVENTS", events)
			if stopFails {
				t.Setenv("TIMEOUT_STOP_FAIL", "1")
			} else {
				t.Setenv("TIMEOUT_STOP_FAIL", "")
			}
			herdr := `case "$*" in
 *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p1"}]}}' ;;
 *"agent list"*) echo '{"id":"x","result":{"agents":[]}}' ;;
 *"pane close"*) echo stop >> "$TIMEOUT_EVENTS"; if [ "$TIMEOUT_STOP_FAIL" = 1 ];then exit 1;fi;echo '{"id":"x","result":{}}' ;;
 *) exit 99 ;;
esac`
			h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, herdr)
			h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
			_, err := h.dispatcher.Store.Update("r1", func(run *runs.Run) error {
				run.LockToken = "owned-token"
				run.LockOwner = "agent"
				run.StartedAt = time.Now().Add(-2 * time.Minute)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			column := h.dispatcher.Config.Column(feature.InProgress)
			column.Timeout = config.Duration(time.Minute)
			h.dispatcher.Config.Columns[string(feature.InProgress)] = column
			bin, argv := stub(t, "vb", `case "$*" in
 *--status*) echo '{"success":true,"data":{"id":"FTR-0001","owner":"agent","token":"owned-token","expires_at":"2099-01-01T00:00:00Z","ttl_minutes":60}}' ;;
 *" move "*) echo move >> "$TIMEOUT_EVENTS";echo '{"success":true,"data":{}}' ;;
 *" index"*) echo index >> "$TIMEOUT_EVENTS";echo '{"success":true,"data":{}}' ;;
 *--release*) echo release >> "$TIMEOUT_EVENTS";echo '{"success":true,"data":{}}' ;;
 *) exit 99 ;;
esac`)
			h.dispatcher.VB.Bin = bin
			_, err = h.dispatcher.Reconcile(context.Background())
			if (err != nil) != stopFails {
				t.Fatalf("stop failure=%v: %v", stopFails, err)
			}
			raw, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			run, err := h.dispatcher.Store.Get("r1")
			if err != nil {
				t.Fatal(err)
			}
			if stopFails {
				if string(raw) != "stop\n" || len(argv()) != 0 || run.State != runs.Running || run.Completion != nil {
					t.Fatalf("failed stop lost writer tracking or mutated board: %s %+v", raw, run)
				}
			} else if string(raw) != "stop\nmove\nindex\nrelease\n" || run.State != runs.Failed || !run.Completion.Done || !run.LeaseReleased {
				t.Fatalf("unsafe timeout ordering/state: %s %+v", raw, run)
			}
		})
	}
}

func TestLeaseRenewalRequiresMatchingFreshMetadata(t *testing.T) {
	for _, response := range []string{
		`{}`, `{"id":"FTR-0001","owner":"other","token":"owned","ttl_minutes":60,"expires_at":"2099-01-01T00:00:00Z"}`,
		`{"id":"FTR-0001","owner":"agent","token":"successor","ttl_minutes":60,"expires_at":"2099-01-01T00:00:00Z"}`,
		`{"id":"FTR-0001","owner":"agent","token":"owned","ttl_minutes":60,"expires_at":"2000-01-01T00:00:00Z"}`,
	} {
		t.Run(response, func(t *testing.T) {
			h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, noPanes)
			t.Setenv("RENEWAL_DUE", time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
			bin, _ := stub(t, "vb", `case "$*" in
 *--status*) printf '{"success":true,"data":{"id":"FTR-0001","owner":"agent","token":"owned","ttl_minutes":60,"expires_at":"%s"}}\n' "$RENEWAL_DUE" ;;
 *--renew*) echo '{"success":true,"data":`+response+`}' ;;
 *) exit 99 ;;
esac`)
			h.dispatcher.VB.Bin = bin
			err := h.dispatcher.refreshLease(context.Background(), &runs.Run{FeatureID: "FTR-0001", LockToken: "owned", LockOwner: "agent"})
			if err == nil || strings.Contains(err.Error(), "successor") {
				t.Fatalf("invalid renewal accepted or nonce disclosed: %v", err)
			}
		})
	}
}
