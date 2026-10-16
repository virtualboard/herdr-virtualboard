package feature

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestLifecycleMatchesSharedContract(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/workflow-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Statuses         []string
		Transitions      map[string][]string
		TerminalStatuses []string `json:"terminal_statuses"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, status := range Statuses {
		actual = append(actual, string(status))
	}
	if !reflect.DeepEqual(actual, contract.Statuses) || len(allowedTransitions) != len(contract.Statuses) {
		t.Fatal("board lifecycle drifted from shared contract")
	}
	terminal := map[string]bool{}
	for _, status := range contract.TerminalStatuses {
		terminal[status] = true
	}
	for _, from := range contract.Statuses {
		allowed := map[string]bool{}
		for _, to := range contract.Transitions[from] {
			allowed[to] = true
		}
		if Status(from).Terminal() != terminal[from] || Status(from).Dir() != "features/"+from {
			t.Fatalf("lifecycle metadata mismatch: %s", from)
		}
		for _, to := range contract.Statuses {
			if got := CanTransition(Status(from), Status(to)); got != allowed[to] {
				t.Errorf("%s -> %s: allowed=%v, contract=%v", from, to, got, allowed[to])
			}
		}
	}
}
