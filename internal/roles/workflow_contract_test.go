package roles

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

func TestRoleSelectionMatchesSharedContract(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/workflow-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Roles struct {
			Keys           []string
			SelectionCases []struct {
				Status         feature.Status
				Role           string
				Labels         []string
				Fallback, Want string
			} `json:"selection_cases"`
		}
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	var roles []Role
	for _, key := range contract.Roles.Keys {
		roles = append(roles, Role{Key: key, Name: key})
	}
	for _, test := range contract.Roles.SelectionCases {
		role, found := Suggest(roles, &feature.Spec{Frontmatter: feature.Frontmatter{Status: test.Status, Role: test.Role, Labels: test.Labels}}, test.Fallback)
		if !found || role.Key != test.Want {
			t.Errorf("%+v selected %q, found=%v", test, role.Key, found)
		}
	}
}
