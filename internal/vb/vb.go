// Package vb wraps the VirtualBoard `vb` CLI.
//
// Every mutation of a feature spec goes through `vb`, never through direct file
// writes: vb owns the id allocation, the status-transition rules, the lock
// file, the hash-chained audit log, and `features/INDEX.md`. hvb reads specs
// straight off disk (they are plain markdown) but never writes one itself.
package vb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Binary is the executable name looked up on PATH when Client.Bin is empty.
// HVB_VB_BIN overrides it, which is what the e2e suite uses to substitute a
// recording stub.
const Binary = "vb"

// Exit codes, copied from cmd/exit.go in vb-cli. hvb maps them to typed errors
// so the TUI can tell "you cannot move a done feature" from "vb is missing".
const (
	ExitSuccess           = 0
	ExitValidation        = 1
	ExitNotFound          = 2
	ExitInvalidTransition = 3
	ExitDependency        = 4
	ExitLockConflict      = 5
	ExitFilesystem        = 6
	ExitSchema            = 7
	ExitExternalCommand   = 8
	ExitUnknown           = 10
)

// Error is a failed `vb` invocation. Code is vb's own exit status, so callers
// branch on the documented meaning instead of matching message text.
type Error struct {
	Code    int
	Args    []string
	Message string
	Data    json.RawMessage
}

func (e *Error) Error() string {
	args := append([]string(nil), e.Args...)
	message := e.Message
	for i, arg := range e.Args {
		token := ""
		if arg == "--token" && i+1 < len(args) {
			token = e.Args[i+1]
			args[i+1] = "[redacted]"
		}
		if value, ok := strings.CutPrefix(arg, "--token="); ok {
			token = value
			args[i] = "--token=[redacted]"
		}
		if token != "" {
			message = strings.ReplaceAll(message, token, "[redacted]")
		}
	}
	return fmt.Sprintf("vb %s: %s (exit %d)", strings.Join(args, " "), message, e.Code)
}

// Is lets errors.Is match the sentinel errors below by exit code.
func (e *Error) Is(target error) bool {
	var other *Error
	if errors.As(target, &other) {
		return other.Code == e.Code
	}
	return false
}

// Sentinels for errors.Is comparisons against the exit codes worth branching on.
var (
	ErrNotFound          = &Error{Code: ExitNotFound, Message: "not found"}
	ErrInvalidTransition = &Error{Code: ExitInvalidTransition, Message: "invalid status transition"}
	ErrLockConflict      = &Error{Code: ExitLockConflict, Message: "lock conflict"}
)

// Envelope is the `--json` response shape shared by every vb subcommand.
type Envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Client runs `vb` against one project root.
type Client struct {
	// Bin is the vb executable. Empty means Binary, resolved on PATH.
	Bin string
	// Root is passed as `--root`; vb resolves `.virtualboard` beneath it.
	Root string
	// Timeout bounds a single invocation. Zero means DefaultTimeout.
	Timeout time.Duration
	// DryRun adds `--dry-run`, which makes vb simulate without writing.
	DryRun     bool
	Actor      string
	LeaseToken string
}

// DefaultTimeout bounds one vb call. vb is a local file-manipulating CLI; a
// call that takes longer than this is wedged, not slow.
const DefaultTimeout = 30 * time.Second

// New builds a client for a project root, honouring the HVB_VB_BIN override.
func New(root string) *Client {
	return &Client{Bin: os.Getenv("HVB_VB_BIN"), Root: root}
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return Binary
}

// Available reports whether the vb executable can be found.
func (c *Client) Available() error {
	if _, err := exec.LookPath(c.bin()); err != nil {
		return fmt.Errorf("vb CLI not found on PATH: %w (install it with .virtualboard/scripts/install-vb-cli.sh)", err)
	}
	return nil
}

// Version returns the `vb version` string.
func (c *Client) Version(ctx context.Context) (string, error) {
	stdout, err := c.raw(ctx, "version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(stdout)), nil
}

// run invokes vb with `--json` and decodes the envelope.
func (c *Client) run(ctx context.Context, args ...string) (*Envelope, error) {
	full := append([]string{"--json"}, args...)
	stdout, err := c.raw(ctx, full...)
	if err != nil {
		return nil, err
	}
	var envelope Envelope
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return nil, fmt.Errorf("vb %s: decode JSON response: %w", strings.Join(args, " "), err)
	}
	if !envelope.Success {
		code := ExitUnknown
		if len(args) > 0 && args[0] == "validate" {
			code = ExitValidation
		}
		return nil, &Error{Code: code, Args: args, Message: envelope.Message, Data: envelope.Data}
	}
	return &envelope, nil
}

// raw invokes vb and returns stdout, mapping a non-zero exit to *Error.
//
// vb writes its failure message to stderr as plain text even under `--json`,
// so the message comes from stderr and the classification from the exit code.
func (c *Client) raw(ctx context.Context, args ...string) ([]byte, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := make([]string, 0, len(args)+4)
	if c.Root != "" {
		full = append(full, "--root", c.Root)
	}
	if c.DryRun {
		full = append(full, "--dry-run")
	}
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, c.bin(), full...)
	if c.Actor != "" {
		cmd.Env = append(os.Environ(), "VB_OWNER="+c.Actor)
	}
	if c.LeaseToken != "" {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, "VB_LOCK_TOKEN="+c.LeaseToken)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// vb prompts on some destructive paths; a board action must never block
	// on a terminal the TUI has taken over.
	cmd.Stdin = nil

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		return nil, &Error{Code: ExitUnknown, Args: full, Message: fmt.Sprintf("timed out after %s", timeout)}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		var envelope Envelope
		if json.Unmarshal(stdout.Bytes(), &envelope) == nil && !envelope.Success && envelope.Data != nil {
			return nil, &Error{Code: exitErr.ExitCode(), Args: full, Message: envelope.Message, Data: envelope.Data}
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = exitErr.String()
		}
		return nil, &Error{Code: exitErr.ExitCode(), Args: full, Message: message}
	}
	return nil, fmt.Errorf("run vb %s: %w", strings.Join(full, " "), err)
}
