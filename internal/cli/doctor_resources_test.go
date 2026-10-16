package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

func TestPaneDiagnosticsScopeAndRetainedHistory(t *testing.T) {
	history := []*runs.Run{
		{ID: "active", State: runs.Running, PaneID: "lost"},
		{ID: "past", State: runs.Succeeded, PaneID: "retained"},
		{ID: "queued", State: runs.Queued},
	}
	panes := []herdrcli.Pane{
		{PaneID: "retained", Label: "ftr-0001 · qa", CWD: "/project"},
		{PaneID: "untracked", Label: "ftr-0002 · backend_dev", CWD: "/project/subdir"},
		{PaneID: "outside", Label: "ftr-0003", CWD: "/project-other"},
		{PaneID: "manual", Label: "my shell", CWD: "/project"},
		{PaneID: "worktree", Label: "ftr-0004-anchor", ForegroundCWD: "/checkout"},
	}
	checks := paneResourceChecks(history, panes, map[string]bool{"retained": true, "lost": true}, []string{"/project", "/checkout"})
	if len(checks) != 3 {
		t.Fatalf("checks = %+v", checks)
	}
	text, _ := json.Marshal(checks)
	for _, want := range []string{"untracked:", "worktree:", "active:"} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	for _, unwanted := range []string{"outside:", "manual:", "past:", "queued:"} {
		if strings.Contains(string(text), unwanted) {
			t.Fatalf("false positive %q: %s", unwanted, text)
		}
	}
}

func TestResourceDiagnosticsAreReadOnlyAndDoNotExposeTokens(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HVB_DATA_DIR", filepath.Join(dir, "data"))
	store, err := runs.Open("project")
	if err != nil {
		t.Fatal(err)
	}
	run := &runs.Run{ID: "active", FeatureID: "FTR-0001", Status: feature.InProgress, State: runs.Running, PaneID: "missing", LockToken: "private-run-token", LockOwner: "alice", StartedAt: time.Now(), Worktree: &runs.Worktree{Path: filepath.Join(dir, "lost-checkout")}}
	if err := store.Append(run); err != nil {
		t.Fatal(err)
	}
	pending := &runs.Run{ID: "pending", FeatureID: "FTR-0002", Status: feature.InProgress, State: runs.Succeeded, StartedAt: time.Now(), Completion: &runs.CompletionProgress{Captured: true, Released: true, LastError: "private-diagnostic-secret"}}
	if err := store.Append(pending); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	stub := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	git := stub("git", `case "$*" in
 'rev-parse --show-toplevel') printf '%s\n' "$PWD" ;;
 '--no-optional-locks worktree list --porcelain -z') printf 'worktree %s\000HEAD 000\000branch refs/heads/main\000\000worktree %s\000HEAD 111\000branch refs/heads/feature/FTR-9999/manual\000\000' "$PWD" "$PWD/../manual" ;;
 *) exit 99 ;;
esac
`)
	t.Setenv("HVB_GIT_BIN", git)
	vbBin := stub("vb", `case "$*" in
 *'lock FTR-0001 --status') echo '{"success":true,"data":{"id":"FTR-0001","locked":true,"owner":"bob","token":"private-successor-token","expires_at":"2099-01-01T00:00:00Z"}}' ;;
 *) exit 99 ;;
esac
`)
	// Only pane list is permitted; a stop/rename/trust command fails this stub.
	herdrBin := stub("herdr", `case "$*" in
 *'pane list') echo '{"id":"x","result":{"panes":[]}}' ;;
 *) exit 99 ;;
esac
`)
	app := &App{workspace: &workspace.Workspace{Root: root, Board: filepath.Join(root, ".virtualboard")}, store: store, vb: &vb.Client{Bin: vbBin, Root: root}, herdr: &herdrcli.Client{Bin: herdrBin}}
	checks := resourceChecks(context.Background(), app, true, true)
	raw, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run lease", "no longer matches", "run pane", "retained worktree", "worktree registration", "untracked worktree", "pending completion", "hvb run retry pending"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q: %s", want, raw)
		}
	}
	for _, secret := range []string{"private-run-token", "private-successor-token", "private-diagnostic-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret disclosed: %s", raw)
		}
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || string(before) != string(after) {
		t.Fatalf("store changed: %v", err)
	}
	files, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil || len(files) != 2 {
		t.Fatalf("unexpected files or recovery: %v %v", files, err)
	}
}
