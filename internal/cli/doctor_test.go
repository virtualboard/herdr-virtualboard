package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

func TestDoctorDetectsCorruptStoreWithoutChangingIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HVB_DATA_DIR", dir)
	store, err := runs.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{broken JSON")
	if err := os.WriteFile(store.Path(), original, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "vb")
	script := `#!/bin/sh
case "$*" in
 *version*) echo test ;;
 *) echo '{"success":false,"data":{"format_version":1,"ok":false,"version":"test","capabilities":[],"checks":[{"name":"schema","ok":false,"fatal":true,"detail":"invalid schema","remedy":"restore schema"}]}}'; exit 1 ;;
esac
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := &App{Out: &out, Err: &out, JSONFlag: true, workspace: &workspace.Workspace{Root: dir, Board: filepath.Join(dir, ".virtualboard")}, store: store, vb: &vb.Client{Bin: stub}, herdr: &herdrcli.Client{Bin: filepath.Join(dir, "missing-herdr")}}
	command := newDoctorCommand(app)
	command.SetArgs(nil)
	if err := command.Execute(); err == nil {
		t.Fatal("corrupt store reported healthy")
	}
	for _, text := range []string{`"ok": false`, `"run store"`, "hvb run recover", "vb schema", "restore schema"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %q in %s", text, out.String())
		}
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("doctor modified corrupt store: %q, %v", after, err)
	}
	files, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil || len(files) != 1 {
		t.Fatalf("doctor created recovery or lock files: %v, %v", files, err)
	}
}
