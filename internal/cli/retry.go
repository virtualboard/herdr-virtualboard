package cli

import "github.com/spf13/cobra"

func newRunRetryCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "retry [RUN-ID]",
		Short: "Resume unfinished completion effects without changing the reported outcome",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			run, err := resolveRun(app, args)
			if err != nil {
				return err
			}
			completion, err := app.Dispatcher().Retry(contextFor(cmd), run.ID)
			if err != nil {
				return err
			}
			if !app.Emit(completion) {
				if completion.Pending {
					app.Warn("Completion still pending: %s", completion.TransitionError)
				} else {
					app.Print("Completion effects settled for %s", run.FeatureID)
				}
			}
			if completion.Pending {
				return errSilent{}
			}
			if completion.AutomationError != "" {
				app.Warn("Automatic handoff failed: %s", completion.AutomationError)
				return errSilent{}
			}
			return nil
		},
	}
}

func newRunRecoverCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "recover",
		Short: "Quarantine a corrupt local run store and initialize an empty one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			backup, err := app.Store().Recover(contextFor(cmd))
			if err != nil {
				return err
			}
			if !app.Emit(map[string]any{"recovered": true, "backup": backup}) {
				app.Print("Run store recovered; original retained at %s", backup)
			}
			return nil
		},
	}
}
