package cli

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCLIInterfacesMatchSharedContract(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/workflow-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		HVB struct {
			GlobalFlags []string `json:"global_flags"`
			Commands    map[string][]string
			ExitCodes   map[string]int `json:"exit_codes"`
		}
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	root := NewRoot(&App{})
	for _, flag := range contract.HVB.GlobalFlags {
		if root.PersistentFlags().Lookup(flag) == nil {
			t.Errorf("missing global flag %s", flag)
		}
	}
	for path, flags := range contract.HVB.Commands {
		command, rest, err := root.Find(strings.Fields(path))
		if err != nil || len(rest) != 0 || command == root {
			t.Errorf("missing command %s: %v", path, err)
			continue
		}
		for _, flag := range flags {
			if command.Flags().Lookup(flag) == nil {
				t.Errorf("%s missing flag %s", path, flag)
			}
		}
	}
	actual := map[string]int{"success": ExitOK, "validation": ExitValidation, "not_found": ExitNotFound, "invalid_transition": ExitBadTransition, "dependency": ExitDependency, "lock_conflict": ExitLockConflict, "herdr_unavailable": ExitHerdrUnavailable, "usage": ExitUsage}
	if !reflect.DeepEqual(actual, contract.HVB.ExitCodes) {
		t.Fatalf("exit codes differ: %v", actual)
	}
}
