package vb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// DoctorCheck is a read-only observation from vb. Remedy describes a manual
// next step; consuming this report never authorizes a repair.
type DoctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fatal  bool   `json:"fatal"`
	Remedy string `json:"remedy,omitempty"`
}

type DoctorReport struct {
	FormatVersion   int           `json:"format_version"`
	OK              bool          `json:"ok"`
	Version         string        `json:"version"`
	Workspace       string        `json:"workspace"`
	TemplateVersion string        `json:"template_version,omitempty"`
	Capabilities    []string      `json:"capabilities"`
	Checks          []DoctorCheck `json:"checks"`
}

// Doctor preserves a complete unhealthy report returned with validation exit 1.
// Other exits, malformed reports, and contradictory health summaries fail closed.
func (c *Client) Doctor(ctx context.Context, capabilitiesOnly bool) (*DoctorReport, error) {
	args := []string{"--json", "doctor"}
	if capabilitiesOnly {
		args = append(args, "--capabilities-only")
	}
	raw, callErr := c.raw(ctx, args...)
	var data json.RawMessage
	if callErr != nil {
		var failure *Error
		if !errors.As(callErr, &failure) || failure.Code != ExitValidation || len(failure.Data) == 0 {
			return nil, callErr
		}
		data = failure.Data
	} else {
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("decode vb doctor envelope: %w", err)
		}
		if !envelope.Success {
			return nil, fmt.Errorf("vb doctor returned failure with exit 0")
		}
		data = envelope.Data
	}
	var report DoctorReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("decode vb doctor: %w", err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if report.FormatVersion != 1 || report.Version == "" || len(report.Checks) == 0 || report.Capabilities == nil || string(fields["ok"]) != fmt.Sprint(report.OK) {
		return nil, fmt.Errorf("unsupported or incomplete vb doctor response; install a compatible vb CLI")
	}
	var checks []map[string]json.RawMessage
	_ = json.Unmarshal(fields["checks"], &checks)
	healthy := true
	for i, check := range report.Checks {
		if check.Name == "" || check.Detail == "" || string(checks[i]["ok"]) != fmt.Sprint(check.OK) || string(checks[i]["fatal"]) != fmt.Sprint(check.Fatal) {
			return nil, fmt.Errorf("incomplete vb doctor check")
		}
		if !check.OK && check.Fatal {
			healthy = false
		}
	}
	if report.OK != healthy || (callErr == nil) != healthy {
		return nil, fmt.Errorf("inconsistent vb doctor health or exit status")
	}
	return &report, nil
}

// RequireDispatchCapabilities checks actual interfaces rather than inferring
// safety from a version number maintained by another repository.
func (r *DoctorReport) RequireDispatchCapabilities() error {
	required := []string{"owner-leases", "lease-tokens", "lease-renewal", "raw-schema-validation", "validation-exit-status"}
	available := map[string]bool{}
	if r != nil {
		for _, capability := range r.Capabilities {
			available[capability] = true
		}
	}
	var missing []string
	for _, capability := range required {
		if !available[capability] {
			missing = append(missing, capability)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("vb lacks dispatch capabilities %v; install a compatible vb CLI and run hvb doctor", missing)
	}
	return nil
}

func (c *Client) CheckDispatchCompatibility(ctx context.Context) error {
	report, err := c.Doctor(ctx, true)
	if err != nil {
		return fmt.Errorf("vb compatibility check failed: %w", err)
	}
	if !report.OK {
		return fmt.Errorf("vb runtime diagnostics failed; inspect vb doctor --capabilities-only")
	}
	return report.RequireDispatchCapabilities()
}
