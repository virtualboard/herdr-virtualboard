package vb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// fakeVB writes an executable stub that records its argv and replies with a
// canned response. Testing against the real vb would make these tests depend on
// a VirtualBoard install and on vb's own release cadence; testing against a stub
// pins the argv hvb actually sends, which is the part hvb owns.
func fakeVB(t *testing.T, script string) (*Client, func() []string) {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	path := filepath.Join(dir, "vb")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argvLog + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{Bin: path, Root: "/project"}
	return client, func() []string {
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

func TestNewSendsTitleAndLabels(t *testing.T) {
	client, argv := fakeVB(t, `cat <<'JSON'
{"success":true,"message":"created","data":{"id":"FTR-0042","title":"Add retry","path":"features/backlog/FTR-0042-add-retry.md","labels":["backend"]}}
JSON`)
	result, err := client.New(context.Background(), "Add retry", "backend")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if result.ID != "FTR-0042" {
		t.Errorf("ID = %q", result.ID)
	}
	got := argv()[0]
	for _, want := range []string{"--root /project", "--json", "new", "Add retry", "backend"} {
		if !strings.Contains(got, want) {
			t.Errorf("argv %q is missing %q", got, want)
		}
	}
}

func TestMovePassesOwner(t *testing.T) {
	client, argv := fakeVB(t, `cat <<'JSON'
{"success":true,"message":"moved","data":{"id":"FTR-0001","status":"review","owner":"alice","path":"features/review/FTR-0001.md","summary":"Moved"}}
JSON`)
	result, err := client.Move(context.Background(), "FTR-0001", feature.Review, "alice")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if result.Status != "review" || result.Owner != "alice" {
		t.Errorf("result = %+v", result)
	}
	if got := argv()[0]; !strings.Contains(got, "move FTR-0001 review --owner alice") {
		t.Errorf("argv = %q", got)
	}
}

// vb reports failures on stderr as plain text even under --json, so the
// classification has to come from the exit code.
func TestExitCodesBecomeTypedErrors(t *testing.T) {
	cases := []struct {
		code    int
		message string
		want    int
	}{
		{ExitNotFound, "feature not found: FTR-9999", ExitNotFound},
		{ExitInvalidTransition, "invalid status transition", ExitInvalidTransition},
		{ExitLockConflict, "lock held by bob", ExitLockConflict},
	}
	for _, tc := range cases {
		client, _ := fakeVB(t, "echo '"+tc.message+"' >&2\nexit "+itoa(tc.code))
		_, err := client.Move(context.Background(), "FTR-0001", feature.Review, "")
		var vbErr *Error
		if !errors.As(err, &vbErr) {
			t.Fatalf("exit %d: error = %v, want *vb.Error", tc.code, err)
		}
		if vbErr.Code != tc.want {
			t.Errorf("exit %d: Code = %d, want %d", tc.code, vbErr.Code, tc.want)
		}
		if !strings.Contains(vbErr.Message, tc.message) {
			t.Errorf("exit %d: Message = %q, want it to carry vb's stderr", tc.code, vbErr.Message)
		}
	}
}

func TestUpdateRefusesAnEmptyChange(t *testing.T) {
	client, argv := fakeVB(t, "exit 0")
	if err := client.Update(context.Background(), "FTR-0001", nil, nil); err == nil {
		t.Fatal("Update with no fields should fail rather than rewrite `updated` for nothing")
	}
	if len(argv()) != 0 {
		t.Fatalf("Update should not have invoked vb: %v", argv())
	}
}

// The argv must be deterministic so recorded-argv assertions and dry runs stay
// stable across map iteration order.
func TestUpdateOrdersFieldsDeterministically(t *testing.T) {
	client, argv := fakeVB(t, `echo '{"success":true,"message":"ok","data":{}}'`)
	fields := map[string]string{"priority": "P1", "complexity": "L", "owner": "alice"}
	for attempt := 0; attempt < 5; attempt++ {
		if err := client.Update(context.Background(), "FTR-0001", fields, nil); err != nil {
			t.Fatal(err)
		}
	}
	recorded := argv()
	for _, line := range recorded {
		if !strings.Contains(line, "--field complexity=L --field owner=alice --field priority=P1") {
			t.Fatalf("fields are not sorted: %q", line)
		}
	}
}

func TestSnapshotDecodesTheEmbeddedIndex(t *testing.T) {
	client, _ := fakeVB(t, `cat <<'JSON'
{"success":true,"message":"index generated","data":{"format":"json","written":false,"total":1,"content":"{\"generated\":\"2026-09-15\",\"features\":[{\"id\":\"FTR-0001\",\"title\":\"First\",\"status\":\"backlog\",\"owner\":\"unassigned\",\"priority\":\"P2\",\"labels\":[],\"updated\":\"2026-09-15\",\"path\":\"backlog/FTR-0001.md\"}],\"summary\":{\"backlog\":1}}"}}
JSON`)
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Features) != 1 || snapshot.Features[0].ID != "FTR-0001" {
		t.Fatalf("Features = %+v", snapshot.Features)
	}
	if snapshot.Summary["backlog"] != 1 {
		t.Errorf("Summary = %v", snapshot.Summary)
	}
}

// An unlocked feature is a normal state; it must not surface as an error the
// caller has to match on by message.
func TestLockStatusReportsUnlockedAsNil(t *testing.T) {
	client, _ := fakeVB(t, "echo 'no lock for FTR-0001' >&2\nexit 2")
	lock, err := client.LockStatus(context.Background(), "FTR-0001")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if lock != nil {
		t.Fatalf("lock = %+v, want nil", lock)
	}
}

func TestDryRunIsPassedThrough(t *testing.T) {
	client, argv := fakeVB(t, `echo '{"success":true,"message":"ok","data":{"id":"FTR-0001","status":"review","owner":"","path":"","summary":""}}'`)
	client.DryRun = true
	if _, err := client.Move(context.Background(), "FTR-0001", feature.Review, ""); err != nil {
		t.Fatal(err)
	}
	if got := argv()[0]; !strings.Contains(got, "--dry-run") {
		t.Fatalf("argv = %q, want --dry-run", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

func TestLeaseErrorsRedactTokenArgumentsAndMessages(t *testing.T) {
	for _, args := range [][]string{{"lock", "FTR-0001", "--release", "--token", "private-token"}, {"lock", "FTR-0001", "--token=private-token", "--renew"}} {
		err := &Error{Code: ExitLockConflict, Args: args, Message: "refused private-token"}
		if message := err.Error(); strings.Contains(message, "private-token") || !strings.Contains(message, "[redacted]") {
			t.Fatalf("token disclosed: %s", message)
		}
		if !strings.Contains(strings.Join(err.Args, " "), "private-token") {
			t.Fatal("formatting mutated structured arguments")
		}
	}
}

func TestLockStatusRequiresExplicitEvidenceOfAbsence(t *testing.T) {
	for _, data := range []string{`{}`, `null`, `{"id":"FTR-0001"}`, `{"id":"FTR-0002","locked":false}`, `{"id":"FTR-0001","locked":false,"token":"private"}`, `{"id":"FTR-0001","locked":false,"owner":"alice"}`} {
		t.Run(data, func(t *testing.T) {
			client, _ := fakeVB(t, "echo '{\"success\":true,\"data\":"+data+"}'")
			if _, err := client.LockStatus(context.Background(), "FTR-0001"); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("missing evidence accepted or token disclosed: %v", err)
			}
		})
	}
	client, _ := fakeVB(t, `echo '{"success":true,"data":{"id":"FTR-0001","locked":false,"expired":false}}'`)
	lease, err := client.LockStatus(context.Background(), "FTR-0001")
	if err != nil || lease != nil {
		t.Fatalf("explicit unlocked result rejected: %+v %v", lease, err)
	}
}
