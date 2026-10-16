package vb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// NewResult is the data payload of `vb new --json`.
type NewResult struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Path   string   `json:"path"`
	Labels []string `json:"labels"`
}

// New creates a feature spec in backlog. Labels must be kebab-case; the
// frontmatter schema rejects anything else, and vb surfaces that as a
// validation error rather than writing a spec that later fails `vb validate`.
func (c *Client) New(ctx context.Context, title string, labels ...string) (*NewResult, error) {
	args := append([]string{"new", title}, labels...)
	envelope, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var result NewResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return nil, fmt.Errorf("decode vb new result: %w", err)
	}
	return &result, nil
}

// MoveResult is the data payload of `vb move --json`.
type MoveResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Owner   string `json:"owner"`
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// Move transitions a feature and optionally sets its owner. An empty owner
// leaves the current one untouched; pass ClearOwner to release it.
func (c *Client) Move(ctx context.Context, id string, status feature.Status, owner string) (*MoveResult, error) {
	args := []string{"move", id, string(status)}
	if owner != "" {
		args = append(args, "--owner", owner)
	}
	envelope, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var result MoveResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return nil, fmt.Errorf("decode vb move result: %w", err)
	}
	return &result, nil
}

// ClearOwner is the owner value that releases a feature. The VirtualBoard
// feature template writes it for an unclaimed feature, so setting it is how a
// move gives a feature back rather than leaving a stale owner behind.
const ClearOwner = feature.Unassigned

// Update sets frontmatter fields and body sections. Both maps may be empty,
// but calling with neither is a caller bug and returns an error rather than a
// no-op invocation, because vb would then rewrite `updated` for no reason.
func (c *Client) Update(ctx context.Context, id string, fields map[string]string, sections map[string]string) error {
	if len(fields) == 0 && len(sections) == 0 {
		return fmt.Errorf("update %s: no fields or sections given", id)
	}
	args := []string{"update", id}
	for _, key := range sortedKeys(fields) {
		args = append(args, "--field", key+"="+fields[key])
	}
	for _, key := range sortedKeys(sections) {
		args = append(args, "--body-section", key+"="+sections[key])
	}
	_, err := c.run(ctx, args...)
	return err
}

// Delete removes a feature spec, without the interactive confirmation.
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.run(ctx, "delete", id, "--force")
	return err
}

// Lock is the data payload of `vb lock --json`.
type Lock struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	StartedAt  string `json:"started_at"`
	ExpiresAt  string `json:"expires_at"`
	TTLMinutes int    `json:"ttl_minutes"`
	Expired    bool   `json:"expired"`
	Token      string `json:"token,omitempty"`
}

// Acquire takes the feature lock for an owner. force overrides a live lock held
// by someone else, which the board only does on an explicit user confirmation.
func (c *Client) Acquire(ctx context.Context, id, owner string, ttlMinutes int, force bool) (*Lock, error) {
	args := []string{"lock", id, "--owner", owner}
	if ttlMinutes > 0 {
		args = append(args, "--ttl", fmt.Sprint(ttlMinutes))
	}
	if force {
		args = append(args, "--force")
	}
	return c.lockCall(ctx, args)
}

func (c *Client) Renew(ctx context.Context, id, token string) (*Lock, error) {
	return c.lockCall(ctx, []string{"lock", id, "--renew", "--token", token})
}

// Release drops the feature lock.
func (c *Client) Release(ctx context.Context, id string, token ...string) error {
	args := []string{"lock", id, "--release"}
	if len(token) > 0 && token[0] != "" {
		args = append(args, "--token", token[0])
	}
	_, err := c.run(ctx, args...)
	return err
}

// LockStatus reports the current lock, or nil when the feature is unlocked.
func (c *Client) LockStatus(ctx context.Context, id string) (*Lock, error) {
	envelope, err := c.run(ctx, "lock", id, "--status")
	if err != nil {
		var vbErr *Error
		if errorsAs(err, &vbErr) && vbErr.Code == ExitNotFound {
			return nil, nil
		}
		return nil, err
	}
	var status struct {
		Lock
		Locked *bool `json:"locked"`
	}
	if err := json.Unmarshal(envelope.Data, &status); err != nil {
		return nil, fmt.Errorf("decode vb lock status: %w", err)
	}
	if status.ID != id {
		return nil, fmt.Errorf("vb lock status did not confirm feature %s", id)
	}
	if status.Owner == "" {
		if status.Locked == nil || *status.Locked || status.Token != "" {
			return nil, fmt.Errorf("vb lock status did not explicitly confirm an unlocked feature")
		}
		return nil, nil
	}
	if status.Locked != nil && !*status.Locked {
		return nil, fmt.Errorf("vb lock status contradicts its owner metadata")
	}
	return &status.Lock, nil
}

func (c *Client) lockCall(ctx context.Context, args []string) (*Lock, error) {
	envelope, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var lock Lock
	if err := json.Unmarshal(envelope.Data, &lock); err != nil {
		return nil, fmt.Errorf("decode vb lock result: %w", err)
	}
	return &lock, nil
}

// ValidationResult is the data payload of `vb validate --json`.
type ValidationResult struct {
	Target     string          `json:"target"`
	FixApplied bool            `json:"fix_applied"`
	Features   ValidationGroup `json:"features"`
	Specs      ValidationGroup `json:"specs"`
	// Message carries vb's own text when validation failed before vb could
	// produce a structured report.
	Message string `json:"message,omitempty"`
}

// ValidationGroup is the per-kind validation summary.
type ValidationGroup struct {
	Total   int                        `json:"total"`
	Valid   int                        `json:"valid"`
	Invalid int                        `json:"invalid"`
	Results map[string]ValidationEntry `json:"results"`
}

// ValidationEntry is one validated feature or spec.
type ValidationEntry struct {
	Status string   `json:"status"`
	Errors []string `json:"errors"`
}

// OK reports whether nothing failed validation.
func (r *ValidationResult) OK() bool { return r.Features.Invalid == 0 && r.Specs.Invalid == 0 }

// Validate runs `vb validate`. target may be empty (everything), a feature id,
// or a spec filename. fix applies vb's safe fixes first.
func (c *Client) Validate(ctx context.Context, target string, fix bool) (*ValidationResult, error) {
	args := []string{"validate"}
	if target != "" {
		args = append(args, target)
	}
	if fix {
		args = append(args, "--fix")
	}
	envelope, err := c.run(ctx, args...)
	if err != nil {
		// vb exits non-zero when validation fails; that is a result, not a
		// transport failure, and the caller wants the details either way.
		var vbErr *Error
		if errorsAs(err, &vbErr) && (vbErr.Code == ExitValidation || vbErr.Code == ExitSchema) {
			if len(vbErr.Data) > 0 {
				result, decodeErr := decodeValidation(target, vbErr.Data)
				if decodeErr == nil && result.OK() {
					return nil, fmt.Errorf("vb validation failure contradicts its successful report")
				}
				return result, decodeErr
			}
			return &ValidationResult{Target: target, Message: vbErr.Message,
				Features: ValidationGroup{Invalid: 1}}, nil
		}
		return nil, err
	}
	return decodeValidation(target, envelope.Data)
}

func decodeValidation(target string, data json.RawMessage) (*ValidationResult, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("incomplete vb validation response")
	}
	var result ValidationResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode vb validate result: %w", err)
	}
	// vb's individual-feature response predates the combined group shape.
	// Normalize both so callers never mistake missing group counts for success.
	var single struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Status string   `json:"status"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(data, &single); err != nil {
		return nil, err
	}
	if single.ID != "" || single.Name != "" {
		if _, found := fields["errors"]; !found {
			return nil, fmt.Errorf("vb validation response omitted errors")
		}
		if single.ID != "" && single.Name != "" {
			return nil, fmt.Errorf("ambiguous vb validation response")
		}
		if len(single.Errors) == 0 && single.Status == "" {
			return nil, fmt.Errorf("vb validation response omitted status")
		}
		group := ValidationGroup{Total: 1, Results: map[string]ValidationEntry{}}
		if len(single.Errors) > 0 {
			group.Invalid = 1
		} else {
			group.Valid = 1
		}
		id := single.ID
		if id == "" {
			id = single.Name
		}
		group.Results[id] = ValidationEntry{Status: single.Status, Errors: single.Errors}
		if single.ID != "" {
			result.Features = group
		} else {
			result.Specs = group
		}
	}
	if single.ID == "" && single.Name == "" {
		found := false
		for _, name := range []string{"features", "specs"} {
			raw, present := fields[name]
			if !present {
				continue
			}
			found = true
			if err := validateGroupReport(raw); err != nil {
				return nil, fmt.Errorf("decode vb validate %s: %w", name, err)
			}
		}
		if !found {
			return nil, fmt.Errorf("vb validation response omitted results")
		}
	}
	result.Target = target
	return &result, nil
}

func validateGroupReport(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("missing summary")
	}
	var group ValidationGroup
	if err := json.Unmarshal(raw, &group); err != nil {
		return err
	}
	for _, name := range []string{"total", "valid", "invalid", "results"} {
		value, ok := fields[name]
		if !ok || string(value) == "null" {
			return fmt.Errorf("missing %s", name)
		}
	}
	if group.Total < 0 || group.Valid < 0 || group.Invalid < 0 || group.Valid > group.Total || group.Invalid != group.Total-group.Valid || group.Results == nil || len(group.Results) < group.Valid || len(group.Results) > group.Total {
		return fmt.Errorf("inconsistent summary counts")
	}
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(fields["results"], &entries); err != nil {
		return err
	}
	invalid := 0
	for id, entry := range group.Results {
		if id == "" {
			return fmt.Errorf("empty result identifier")
		}
		if _, ok := entries[id]["errors"]; !ok {
			return fmt.Errorf("result %s omitted errors", id)
		}
		if len(entry.Errors) > 0 {
			invalid++
		} else if entry.Status == "" {
			return fmt.Errorf("result %s omitted status", id)
		}
	}
	// Duplicate identifiers can collapse invalid entries in older vb reports.
	// Preserve those diagnostics, but never accept an error as a successful count.
	if invalid > group.Invalid || len(group.Results)-invalid != group.Valid {
		return fmt.Errorf("results contradict summary")
	}
	return nil
}

// Index regenerates `features/INDEX.md`.
func (c *Client) Index(ctx context.Context) error {
	_, err := c.run(ctx, "index")
	return err
}

// IndexEntry is one row of `vb index --format json`.
type IndexEntry struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Owner      string   `json:"owner"`
	Priority   string   `json:"priority"`
	Complexity string   `json:"complexity"`
	Labels     []string `json:"labels"`
	Updated    string   `json:"updated"`
	Path       string   `json:"path"`
}

// IndexSnapshot is the decoded `vb index --format json --json` document.
type IndexSnapshot struct {
	Generated string         `json:"generated"`
	Features  []IndexEntry   `json:"features"`
	Summary   map[string]int `json:"summary"`
}

// Snapshot reads the whole board through vb without writing INDEX.md. The TUI
// reads specs off disk instead, because it needs bodies as well as headers;
// this exists for `hvb feature list --source vb` and for cross-checking that
// hvb's own reader agrees with vb.
func (c *Client) Snapshot(ctx context.Context) (*IndexSnapshot, error) {
	envelope, err := c.run(ctx, "index", "--format", "json")
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(envelope.Data, &wrapper); err != nil {
		return nil, fmt.Errorf("decode vb index result: %w", err)
	}
	var snapshot IndexSnapshot
	if err := json.Unmarshal([]byte(wrapper.Content), &snapshot); err != nil {
		return nil, fmt.Errorf("decode vb index content: %w", err)
	}
	return &snapshot, nil
}

// sortedKeys orders map keys so the argv hvb builds is deterministic, which is
// what lets the tests assert an exact command line.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
