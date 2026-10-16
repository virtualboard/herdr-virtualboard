package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/tui"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

func newTUICommand(app *App) *cobra.Command {
	var (
		refresh time.Duration
		colour  bool
	)
	cmd := &cobra.Command{
		Use:     "tui",
		Aliases: []string{"board"},
		Short:   "Open the kanban board",
		Long: `Open the kanban board over this VirtualBoard workspace.

Columns are the VirtualBoard lifecycle: backlog, in-progress, blocked, review,
done. Cards are feature specs. Moving a card runs vb, so the repository and the
board can never disagree; pressing d dispatches a role agent into a Herdr pane.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := contextFor(cmd)
			// A plugin pane that exits immediately is invisible: the user
			// presses a key, something flashes, and nothing explains itself.
			// Every startup failure becomes a screen the user can read.
			if err := app.Resolve(); err != nil {
				if errors.Is(err, workspace.ErrNotFound) {
					cwd, _ := os.Getwd()
					return app.showNotice(ctx, tui.NoWorkspaceNotice(cwd, err), colour, err)
				}
				return app.showNotice(ctx, tui.Notice{
					Title: "The board could not start",
					Body:  []string{err.Error()},
				}, colour, err)
			}
			if err := app.VB().Available(); err != nil {
				return app.showNotice(ctx, tui.Notice{
					Title: "The vb CLI is missing",
					Body: []string{
						"hvb makes every change through vb, so the board cannot open without it.",
						"",
						err.Error(),
						"",
						"Install it with .virtualboard/scripts/install-vb-cli.sh, or see https://github.com/virtualboard/vb-cli.",
					},
				}, colour, err)
			}

			backend := tui.NewBackend(
				app.Workspace(), app.Config(), app.VB(), app.Herdr(),
				app.Store(), app.Dispatcher(), app.Roles(),
			)

			// SIGTERM must reach the loop as a cancellation rather than kill
			// the process: the terminal has to be restored first.
			ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
			defer stop()

			return tui.Run(ctx, backend, tui.Options{
				Refresh:   refresh,
				Colour:    colour,
				PaneTitle: app.paneTitle(),
			})
		},
	}
	cmd.Flags().DurationVar(&refresh, "refresh", tui.DefaultRefresh, "how often to reload the board")
	cmd.Flags().BoolVar(&colour, "color", true, "use colour (NO_COLOR also disables it)")
	return cmd
}

// showNotice renders a startup failure as a screen the user can read, and falls
// back to the ordinary error path when there is no terminal — a script running
// `hvb tui` in a pipe wants the error, not a drawing of it.
//
// The original error is still returned, so the exit status is unchanged and a
// caller branching on `$?` sees the same thing either way.
func (a *App) showNotice(ctx context.Context, notice tui.Notice, colour bool, cause error) error {
	if err := tui.RunNotice(ctx, notice, colour); err != nil {
		return cause
	}
	return cause
}

// paneTitle returns a callback that relabels the plugin's own Herdr pane, or
// nil when hvb is not running inside one.
func (a *App) paneTitle() func(context.Context, string) {
	paneID := os.Getenv("HERDR_PANE_ID")
	if paneID == "" {
		return nil
	}
	return func(ctx context.Context, title string) {
		// Cosmetic: a failed rename leaves the default border and is not
		// worth interrupting the board for.
		_ = a.Herdr().RenamePane(ctx, paneID, title)
	}
}
