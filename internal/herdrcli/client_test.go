package herdrcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHerdr writes a stub `herdr` that records argv and replies from a case
// statement on the subcommand. The recorded argv is the point: these tests pin
// the exact command lines verified against Herdr 0.9.0, so a careless edit to a
// flag name fails here rather than at dispatch time.
func fakeHerdr(t *testing.T, script string) (*Client, func() []string) {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	path := filepath.Join(dir, "herdr")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argvLog + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{Bin: path}, func() []string {
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

const supportedStatus = `cat <<'EOF'
client:
  version: 0.9.0
  channel: stable
  protocol: 22

server:
  status: running
  version: 0.9.0
  socket: /tmp/herdr.sock
EOF`

func TestStatusParsesTheYAMLishReport(t *testing.T) {
	client, _ := fakeHerdr(t, supportedStatus)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.ClientProtocol != 22 {
		t.Errorf("ClientProtocol = %d", status.ClientProtocol)
	}
	if status.ServerVersion != "0.9.0" || !status.ServerRunning {
		t.Errorf("server = %q running=%v", status.ServerVersion, status.ServerRunning)
	}
	if status.Socket != "/tmp/herdr.sock" {
		t.Errorf("Socket = %q", status.Socket)
	}
}

func TestGateAcceptsTheSupportedBuild(t *testing.T) {
	client, _ := fakeHerdr(t, supportedStatus)
	if err := client.Gate(context.Background()); err != nil {
		t.Fatalf("Gate on %s/protocol %d: %v", SupportedVersion, SupportedProtocol, err)
	}
}

// The gate is policy, not negotiation: a different version or protocol must
// fail before hvb creates layout or launches an agent.
func TestGateRejectsEverythingElse(t *testing.T) {
	cases := map[string]string{
		"old server":     strings.ReplaceAll(supportedStatus, "version: 0.9.0\n  socket", "version: 0.8.1\n  socket"),
		"wrong protocol": strings.ReplaceAll(supportedStatus, "protocol: 22", "protocol: 21"),
		"server down":    strings.ReplaceAll(supportedStatus, "status: running", "status: stopped"),
	}
	for name, script := range cases {
		client, _ := fakeHerdr(t, script)
		err := client.Gate(context.Background())
		if !errors.Is(err, ErrIncompatible) {
			t.Errorf("%s: Gate = %v, want ErrIncompatible", name, err)
		}
	}
}

func TestPanesDecodesTheEnvelope(t *testing.T) {
	client, _ := fakeHerdr(t, `cat <<'EOF'
{"id":"cli:pane:list","result":{"type":"pane_list","panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1","label":"ftr-0001","cwd":"/project","agent":"claude","agent_status":"working","focused":true},{"pane_id":"w1:p2","tab_id":"w1:t1","workspace_id":"w1","terminal_title_stripped":"zsh","cwd":"/project","agent_status":"unknown"}]}}
EOF`)
	panes, err := client.Panes(context.Background(), "w1")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("got %d panes", len(panes))
	}
	if panes[0].Name() != "ftr-0001" {
		t.Errorf("labelled pane Name() = %q", panes[0].Name())
	}
	// An unrenamed pane has no label and must fall back to its terminal title.
	if panes[1].Name() != "zsh" {
		t.Errorf("unlabelled pane Name() = %q, want the terminal title", panes[1].Name())
	}
}

func TestSplitPaneArgv(t *testing.T) {
	client, argv := fakeHerdr(t, `echo '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p9","tab_id":"w1:t1","workspace_id":"w1"}}}'`)
	pane, err := client.SplitPane(context.Background(), "w1:p1", "right", "/project",
		map[string]string{"HVB_RUN_ID": "r1", "HVB_FEATURE_ID": "FTR-0001"}, false)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if pane.PaneID != "w1:p9" {
		t.Errorf("PaneID = %q", pane.PaneID)
	}
	got := argv()[0]
	// Env must be sorted so the argv is reproducible.
	want := "pane split w1:p1 --direction right --cwd /project --env HVB_FEATURE_ID=FTR-0001 --env HVB_RUN_ID=r1 --no-focus"
	if got != want {
		t.Errorf("argv =\n  %q\nwant\n  %q", got, want)
	}
}

func TestStartAgentArgv(t *testing.T) {
	client, argv := fakeHerdr(t, "echo '{\"id\":\"x\",\"result\":{}}'")
	if err := client.StartAgent(context.Background(), "ftr-0001-ab12", "claude", "w1:p9", 90*time.Second, nil); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	want := "agent start ftr-0001-ab12 --kind claude --pane w1:p9 --timeout 90000"
	if got := argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestCreateTabArgvAndResult(t *testing.T) {
	client, argv := fakeHerdr(t, `echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"w1:t2","workspace_id":"w1","label":"ftr-0001"},"root_pane":{"pane_id":"w1:p2","tab_id":"w1:t2","workspace_id":"w1"}}}'`)
	tab, root, err := client.CreateTab(context.Background(), "w1", "/project", "ftr-0001", nil, false)
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if tab.TabID != "w1:t2" || root.PaneID != "w1:p2" {
		t.Errorf("tab=%+v root=%+v", tab, root)
	}
	want := "tab create --workspace w1 --cwd /project --label ftr-0001 --no-focus"
	if got := argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// Cleanup has to be idempotent: a run can be torn down twice, once by the user
// closing the pane and once by hvb reconciling.
func TestClosePaneTreatsAMissingPaneAsClosed(t *testing.T) {
	client, _ := fakeHerdr(t, `echo '{"id":"x","error":{"code":2,"kind":"not_found","message":"pane_not_found"}}' >&2
exit 1`)
	if err := client.ClosePane(context.Background(), "w1:p9"); err != nil {
		t.Fatalf("ClosePane on a missing pane = %v, want nil", err)
	}
}

func TestErrorEnvelopeIsSurfaced(t *testing.T) {
	client, _ := fakeHerdr(t, `echo '{"id":"x","error":{"code":4,"kind":"herdr","message":"workspace_not_found"}}'
exit 0`)
	_, err := client.Panes(context.Background(), "")
	var herdrErr *Error
	if !errors.As(err, &herdrErr) {
		t.Fatalf("error = %v, want *herdrcli.Error", err)
	}
	if herdrErr.Code != 4 || herdrErr.Message != "workspace_not_found" {
		t.Errorf("error = %+v", herdrErr)
	}
}

// Herdr reports the kernel's resolved cwd, so /tmp on macOS comes back as
// /private/tmp. Comparing the strings as given would silently fail to find a
// workspace that is right there.
func TestWorkspaceForPathResolvesSymlinks(t *testing.T) {
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	client, _ := fakeHerdr(t, `cat <<EOF
{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w3:p1","workspace_id":"w3","cwd":"`+realDir+`"}]}}
EOF`)
	id, found, err := client.WorkspaceForPath(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if !found || id != "w3" {
		t.Fatalf("WorkspaceForPath through a symlink = %q, %v; want w3, true", id, found)
	}
}

// `pane read` is the one command that prints terminal content instead of a JSON
// envelope. Decoding it as one would return empty output for every pane.
func TestReadPaneReturnsRawTerminalText(t *testing.T) {
	client, argv := fakeHerdr(t, `printf 'line one\nline two\n'`)
	out, err := client.ReadPane(context.Background(), "w1:p1", "recent-unwrapped", 40)
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Fatalf("ReadPane = %q, want the terminal content", out)
	}
	want := "pane read w1:p1 --source recent-unwrapped --lines 40"
	if got := argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// A pane whose content happens to be JSON must come back as content, not be
// mistaken for a response envelope.
func TestReadPaneDoesNotSwallowJSONContent(t *testing.T) {
	client, _ := fakeHerdr(t, `printf '{"some":"json the pane printed"}\n'`)
	out, err := client.ReadPane(context.Background(), "w1:p1", "visible", 0)
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if !strings.Contains(out, "json the pane printed") {
		t.Fatalf("ReadPane = %q, want the pane's own output", out)
	}
}

func TestValidKindMatchesTheHerdrList(t *testing.T) {
	for _, kind := range []string{"claude", "pi", "codex", "opencode", "agy"} {
		if !ValidKind(kind) {
			t.Errorf("%s should be a valid harness", kind)
		}
	}
	for _, kind := range []string{"", "gpt", "claude-code"} {
		if ValidKind(kind) {
			t.Errorf("%q should not be a valid harness", kind)
		}
	}
}

func TestSessionFlagIsPrepended(t *testing.T) {
	client, argv := fakeHerdr(t, `echo '{"id":"x","result":{"panes":[]}}'`)
	client.Session = "work"
	if _, err := client.Panes(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := argv()[0]; got != "--session work pane list" {
		t.Fatalf("argv = %q", got)
	}
}

func TestPanesRejectsIncompleteInventory(t *testing.T) {
	for _, result := range []string{`{}`, `{"panes":null}`, `{"panes":[{}]}`, `{"panes":[{"pane_id":"same"},{"pane_id":"same"}]}`} {
		t.Run(result, func(t *testing.T) {
			client, _ := fakeHerdr(t, "echo '{\"id\":\"x\",\"result\":"+result+"}'")
			if _, err := client.Panes(context.Background(), ""); err == nil {
				t.Fatal("incomplete inventory accepted as authoritative")
			}
		})
	}
}
