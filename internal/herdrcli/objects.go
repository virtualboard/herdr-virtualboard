package herdrcli

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Workspace is one entry of `herdr workspace list`.
type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	ActiveTabID string `json:"active_tab_id"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
	TabCount    int    `json:"tab_count"`
	PaneCount   int    `json:"pane_count"`
	// Worktree is present only when the workspace is a linked worktree
	// checkout rather than an ordinary directory.
	Worktree *WorktreeWorkspace `json:"worktree,omitempty"`
}

// Tab is one entry of `herdr tab list`.
type Tab struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
}

// Pane is one entry of `herdr pane list`.
//
// `label` is present only once something has renamed the pane; an untouched
// pane reports its terminal title instead, which is why Name prefers the label
// and falls back rather than assuming either exists.
type Pane struct {
	PaneID        string `json:"pane_id"`
	TabID         string `json:"tab_id"`
	WorkspaceID   string `json:"workspace_id"`
	Label         string `json:"label"`
	TerminalTitle string `json:"terminal_title_stripped"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Agent         string `json:"agent"`
	AgentStatus   string `json:"agent_status"`
	Focused       bool   `json:"focused"`
}

// Name is the pane's user-visible name, preferring the explicit label hvb sets
// with `pane rename` over the terminal-derived title.
func (p Pane) Name() string {
	if p.Label != "" {
		return p.Label
	}
	return p.TerminalTitle
}

// Agent is one entry of `herdr agent list`.
type Agent struct {
	Agent            string        `json:"agent"`
	AgentStatus      string        `json:"agent_status"`
	PaneID           string        `json:"pane_id"`
	TabID            string        `json:"tab_id"`
	WorkspaceID      string        `json:"workspace_id"`
	CWD              string        `json:"cwd"`
	TerminalTitle    string        `json:"terminal_title_stripped"`
	Focused          bool          `json:"focused"`
	InteractiveReady bool          `json:"interactive_ready"`
	LaunchPending    bool          `json:"launch_pending"`
	AgentSession     *AgentSession `json:"agent_session"`
}

// AgentSession is the harness conversation reference a Herdr integration
// reports. It is absent on panes whose harness has no integration installed.
type AgentSession struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

// Agent lifecycle states, as Herdr reports them. `idle` and `done` both mean
// ready for input; `unknown` means an agent is present but unclassified and
// must not be read as completion.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusUnknown = "unknown"
)

// Kinds are the agent harnesses `herdr agent start --kind` accepts on Herdr
// 0.9.0. Verified with `herdr agent`; re-read it rather than editing this list
// from memory.
var Kinds = []string{
	"pi", "claude", "codex", "gemini", "cursor", "devin", "agy", "cline", "omp",
	"mastracode", "opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok",
	"hermes", "kilo", "qodercli", "qwen", "maki", "muse",
}

// ValidKind reports whether kind is a harness Herdr can start.
func ValidKind(kind string) bool {
	for _, candidate := range Kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

// Workspaces lists every workspace in the session.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	var result struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := c.call(ctx, &result, "workspace", "list"); err != nil {
		return nil, err
	}
	return result.Workspaces, nil
}

// WorkspaceForPath finds an open workspace whose panes run in dir. Herdr does
// not index workspaces by path, so this resolves through the pane list — the
// same thing a human does by eye.
//
// Herdr reports the kernel's resolved cwd, so a caller passing /tmp on macOS
// gets back /private/tmp. Both sides are resolved before comparing; matching
// the strings as given silently fails to find a workspace that is right there.
func (c *Client) WorkspaceForPath(ctx context.Context, dir string) (string, bool, error) {
	panes, err := c.Panes(ctx, "")
	if err != nil {
		return "", false, err
	}
	for _, pane := range panes {
		if sameDir(pane.CWD, dir) {
			return pane.WorkspaceID, true, nil
		}
	}
	return "", false, nil
}

// CreateWorkspace opens a workspace rooted at cwd.
func (c *Client) CreateWorkspace(ctx context.Context, cwd, label string, env map[string]string, focus bool) (*Workspace, *Tab, *Pane, error) {
	args := []string{"workspace", "create"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	if label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, envArgs(env)...)
	args = append(args, focusFlag(focus))

	var result struct {
		Workspace Workspace `json:"workspace"`
		Tab       Tab       `json:"tab"`
		RootPane  Pane      `json:"root_pane"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, nil, nil, err
	}
	return &result.Workspace, &result.Tab, &result.RootPane, nil
}

// Tabs lists the tabs of a workspace, or of every workspace when empty.
func (c *Client) Tabs(ctx context.Context, workspaceID string) ([]Tab, error) {
	args := []string{"tab", "list"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	var result struct {
		Tabs []Tab `json:"tabs"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, err
	}
	return result.Tabs, nil
}

// CreateTab opens a tab with a shell root pane. The root pane is the anchor a
// run child is split from; see dispatch for why the split is pane-first.
func (c *Client) CreateTab(ctx context.Context, workspaceID, cwd, label string, env map[string]string, focus bool) (*Tab, *Pane, error) {
	args := []string{"tab", "create"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	if label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, envArgs(env)...)
	args = append(args, focusFlag(focus))

	var result struct {
		Tab      Tab  `json:"tab"`
		RootPane Pane `json:"root_pane"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, nil, err
	}
	return &result.Tab, &result.RootPane, nil
}

// RenameTab sets a tab's label.
func (c *Client) RenameTab(ctx context.Context, tabID, label string) error {
	return c.call(ctx, nil, "tab", "rename", tabID, label)
}

// CloseTab closes a tab and everything in it.
func (c *Client) CloseTab(ctx context.Context, tabID string) error {
	return c.call(ctx, nil, "tab", "close", tabID)
}

// Panes lists panes, optionally scoped to one workspace.
func (c *Client) Panes(ctx context.Context, workspaceID string) ([]Pane, error) {
	args := []string{"pane", "list"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	var result struct {
		Panes []Pane `json:"panes"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, err
	}
	if result.Panes == nil {
		return nil, errors.New("Herdr pane list omitted its panes array")
	}
	seen := map[string]bool{}
	for _, pane := range result.Panes {
		if pane.PaneID == "" || seen[pane.PaneID] {
			return nil, errors.New("Herdr pane list returned empty or duplicate pane IDs")
		}
		seen[pane.PaneID] = true
	}
	return result.Panes, nil
}

// GetPane reads one pane. A missing pane is reported as (nil, nil) so callers
// checking liveness do not have to match on error text.
func (c *Client) GetPane(ctx context.Context, paneID string) (*Pane, error) {
	var result struct {
		Pane Pane `json:"pane"`
	}
	if err := c.call(ctx, &result, "pane", "get", paneID); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &result.Pane, nil
}

// SplitPane splits an existing pane and returns the child. The child inherits
// nothing but what is passed here, so cwd and env must both be explicit.
func (c *Client) SplitPane(ctx context.Context, paneID, direction, cwd string, env map[string]string, focus bool) (*Pane, error) {
	if direction == "" {
		direction = "right"
	}
	args := []string{"pane", "split", paneID, "--direction", direction}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	args = append(args, envArgs(env)...)
	args = append(args, focusFlag(focus))

	var result struct {
		Pane Pane `json:"pane"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, err
	}
	return &result.Pane, nil
}

// RenamePane sets a pane's label.
func (c *Client) RenamePane(ctx context.Context, paneID, label string) error {
	return c.call(ctx, nil, "pane", "rename", paneID, label)
}

// ClosePane closes a pane. A pane that is already gone counts as closed:
// cleanup must be idempotent because a run can be torn down twice (by the user
// in the UI, then by hvb reconciling).
func (c *Client) ClosePane(ctx context.Context, paneID string) error {
	err := c.call(ctx, nil, "pane", "close", paneID)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

// FocusPane focuses a pane, switching the user's view to it.
func (c *Client) FocusPane(ctx context.Context, paneID string) error {
	return c.call(ctx, nil, "plugin", "pane", "focus", paneID)
}

// ReadPane returns recent terminal output. `recent-unwrapped` joins soft wraps
// and is the right source for transcripts and logs.
//
// Unlike every other command here, `pane read` prints the terminal content
// itself rather than a JSON envelope — verified against Herdr 0.9.0. A pane
// whose content happens to start with `{` would otherwise be mistaken for a
// response, so the envelope is only consulted when it actually parses and
// carries an error.
func (c *Client) ReadPane(ctx context.Context, paneID, source string, lines int) (string, error) {
	if source == "" {
		source = "recent-unwrapped"
	}
	args := []string{"pane", "read", paneID, "--source", source}
	if lines > 0 {
		args = append(args, "--lines", strconv.Itoa(lines))
	}
	raw, err := c.raw(ctx, args...)
	if err != nil {
		return "", err
	}
	var env envelope
	if json.Unmarshal(raw, &env) == nil && env.Error != nil {
		return "", &Error{Args: args, Code: env.Error.Code, Message: env.Error.Message}
	}
	return string(raw), nil
}

// Agents lists live agents across the session.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var result struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.call(ctx, &result, "agent", "list"); err != nil {
		return nil, err
	}
	return result.Agents, nil
}

// GetAgent reads one agent by unique name or by the pane id hosting it.
// A target with no live agent is reported as (nil, nil).
func (c *Client) GetAgent(ctx context.Context, target string) (*Agent, error) {
	var result struct {
		Agent Agent `json:"agent"`
	}
	if err := c.call(ctx, &result, "agent", "get", target); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &result.Agent, nil
}

// StartAgent starts a harness in an existing, available shell pane.
//
// The pane must be at its interactive prompt with no foreground command;
// `agent start` never creates or moves layout. The call returns only once Herdr
// has detected the harness and considers it ready for input.
func (c *Client) StartAgent(ctx context.Context, name, kind, paneID string, timeout time.Duration, agentArgs []string) error {
	args := []string{"agent", "start", name, "--kind", kind, "--pane", paneID}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	if len(agentArgs) > 0 {
		args = append(args, "--")
		args = append(args, agentArgs...)
	}
	_, err := c.rawNoTimeout(ctx, args...)
	return err
}

// PromptAgent submits a prompt. wait blocks until the agent settles into idle,
// done, or blocked.
//
// A successful submission is not proof the agent started a turn, and a timeout
// is not proof the prompt was never delivered — never retry one blindly.
func (c *Client) PromptAgent(ctx context.Context, target, text string, wait bool, timeout time.Duration) error {
	args := []string{"agent", "prompt", target, text}
	if wait {
		args = append(args, "--wait")
	}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	_, err := c.rawNoTimeout(ctx, args...)
	return err
}

// WaitAgent blocks until the agent reaches one of the given states, or any
// settled state when none are given.
func (c *Client) WaitAgent(ctx context.Context, target string, until []string, timeout time.Duration) error {
	args := []string{"agent", "wait", target}
	for _, state := range until {
		args = append(args, "--until", state)
	}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	_, err := c.rawNoTimeout(ctx, args...)
	return err
}

// FocusAgent brings the agent's pane into view.
func (c *Client) FocusAgent(ctx context.Context, target string) error {
	return c.call(ctx, nil, "agent", "focus", target)
}

// SendKeys sends logical key presses to an agent, for interactive UI controls.
func (c *Client) SendKeys(ctx context.Context, target string, keys ...string) error {
	return c.call(ctx, nil, append([]string{"agent", "send-keys", target}, keys...)...)
}

// Notify shows a Herdr notification. Failures are cosmetic and callers may
// ignore them.
func (c *Client) Notify(ctx context.Context, message string) error {
	return c.call(ctx, nil, "notification", "show", message)
}

// OpenPluginPane opens a pane owned by a plugin entrypoint.
func (c *Client) OpenPluginPane(ctx context.Context, plugin, entrypoint, placement string, focus bool) (*Pane, error) {
	args := []string{"plugin", "pane", "open", "--plugin", plugin, "--entrypoint", entrypoint}
	if placement != "" {
		args = append(args, "--placement", placement)
	}
	args = append(args, focusFlag(focus))
	var result struct {
		Pane Pane `json:"pane"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, err
	}
	return &result.Pane, nil
}

func envArgs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	// Deterministic argv, so the recorded-argv tests can assert an exact line.
	sort.Strings(keys)
	out := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		out = append(out, "--env", key+"="+env[key])
	}
	return out
}

func focusFlag(focus bool) string {
	if focus {
		return "--focus"
	}
	return "--no-focus"
}

func sameDir(left, right string) bool {
	return normalizeDir(left) == normalizeDir(right)
}

func normalizeDir(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return strings.TrimRight(filepath.Clean(path), "/")
}

// IsPaneBusy reports whether a failed `agent start` was refused because the
// target pane is not an available shell.
//
// A pane that Herdr has just created is not at its interactive prompt yet: the
// shell has to exec and print a prompt first, and `agent start` refuses the
// pane until it does. A caller that created the pane itself should retry for a
// short while; a caller aiming at a pane the user is typing in should not.
func IsPaneBusy(err error) bool {
	var herdrErr *Error
	if !asError(err, &herdrErr) {
		return false
	}
	message := strings.ToLower(herdrErr.Message)
	return strings.Contains(message, "agent_pane_busy") ||
		strings.Contains(message, "not an available shell")
}

// IsAgentNotReady reports whether `agent start` returned agent_not_ready.
//
// This is not a failure. Herdr's contract is explicit: the agent started and is
// blocked on its own startup UI — a trust prompt, an update notice — and the
// name stays valid for reading and for keys. A fresh worktree is a directory
// the harness has never seen, so this is the ordinary case there, not the
// exception.
func IsAgentNotReady(err error) bool {
	var herdrErr *Error
	if !asError(err, &herdrErr) {
		return false
	}
	message := strings.ToLower(herdrErr.Message)
	return strings.Contains(message, "agent_not_ready") ||
		strings.Contains(message, "not ready for prompts")
}

// IsAgentBlocked reports whether a prompt was refused because the agent is
// sitting at an approval or question dialog.
func IsAgentBlocked(err error) bool {
	var herdrErr *Error
	if !asError(err, &herdrErr) {
		return false
	}
	return strings.Contains(strings.ToLower(herdrErr.Message), "agent_blocked")
}

func isNotFound(err error) bool {
	var herdrErr *Error
	if !asError(err, &herdrErr) {
		return false
	}
	message := strings.ToLower(herdrErr.Message)
	return strings.Contains(message, "not_found") || strings.Contains(message, "not found")
}

func asError(err error, target **Error) bool { return errors.As(err, target) }
