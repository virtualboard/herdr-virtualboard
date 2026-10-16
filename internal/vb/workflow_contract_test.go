package vb

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDispatchCapabilitiesMatchSharedContract(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/workflow-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Doctor struct {
			DispatchCapabilities []string `json:"dispatch_capabilities"`
		}
		VB struct {
			ExitCodes map[string]int `json:"exit_codes"`
		}
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if err := (&DoctorReport{Capabilities: contract.Doctor.DispatchCapabilities}).RequireDispatchCapabilities(); err != nil {
		t.Fatal(err)
	}
	for omitted := range contract.Doctor.DispatchCapabilities {
		capabilities := append([]string{}, contract.Doctor.DispatchCapabilities[:omitted]...)
		capabilities = append(capabilities, contract.Doctor.DispatchCapabilities[omitted+1:]...)
		if err := (&DoctorReport{Capabilities: capabilities}).RequireDispatchCapabilities(); err == nil {
			t.Errorf("missing capability accepted: %s", contract.Doctor.DispatchCapabilities[omitted])
		}
	}
	actual := map[string]int{"success": ExitSuccess, "validation": ExitValidation, "not_found": ExitNotFound, "invalid_transition": ExitInvalidTransition, "dependency": ExitDependency, "lock_conflict": ExitLockConflict, "filesystem": ExitFilesystem, "schema": ExitSchema, "external_command": ExitExternalCommand, "unknown": ExitUnknown}
	for name, value := range actual {
		if contract.VB.ExitCodes[name] != value {
			t.Errorf("vb wrapper %s=%d, contract=%d", name, value, contract.VB.ExitCodes[name])
		}
	}
}
