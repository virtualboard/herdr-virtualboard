package runs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIncompleteDocumentsRequireExplicitRecovery(t *testing.T) {
	for _, original := range []string{
		`null`, `{}`, `{"version":2}`, `{"runs":[]}`, `{"version":null,"runs":[]}`,
		`{"version":2,"runs":{}}`, `{"version":2,"runs":[{"id":"orphan"}]}`,
		`{"version":2,"runs":[],"unknown_metadata":"preserve me"}`,
	} {
		t.Run(original, func(t *testing.T) {
			store := newStore(t)
			if err := writeFile(store.Path(), original); err != nil {
				t.Fatal(err)
			}
			if _, err := store.List(); err == nil {
				t.Fatal("incomplete store treated as empty")
			}
			if err := store.Append(newRun("new", "FTR-0001", Running)); err == nil {
				t.Fatal("mutation discarded incomplete data")
			}
			before, err := os.ReadFile(store.Path())
			if err != nil || string(before) != original {
				t.Fatalf("read/mutation changed original: %q %v", before, err)
			}
			backup, err := store.Recover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			retained, err := os.ReadFile(backup)
			if err != nil || string(retained) != original {
				t.Fatalf("quarantine lost original: %q %v", retained, err)
			}
			list, err := store.List()
			if err != nil || len(list) != 0 {
				t.Fatalf("explicit recovery did not create readable empty store: %v %v", list, err)
			}
		})
	}
}

func TestFutureFormatIsProtectedBeforeUnknownFieldDecoding(t *testing.T) {
	store := newStore(t)
	original := `{"version":3,"runs":[],"future_metadata":{"keep":true}}`
	if err := writeFile(store.Path(), original); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("future version classification: %v", err)
	}
	if _, err := store.Recover(context.Background()); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("future version recovered: %v", err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || string(after) != original {
		t.Fatalf("future bytes changed: %q %v", after, err)
	}
	matches, err := filepath.Glob(store.Path() + ".corrupt-*")
	if err != nil || len(matches) != 0 {
		t.Fatalf("future store quarantined: %v %v", matches, err)
	}
}

func TestVersionOneAndEmptyNullArrayRemainReadable(t *testing.T) {
	for _, raw := range []string{`{"version":1,"runs":[]}`, `{"version":2,"project":"legacy","runs":null}`} {
		t.Run(raw, func(t *testing.T) {
			store := newStore(t)
			if err := writeFile(store.Path(), raw); err != nil {
				t.Fatal(err)
			}
			list, err := store.List()
			if err != nil || len(list) != 0 {
				t.Fatalf("legacy empty store: %v %v", list, err)
			}
			if err := store.Append(newRun("r1", "FTR-0001", Running)); err != nil {
				t.Fatal(err)
			}
			bytes, err := os.ReadFile(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			var doc document
			if err := json.Unmarshal(bytes, &doc); err != nil || doc.Version != version || len(doc.Runs) != 1 {
				t.Fatalf("migration: %s %v", bytes, err)
			}
		})
	}
}

func TestInvalidRecordMutationsPreserveStoredBytes(t *testing.T) {
	store := newStore(t)
	if err := store.Append(newRun("r1", "FTR-0001", Running)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("r1", func(run *Run) error { run.State = "unknown-state"; return nil }); err == nil {
		t.Fatal("invalid state persisted")
	}
	if err := store.Append(newRun("r1", "FTR-0002", Running)); err == nil {
		t.Fatal("duplicate record persisted")
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed mutation changed bytes: %v", err)
	}
}
