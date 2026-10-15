package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
)

func newRoleCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "role",
		Short: "Inspect the VirtualBoard agent charters this workspace ships",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List the roles a dispatch can adopt",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := app.Resolve(); err != nil {
					return err
				}
				if app.Emit(map[string]any{"roles": app.Roles(), "default": app.Config().Role}) {
					return nil
				}
				if len(app.Roles()) == 0 {
					app.Print("No agent charters in %s.", app.Workspace().AgentsDir())
					return nil
				}
				for _, role := range app.Roles() {
					marker := " "
					if role.Key == app.Config().Role {
						marker = "*"
					}
					app.Print("%s %-30s %s", marker, role.Key, role.Description)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "show <ROLE>",
			Short: "Print a role's charter",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := app.Resolve(); err != nil {
					return err
				}
				role, found := roles.Find(app.Roles(), args[0])
				if !found {
					return Usage("unknown role %q (see `hvb role list`)", args[0])
				}
				charter, err := role.Charter()
				if err != nil {
					return err
				}
				if app.Emit(map[string]any{"role": role, "charter": charter}) {
					return nil
				}
				app.Print("%s", charter)
				return nil
			},
		},
	)
	return cmd
}

func newHarnessCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "harness",
		Short: "Inspect the agent harnesses Herdr can start",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the harness kinds `hvb run start --harness` accepts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defaultKind := "claude"
			if err := app.Resolve(); err == nil {
				defaultKind = app.Config().Harness
			}
			if app.Emit(map[string]any{"kinds": herdrcli.Kinds, "default": defaultKind}) {
				return nil
			}
			for _, kind := range herdrcli.Kinds {
				marker := " "
				if kind == defaultKind {
					marker = "*"
				}
				app.Print("%s %s", marker, kind)
			}
			app.Print("")
			app.Print("Precise working/blocked/done signals need the matching Herdr integration:")
			app.Print("  herdr integration install %s", defaultKind)
			return nil
		},
	})
	return cmd
}

// check is one line of the doctor report.
type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Fatal marks a check whose failure stops hvb working at all, as
	// opposed to one that only degrades it.
	Fatal  bool   `json:"fatal"`
	Remedy string `json:"remedy,omitempty"`
}

func newDoctorCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that hvb, vb, Herdr, and the workspace agree",
		Long: `Check the environment hvb needs.

Run this first when something does not work. It reports what is wrong and what
still works anyway: a missing harness integration degrades lifecycle signals but
does not stop dispatch, whereas an unsupported Herdr does.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := contextFor(cmd)
			var checks []check

			workspaceErr := app.Resolve()
			if workspaceErr != nil {
				checks = append(checks, check{Name: "workspace", Detail: workspaceErr.Error(), Fatal: true})
			} else {
				checks = append(checks, check{Name: "workspace", OK: true,
					Detail: app.Workspace().Root + " (" + app.Workspace().ID() + ")"})
				_, storeErr := app.Store().List()
				storeCheck := check{Name: "run store", OK: storeErr == nil, Detail: app.Store().Path(), Fatal: true}
				if storeErr != nil {
					storeCheck.Detail = storeErr.Error()
					storeCheck.Remedy = "Preserve this file. Use a compatible hvb for newer formats; for corrupt data, inspect it before explicitly running hvb run recover."
				}
				checks = append(checks, storeCheck)
				checks = append(checks, roleCheck(app))
			}

			binaryCheck := vbCheck(app, ctx, workspaceErr == nil)
			checks = append(checks, binaryCheck)
			if workspaceErr == nil && binaryCheck.OK {
				report, err := app.VB().Doctor(ctx, false)
				if err != nil {
					checks = append(checks, check{Name: "vb diagnostics", Fatal: true, Detail: err.Error(), Remedy: "Install a compatible vb CLI, then run vb doctor directly to inspect the workspace."})
				} else {
					if err := report.RequireDispatchCapabilities(); err != nil {
						checks = append(checks, check{Name: "vb capabilities", Fatal: true, Detail: err.Error(), Remedy: "Install a vb CLI supporting ownership tokens, lease renewal, raw validation, and validation exit statuses."})
					}
					for _, entry := range report.Checks {
						checks = append(checks, check{Name: "vb " + entry.Name, OK: entry.OK, Detail: entry.Detail, Fatal: entry.Fatal, Remedy: entry.Remedy})
					}
				}
			}
			herdrHealth := herdrCheck(app, ctx)
			checks = append(checks, herdrHealth)
			if workspaceErr == nil {
				checks = append(checks, resourceChecks(ctx, app, herdrHealth.OK, binaryCheck.OK)...)
			}
			checks = append(checks, check{
				Name:   "herdr pane",
				OK:     herdrcli.InHerdr(),
				Detail: herdrEnvDetail(),
			})

			healthy := true
			for _, entry := range checks {
				if !entry.OK && entry.Fatal {
					healthy = false
				}
			}
			if app.Emit(map[string]any{"checks": checks, "ok": healthy}) {
				if !healthy {
					return errSilent{}
				}
				return nil
			}
			for _, entry := range checks {
				mark := "✓"
				if !entry.OK {
					mark = "✗"
					if !entry.Fatal {
						mark = "!"
					}
				}
				app.Print("%s %-14s %s", mark, entry.Name, entry.Detail)
				if !entry.OK && entry.Remedy != "" {
					app.Print("  Remedy: %s", entry.Remedy)
				}
			}
			if !healthy {
				return errSilent{}
			}
			return nil
		},
	}
}

func roleCheck(app *App) check {
	names := make([]string, 0, len(app.Roles()))
	for _, role := range app.Roles() {
		names = append(names, role.Key)
	}
	if len(names) == 0 {
		return check{Name: "roles", Detail: "no charters in " + app.Workspace().AgentsDir() +
			" — dispatch will run without a role prompt"}
	}
	return check{Name: "roles", OK: true, Detail: strings.Join(names, ", ")}
}

func vbCheck(app *App, ctx context.Context, resolved bool) check {
	client := app.VB()
	if !resolved || client == nil {
		// Without a workspace there is no --root to pass, but `vb version`
		// still answers, and knowing vb is installed is the useful half of
		// the check.
		client = vb.New("")
	}
	if err := client.Available(); err != nil {
		return check{Name: "vb", Detail: err.Error(), Fatal: true}
	}
	version, err := client.Version(ctx)
	if err != nil {
		return check{Name: "vb", Detail: "found, but `vb version` failed: " + err.Error(), Fatal: true}
	}
	return check{Name: "vb", OK: true, Detail: strings.TrimSpace(version)}
}

func herdrCheck(app *App, ctx context.Context) check {
	client := app.Herdr()
	if client == nil {
		client = herdrcli.New()
	}
	if err := client.Available(); err != nil {
		return check{Name: "herdr", Detail: err.Error(), Fatal: true}
	}
	status, err := client.Status(ctx)
	if err != nil {
		return check{Name: "herdr", Detail: "found, but `herdr status` failed: " + err.Error(), Fatal: true}
	}
	if err := client.Gate(ctx); err != nil {
		return check{Name: "herdr", Detail: err.Error(), Fatal: true}
	}
	return check{Name: "herdr", OK: true,
		Detail: status.ServerVersion + " / protocol " + itoa(status.ClientProtocol) + " (" + status.Socket + ")"}
}

func herdrEnvDetail() string {
	if herdrcli.InHerdr() {
		return "running inside a Herdr-managed pane"
	}
	return "HERDR_ENV is not 1 — the TUI and dispatch work best from inside Herdr"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
