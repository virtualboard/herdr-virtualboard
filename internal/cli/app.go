// Package cli assembles the `hvb` command tree.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/dispatch"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// Exit codes. The first five mirror vb's own, so a script driving both CLIs
// branches on one set of numbers. 64 is EX_USAGE, for a failure hvb raised
// itself before touching vb or Herdr.
const (
	ExitOK               = 0
	ExitValidation       = 1
	ExitNotFound         = 2
	ExitBadTransition    = 3
	ExitDependency       = 4
	ExitLockConflict     = 5
	ExitHerdrUnavailable = 6
	ExitUsage            = 64
)

// UsageError is a failure hvb raised itself: a bad flag combination, an unknown
// enum value, a missing run id. It always exits ExitUsage.
type UsageError struct{ error }

// Usage wraps a message as a usage error.
func Usage(format string, args ...any) error {
	return UsageError{fmt.Errorf(format, args...)}
}

// App is the resolved runtime for one hvb invocation. Commands that need a
// workspace call Resolve; commands that do not (version, skill) never touch it,
// so `hvb skill` works outside a VirtualBoard project.
type App struct {
	// RootFlag is --root, the project root override.
	RootFlag string
	// JSONFlag is --json.
	JSONFlag bool
	// Session is --session, a named Herdr session.
	Session string

	Out io.Writer
	Err io.Writer

	workspace *workspace.Workspace
	config    *config.Config
	vb        *vb.Client
	herdr     *herdrcli.Client
	store     *runs.Store
	roles     []roles.Role
}

// Resolve loads everything a board command needs. It is idempotent.
func (a *App) Resolve() error {
	if a.workspace != nil {
		return nil
	}
	root := a.RootFlag
	if root == "" {
		root = os.Getenv("HVB_PROJECT_ROOT")
	}
	var (
		resolved *workspace.Workspace
		err      error
	)
	if root != "" {
		resolved, err = workspace.Open(root)
	} else {
		resolved, err = workspace.Discover("")
	}
	if err != nil {
		return err
	}
	cfg, err := config.Load(resolved.Root)
	if err != nil {
		return err
	}
	store, err := runs.Open(resolved.ID())
	if err != nil {
		return err
	}
	loadedRoles, err := roles.Load(resolved.AgentsDir())
	if err != nil {
		return err
	}
	herdr := herdrcli.New()
	herdr.Session = a.Session

	a.workspace, a.config, a.store, a.roles, a.herdr = resolved, cfg, store, loadedRoles, herdr
	a.vb = vb.New(resolved.Root)
	a.vb.Actor = cfg.ResolveOwner()
	if runID := os.Getenv("HVB_RUN_ID"); runID != "" {
		if run, err := store.Get(runID); err == nil {
			a.vb.LeaseToken = run.LockToken
			if run.LockOwner != "" {
				a.vb.Actor = run.LockOwner
			}
		}
	}
	return nil
}

// Workspace returns the resolved workspace.
func (a *App) Workspace() *workspace.Workspace { return a.workspace }

// Config returns the resolved configuration.
func (a *App) Config() *config.Config { return a.config }

// VB returns the vb client.
func (a *App) VB() *vb.Client { return a.vb }

// Herdr returns the herdr client.
func (a *App) Herdr() *herdrcli.Client { return a.herdr }

// Store returns the run store.
func (a *App) Store() *runs.Store { return a.store }

// Roles returns the workspace's agent charters.
func (a *App) Roles() []roles.Role { return a.roles }

// Dispatcher builds a dispatcher over the resolved runtime.
func (a *App) Dispatcher() *dispatch.Dispatcher {
	return &dispatch.Dispatcher{
		Workspace: a.workspace,
		Config:    a.config,
		Herdr:     a.herdr,
		VB:        a.vb,
		Store:     a.store,
		Roles:     a.roles,
		SelfRunID: os.Getenv("HVB_RUN_ID"),
	}
}

// Print writes a human line to stdout.
func (a *App) Print(format string, args ...any) {
	fmt.Fprintf(a.out(), format+"\n", args...)
}

// Warn writes a diagnostic to stderr. It never becomes part of --json stdout.
func (a *App) Warn(format string, args ...any) {
	fmt.Fprintf(a.errOut(), format+"\n", args...)
}

// Emit writes value as indented JSON when --json is set and returns whether it
// did, so a command reads as:
//
//	if app.Emit(result) { return nil }
//	… human rendering …
func (a *App) Emit(value any) bool {
	if !a.JSONFlag {
		return false
	}
	encoder := json.NewEncoder(a.out())
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(a.errOut(), "encode JSON: %v\n", err)
	}
	return true
}

func (a *App) out() io.Writer {
	if a.Out != nil {
		return a.Out
	}
	return os.Stdout
}

func (a *App) errOut() io.Writer {
	if a.Err != nil {
		return a.Err
	}
	return os.Stderr
}

// ExitCode classifies an error into the process exit status.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var usage UsageError
	if errors.As(err, &usage) {
		return ExitUsage
	}
	var vbErr *vb.Error
	if errors.As(err, &vbErr) {
		switch vbErr.Code {
		case vb.ExitNotFound:
			return ExitNotFound
		case vb.ExitInvalidTransition:
			return ExitBadTransition
		case vb.ExitDependency:
			return ExitDependency
		case vb.ExitLockConflict:
			return ExitLockConflict
		case vb.ExitValidation, vb.ExitSchema:
			return ExitValidation
		}
		return ExitValidation
	}
	switch {
	case errors.Is(err, runs.ErrNotFound), errors.Is(err, workspace.ErrNotFound):
		return ExitNotFound
	case errors.Is(err, dispatch.ErrUnknownHarness):
		return ExitUsage
	case errors.Is(err, dispatch.ErrLocked):
		return ExitLockConflict
	case errors.Is(err, herdrcli.ErrIncompatible):
		return ExitHerdrUnavailable
	}
	var herdrErr *herdrcli.Error
	if errors.As(err, &herdrErr) {
		return ExitHerdrUnavailable
	}
	if strings.Contains(err.Error(), "not found") {
		return ExitNotFound
	}
	return ExitValidation
}

// silent reports whether the error has already rendered everything the user
// needs, so ReportError should stay quiet and only the exit status speaks.
func silent(err error) bool {
	var quiet errSilent
	return errors.As(err, &quiet)
}

// ReportError writes a failure in the shape the invocation asked for: the
// stable JSON envelope on stderr under --json, a plain line otherwise. Under
// --json, stdout is left empty so a caller can parse it unconditionally.
func (a *App) ReportError(err error) {
	if err == nil || silent(err) {
		return
	}
	code := ExitCode(err)
	if a.JSONFlag {
		envelope := map[string]any{"error": map[string]any{
			"code":    code,
			"kind":    errorKind(err),
			"message": err.Error(),
		}}
		encoder := json.NewEncoder(a.errOut())
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(envelope)
		return
	}
	fmt.Fprintf(a.errOut(), "hvb: %v\n", err)
}

func errorKind(err error) string {
	var usage UsageError
	switch {
	case errors.As(err, &usage), errors.Is(err, dispatch.ErrUnknownHarness):
		return "cli"
	case errors.Is(err, herdrcli.ErrIncompatible):
		return "herdr"
	}
	var vbErr *vb.Error
	if errors.As(err, &vbErr) {
		return "vb"
	}
	var herdrErr *herdrcli.Error
	if errors.As(err, &herdrErr) {
		return "herdr"
	}
	return "hvb"
}

// contextFor returns the command's context, defaulting to Background.
func contextFor(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
