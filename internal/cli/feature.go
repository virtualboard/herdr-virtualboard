package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
)

func newFeatureCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "feature",
		Aliases: []string{"ftr", "f"},
		Short:   "Read and change VirtualBoard feature specs",
	}
	cmd.AddCommand(
		newFeatureListCommand(app),
		newFeatureShowCommand(app),
		newFeatureNewCommand(app),
		newFeatureMoveCommand(app),
		newFeatureSetCommand(app),
		newFeatureNoteCommand(app),
		newFeatureDeleteCommand(app),
		newFeatureValidateCommand(app),
	)
	return cmd
}

// featureRow is the list projection, kept separate from feature.Spec so the
// JSON surface of `hvb feature list` does not change whenever the spec struct
// gains a field.
type featureRow struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Owner      string   `json:"owner,omitempty"`
	Priority   string   `json:"priority,omitempty"`
	Complexity string   `json:"complexity,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Updated    string   `json:"updated,omitempty"`
	Path       string   `json:"path"`
	Runs       int      `json:"runs"`
	ActiveRun  string   `json:"active_run,omitempty"`
}

func newFeatureListCommand(app *App) *cobra.Command {
	var (
		statusFilter []string
		labelFilter  []string
		ownerFilter  string
		mine         bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List features, grouped by lifecycle status",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			wanted, err := parseStatuses(statusFilter)
			if err != nil {
				return err
			}
			if mine {
				if ownerFilter != "" {
					return Usage("--mine and --owner are mutually exclusive")
				}
				ownerFilter = app.Config().ResolveOwner()
			}

			specs, problems := app.Workspace().LoadSpecs()
			for _, problem := range problems {
				app.Warn("hvb: %v", problem)
			}
			activeByFeature := map[string]string{}
			runCounts := map[string]int{}
			if all, err := app.Store().List(); err == nil {
				for _, run := range all {
					runCounts[run.FeatureID]++
					if run.Active() && activeByFeature[run.FeatureID] == "" {
						activeByFeature[run.FeatureID] = run.ID
					}
				}
			}

			var rows []featureRow
			for _, spec := range specs {
				if len(wanted) > 0 && !wanted[spec.Status] {
					continue
				}
				if ownerFilter != "" && !strings.EqualFold(spec.Owner, ownerFilter) {
					continue
				}
				if !hasAllLabels(spec, labelFilter) {
					continue
				}
				rows = append(rows, featureRow{
					ID: spec.ID, Title: spec.Title, Status: string(spec.Status),
					Owner: spec.Owner, Priority: spec.Priority, Complexity: spec.Complexity,
					Labels: spec.Labels, Updated: spec.Updated,
					Path:      app.Workspace().Rel(spec.Path),
					Runs:      runCounts[spec.ID],
					ActiveRun: activeByFeature[spec.ID],
				})
			}
			if app.Emit(map[string]any{"features": rows, "total": len(rows)}) {
				return nil
			}
			if len(rows) == 0 {
				app.Print("No features match.")
				return nil
			}
			renderFeatureTable(app, rows)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&statusFilter, "status", nil, "only these statuses (repeatable)")
	cmd.Flags().StringSliceVar(&labelFilter, "label", nil, "only features carrying every given label")
	cmd.Flags().StringVar(&ownerFilter, "owner", "", "only features owned by this handle")
	cmd.Flags().BoolVar(&mine, "mine", false, "only features owned by the configured owner")
	return cmd
}

func newFeatureShowCommand(app *App) *cobra.Command {
	var bodyOnly bool
	cmd := &cobra.Command{
		Use:   "show [ID]",
		Short: "Show one feature spec",
		Long: `Show one feature spec.

With no id, shows $HVB_FEATURE_ID — the feature a dispatched agent owns — so an
agent can read its own assignment without being told which one it is.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			id, err := resolveFeatureID(args)
			if err != nil {
				return err
			}
			spec, err := app.Workspace().FindSpec(id)
			if err != nil {
				return err
			}
			if bodyOnly {
				app.Print("%s", strings.TrimSpace(spec.Body))
				return nil
			}
			featureRuns, _ := app.Store().ForFeature(spec.ID)
			if app.Emit(map[string]any{
				"feature":             spec,
				"body":                spec.Body,
				"acceptance_criteria": spec.AcceptanceCriteria(),
				"sections":            spec.Sections(),
				"runs":                featureRuns,
				"path":                app.Workspace().Rel(spec.Path),
			}) {
				return nil
			}
			renderFeatureDetail(app, spec, featureRuns)
			return nil
		},
	}
	cmd.Flags().BoolVar(&bodyOnly, "body", false, "print only the markdown body")
	return cmd
}

func newFeatureNewCommand(app *App) *cobra.Command {
	var (
		labels     []string
		priority   string
		complexity string
		summary    string
	)
	cmd := &cobra.Command{
		Use:   "new <title>",
		Short: "Create a feature in backlog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			ctx := contextFor(cmd)
			created, err := app.VB().New(ctx, args[0], labels...)
			if err != nil {
				return err
			}
			fields := map[string]string{}
			if priority != "" {
				fields["priority"] = priority
			}
			if complexity != "" {
				fields["complexity"] = complexity
			}
			sections := map[string]string{}
			if summary != "" {
				sections["Summary"] = summary
			}
			if len(fields) > 0 || len(sections) > 0 {
				if err := app.VB().Update(ctx, created.ID, fields, sections); err != nil {
					return err
				}
			}
			if err := app.VB().Index(ctx); err != nil {
				app.Warn("hvb: index regeneration failed: %v", err)
			}
			if app.Emit(created) {
				return nil
			}
			app.Print("Created %s — %s", created.ID, created.Title)
			app.Print("  %s", created.Path)
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&labels, "label", "l", nil, "kebab-case label (repeatable)")
	cmd.Flags().StringVar(&priority, "priority", "", "P0, P1, P2, or P3")
	cmd.Flags().StringVar(&complexity, "complexity", "", "XS, S, M, L, or XL")
	cmd.Flags().StringVarP(&summary, "summary", "d", "", "fill the Summary section")
	return cmd
}

func newFeatureMoveCommand(app *App) *cobra.Command {
	var owner string
	var release bool
	cmd := &cobra.Command{
		Use:   "move <ID> <status>",
		Short: "Move a feature to another lifecycle status",
		Long: `Move a feature to another lifecycle status.

VirtualBoard allows: backlog → in-progress → review → done, with in-progress ↔
blocked and review → in-progress. Nothing leaves done. vb enforces this; hvb
refuses obviously illegal moves first so the error names the lifecycle rather
than an exit code.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			target, ok := feature.ParseStatus(args[1])
			if !ok {
				return Usage("unknown status %q (expected backlog, in-progress, blocked, review, or done)", args[1])
			}
			if release && owner != "" {
				return Usage("--release and --owner are mutually exclusive")
			}
			if release {
				owner = vb.ClearOwner
			}
			ctx := contextFor(cmd)
			spec, err := app.Workspace().FindSpec(args[0])
			if err != nil {
				return err
			}
			if spec.Status == target {
				return Usage("%s is already in %s", spec.ID, target)
			}
			if !feature.CanTransition(spec.Status, target) {
				return Usage("VirtualBoard does not allow %s → %s (from %s you may move to: %s)",
					spec.Status, target, spec.Status, joinStatuses(spec.Status.NextStatuses()))
			}
			moved, err := app.VB().Move(ctx, spec.ID, target, owner)
			if err != nil {
				return err
			}
			if err := app.VB().Index(ctx); err != nil {
				app.Warn("hvb: index regeneration failed: %v", err)
			}
			if _, err := app.Dispatcher().AutoStart(ctx, spec.ID); err != nil {
				return fmt.Errorf("moved %s to %s, but automatic dispatch failed: %w", spec.ID, target, err)
			}
			if app.Emit(moved) {
				return nil
			}
			app.Print("%s → %s", moved.ID, moved.Status)
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "set the owner while moving")
	cmd.Flags().BoolVar(&release, "release", false, "clear the owner while moving")
	return cmd
}

func newFeatureSetCommand(app *App) *cobra.Command {
	var fields []string
	cmd := &cobra.Command{
		Use:   "set <ID> key=value [key=value ...]",
		Short: "Set frontmatter fields",
		Long: `Set frontmatter fields through vb.

Setting status here is refused: a status change is a move, and moving is what
keeps the spec file, its directory, and the index agreeing with each other.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			assignments := map[string]string{}
			for _, raw := range append(args[1:], fields...) {
				key, value, ok := strings.Cut(raw, "=")
				if !ok {
					return Usage("expected key=value, got %q", raw)
				}
				key = strings.TrimSpace(key)
				if key == "status" {
					return Usage("use `hvb feature move` to change status")
				}
				assignments[key] = value
			}
			ctx := contextFor(cmd)
			if err := app.VB().Update(ctx, args[0], assignments, nil); err != nil {
				return err
			}
			if err := app.VB().Index(ctx); err != nil {
				app.Warn("hvb: index regeneration failed: %v", err)
			}
			if app.Emit(map[string]any{"id": args[0], "fields": assignments}) {
				return nil
			}
			app.Print("Updated %s (%s)", args[0], strings.Join(sortedPairs(assignments), ", "))
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&fields, "field", nil, "additional key=value assignment (repeatable)")
	return cmd
}

func newFeatureNoteCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note <section> <text> [ID]",
		Short: "Write text into a section of a feature spec",
		Long: `Write text into a named section of a feature spec.

This is how a dispatched agent records something that should outlive its run —
a decision, a surprise, a deviation from the plan. Without an id it writes to
$HVB_FEATURE_ID.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			id, err := resolveFeatureID(args[2:])
			if err != nil {
				return err
			}
			ctx := contextFor(cmd)
			if err := app.VB().Update(ctx, id, nil, map[string]string{args[0]: args[1]}); err != nil {
				return err
			}
			if app.Emit(map[string]any{"id": id, "section": args[0]}) {
				return nil
			}
			app.Print("Updated %s section %q", id, args[0])
			return nil
		},
	}
	return cmd
}

func newFeatureDeleteCommand(app *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <ID>",
		Short: "Delete a feature spec",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			if !force {
				return Usage("deleting %s is irreversible; pass --force to confirm", args[0])
			}
			ctx := contextFor(cmd)
			spec, err := app.Workspace().FindSpec(args[0])
			if err != nil {
				return err
			}
			if err := app.VB().Delete(ctx, spec.ID); err != nil {
				return err
			}
			if err := app.VB().Index(ctx); err != nil {
				app.Warn("hvb: index regeneration failed: %v", err)
			}
			if app.Emit(map[string]any{"id": spec.ID, "deleted": true}) {
				return nil
			}
			app.Print("Deleted %s", spec.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm the deletion")
	return cmd
}

func newFeatureValidateCommand(app *App) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "validate [ID]",
		Short: "Validate feature specs against the VirtualBoard schema",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			result, err := app.VB().Validate(contextFor(cmd), target, fix)
			if err != nil {
				return err
			}
			if app.Emit(result) {
				if !result.OK() {
					return errSilent{}
				}
				return nil
			}
			app.Print("features: %d valid, %d invalid", result.Features.Valid, result.Features.Invalid)
			if result.Specs.Total > 0 {
				app.Print("specs:    %d valid, %d invalid", result.Specs.Valid, result.Specs.Invalid)
			}
			// Sorted, so two runs over the same board report the same
			// problems in the same order and a diff of the output is useful.
			for _, id := range sortedIDs(result.Features.Results) {
				for _, problem := range result.Features.Results[id].Errors {
					app.Warn("  %s: %s", id, problem)
				}
			}
			if !result.OK() {
				return errSilent{}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply vb's safe fixes before validating")
	return cmd
}

// errSilent exits non-zero without printing a second message: the command has
// already rendered the detail the user needs.
type errSilent struct{}

func (errSilent) Error() string { return "validation failed" }

func resolveFeatureID(args []string) (string, error) {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return strings.ToUpper(strings.TrimSpace(args[0])), nil
	}
	if id := os.Getenv("HVB_FEATURE_ID"); id != "" {
		return strings.ToUpper(id), nil
	}
	return "", Usage("no feature id given and $HVB_FEATURE_ID is not set")
}

func parseStatuses(values []string) (map[feature.Status]bool, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := map[feature.Status]bool{}
	for _, value := range values {
		status, ok := feature.ParseStatus(value)
		if !ok {
			return nil, Usage("unknown status %q", value)
		}
		out[status] = true
	}
	return out, nil
}

func hasAllLabels(spec *feature.Spec, labels []string) bool {
	for _, label := range labels {
		if !spec.HasLabel(label) {
			return false
		}
	}
	return true
}

func joinStatuses(statuses []feature.Status) string {
	if len(statuses) == 0 {
		return "nothing — it is terminal"
	}
	out := make([]string, len(statuses))
	for i, status := range statuses {
		out[i] = string(status)
	}
	return strings.Join(out, ", ")
}

func sortedPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key, value := range m {
		out = append(out, fmt.Sprintf("%s=%s", key, value))
	}
	sort.Strings(out)
	return out
}

func sortedIDs(results map[string]vb.ValidationEntry) []string {
	out := make([]string, 0, len(results))
	for id := range results {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
