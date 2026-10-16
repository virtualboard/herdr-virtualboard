// Package config loads hvb's settings: the default harness, the per-status
// pipeline policy, and dispatch timeouts.
//
// Two files are read, in order, and the later one wins field by field:
//
//	~/.config/herdr-virtualboard/config.toml   global defaults
//	<project root>/.hvb.toml                   per-project overrides
//
// The project file sits next to `.virtualboard/`, not inside it, because
// `vb init --update` re-applies the upstream template over that directory and
// would overwrite anything hvb left there.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/git"
)

// ProjectFile is the per-project config filename, resolved against the project
// root (the directory holding `.virtualboard/`).
const ProjectFile = ".hvb.toml"

// Config is the resolved settings for one project.
type Config struct {
	MaxAutoStages     int `toml:"max_auto_stages"`
	MaxConcurrentRuns int `toml:"max_concurrent_runs"`
	// Harness is the default Herdr agent kind for dispatches.
	Harness string `toml:"harness"`
	// Role is the fallback VirtualBoard role when a feature's labels and
	// status suggest none.
	Role string `toml:"role"`
	// Owner is the handle written to a feature's `owner` frontmatter and
	// its vb lock when hvb claims it. Empty resolves at use time from
	// $HVB_OWNER, $USER, then the hostname.
	Owner string `toml:"owner"`
	// LockTTLMinutes is the vb lock TTL a dispatch takes. Zero disables
	// locking, which is the right setting for a single-agent board.
	LockTTLMinutes int `toml:"lock_ttl_minutes"`
	// StartTimeout bounds `herdr agent start`.
	StartTimeout Duration `toml:"start_timeout"`
	// PromptTimeout bounds the first `herdr agent prompt --wait`.
	PromptTimeout Duration `toml:"prompt_timeout"`
	// Placement is where `hvb tui` opens as a plugin pane.
	Placement string `toml:"placement"`
	// Columns is the per-status pipeline policy.
	Columns map[string]Column `toml:"columns"`
	// Worktree configures isolated git checkouts for dispatched agents.
	Worktree Worktree `toml:"worktree"`
	// Forge configures pull-request creation.
	Forge Forge `toml:"forge"`
}

// Worktree configures running an agent in an isolated git checkout rather than
// in the project directory.
type Worktree struct {
	// Enabled makes a worktree the default for dispatches that do not say
	// otherwise. It stays off by default: a worktree is a real directory on
	// disk and a real branch, and creating either without being asked would
	// be a surprise.
	Enabled bool `toml:"enabled"`
	// Branch is the branch-name template. `{id}`, `{id_lower}` and `{slug}`
	// are substituted; the default matches VirtualBoard's own convention.
	Branch string `toml:"branch"`
	// Base is the branch features are cut from. Empty means the repository's
	// default branch, resolved from the remote.
	Base string `toml:"base"`
	// Remote is the git remote to branch from and push to.
	Remote string `toml:"remote"`
}

// Forge configures pull-request creation.
type Forge struct {
	// Enabled opens a pull request when a worktree run succeeds.
	Enabled bool `toml:"enabled"`
	// Draft opens the pull request as a draft. On by default: an agent's
	// work should be looked at before it asks for review.
	Draft bool `toml:"draft"`
	// Kind overrides forge detection for a self-hosted instance whose
	// hostname gives nothing away: "github", "forgejo", "gitea", "gitlab".
	Kind string `toml:"kind"`
	// Token authenticates the Forgejo and Gitea clients. GitHub uses the
	// gh CLI's own credentials and needs nothing here. Empty falls back to
	// $HVB_FORGE_TOKEN.
	Token string `toml:"token"`
	// BaseURL overrides the API root derived from the remote host.
	BaseURL string `toml:"base_url"`
}

// ResolveToken returns the forge token, preferring configuration then the
// environment. Keeping the environment as a fallback means a token never has
// to be written into a file that might be committed.
func (f Forge) ResolveToken() string {
	if f.Token != "" {
		return f.Token
	}
	return os.Getenv("HVB_FORGE_TOKEN")
}

// Column is the policy for one lifecycle status.
type Column struct {
	// Auto dispatches an agent as soon as a feature lands in this status.
	// Without it the status is a human gate: work stops there until someone
	// moves the card on.
	Auto bool `toml:"auto"`
	// Role overrides the suggested role for features in this status.
	Role string `toml:"role"`
	// Harness overrides the default agent kind for this status.
	Harness string `toml:"harness"`
	// Prompt is prepended to the dispatch prompt, the way a herdr-board
	// column's system prompt is.
	Prompt string `toml:"prompt"`
	// OnSuccess and OnFailure are the statuses a finished run moves the
	// feature to. Both must be legal VirtualBoard transitions from this
	// status; Validate rejects anything else rather than letting a dispatch
	// fail later against vb.
	OnSuccess string `toml:"on_success"`
	OnFailure string `toml:"on_failure"`
	// Timeout bounds a run in this status. Zero means no bound.
	Timeout Duration `toml:"timeout"`
	// Worktree dispatches runs from this column into an isolated checkout.
	// Unset inherits the global setting; set here it wins.
	Worktree *bool `toml:"worktree"`
	// PR opens a pull request when a run from this column succeeds. Unset
	// inherits the global setting.
	PR *bool `toml:"pr"`
}

// UseWorktree resolves the column's worktree setting against the global one.
func (c Column) UseWorktree(global bool) bool {
	if c.Worktree != nil {
		return *c.Worktree
	}
	return global
}

// UsePR resolves the column's pull-request setting against the global one.
func (c Column) UsePR(global bool) bool {
	if c.PR != nil {
		return *c.PR
	}
	return global
}

// Duration is a TOML-friendly time.Duration parsed from strings like "45m".
type Duration time.Duration

// UnmarshalText decodes a Go duration string.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalText encodes the duration back to its string form.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Default is the built-in policy. It mirrors the VirtualBoard workflow the
// agent charters describe: backlog and blocked are human gates, in-progress
// builds, review checks, and done is terminal.
func Default() Config {
	return Config{
		MaxAutoStages:     8,
		MaxConcurrentRuns: 3,
		Harness:           "claude",
		Role:              "fullstack_dev",
		LockTTLMinutes:    60,
		StartTimeout:      Duration(90 * time.Second),
		PromptTimeout:     Duration(10 * time.Minute),
		Placement:         "overlay",
		Worktree:          Worktree{Branch: git.BranchTemplate, Remote: "origin"},
		Forge:             Forge{Draft: true},
		Columns: map[string]Column{
			string(feature.Backlog): {},
			string(feature.InProgress): {
				Auto:      false,
				OnSuccess: string(feature.Review),
				OnFailure: string(feature.Blocked),
				Prompt:    "Implement this feature end to end. When the acceptance criteria are met, report success; if you are blocked by something you cannot resolve, report failure with the reason.",
			},
			string(feature.Blocked): {},
			string(feature.Review): {
				Auto:      false,
				Role:      "qa",
				OnSuccess: string(feature.Done),
				OnFailure: string(feature.InProgress),
				Prompt:    "Review this feature against its acceptance criteria. Verify the tests actually exercise the behaviour. Report success only if every criterion is met; otherwise report failure and say exactly what is missing.",
			},
			string(feature.Done): {},
		},
	}
}

// Column returns the policy for a status, falling back to an empty policy.
func (c *Config) Column(status feature.Status) Column {
	if column, ok := c.Columns[string(status)]; ok {
		return column
	}
	return Column{}
}

// Load resolves the global and project config over the defaults. A missing file
// at either layer is normal and contributes nothing.
func Load(projectRoot string) (*Config, error) {
	resolved := Default()
	globalPath, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	for _, path := range []string{globalPath, filepath.Join(projectRoot, ProjectFile)} {
		if path == "" {
			continue
		}
		if err := mergeFile(&resolved, path); err != nil {
			return nil, err
		}
	}
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	return &resolved, nil
}

// GlobalPath is $HVB_CONFIG, else $XDG_CONFIG_HOME/herdr-virtualboard/config.toml,
// else ~/.config/herdr-virtualboard/config.toml.
func GlobalPath() (string, error) {
	if path := os.Getenv("HVB_CONFIG"); path != "" {
		return path, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "herdr-virtualboard", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "herdr-virtualboard", "config.toml"), nil
}

// mergeFile overlays one TOML file onto cfg. Only the keys the file actually
// sets are applied, so a project file that names one column does not erase the
// rest of the pipeline.
func mergeFile(cfg *Config, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var overlay Config
	meta, err := toml.Decode(string(raw), &overlay)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return fmt.Errorf("parse %s: unknown key %q", path, undecoded[0].String())
	}

	if meta.IsDefined("harness") {
		cfg.Harness = overlay.Harness
	}
	if meta.IsDefined("role") {
		cfg.Role = overlay.Role
	}
	if meta.IsDefined("owner") {
		cfg.Owner = overlay.Owner
	}
	if meta.IsDefined("lock_ttl_minutes") {
		cfg.LockTTLMinutes = overlay.LockTTLMinutes
	}
	if meta.IsDefined("max_auto_stages") {
		cfg.MaxAutoStages = overlay.MaxAutoStages
	}
	if meta.IsDefined("max_concurrent_runs") {
		cfg.MaxConcurrentRuns = overlay.MaxConcurrentRuns
	}
	if meta.IsDefined("start_timeout") {
		cfg.StartTimeout = overlay.StartTimeout
	}
	if meta.IsDefined("prompt_timeout") {
		cfg.PromptTimeout = overlay.PromptTimeout
	}
	if meta.IsDefined("placement") {
		cfg.Placement = overlay.Placement
	}
	for _, field := range []struct {
		key   string
		apply func()
	}{
		{"worktree.enabled", func() { cfg.Worktree.Enabled = overlay.Worktree.Enabled }},
		{"worktree.branch", func() { cfg.Worktree.Branch = overlay.Worktree.Branch }},
		{"worktree.base", func() { cfg.Worktree.Base = overlay.Worktree.Base }},
		{"worktree.remote", func() { cfg.Worktree.Remote = overlay.Worktree.Remote }},
		{"forge.enabled", func() { cfg.Forge.Enabled = overlay.Forge.Enabled }},
		{"forge.draft", func() { cfg.Forge.Draft = overlay.Forge.Draft }},
		{"forge.kind", func() { cfg.Forge.Kind = overlay.Forge.Kind }},
		{"forge.token", func() { cfg.Forge.Token = overlay.Forge.Token }},
		{"forge.base_url", func() { cfg.Forge.BaseURL = overlay.Forge.BaseURL }},
	} {
		if meta.IsDefined(strings.Split(field.key, ".")...) {
			field.apply()
		}
	}
	for name, column := range overlay.Columns {
		base := cfg.Column(feature.Status(name))
		if meta.IsDefined("columns", name, "auto") {
			base.Auto = column.Auto
		}
		if meta.IsDefined("columns", name, "role") {
			base.Role = column.Role
		}
		if meta.IsDefined("columns", name, "harness") {
			base.Harness = column.Harness
		}
		if meta.IsDefined("columns", name, "prompt") {
			base.Prompt = column.Prompt
		}
		if meta.IsDefined("columns", name, "on_success") {
			base.OnSuccess = column.OnSuccess
		}
		if meta.IsDefined("columns", name, "on_failure") {
			base.OnFailure = column.OnFailure
		}
		if meta.IsDefined("columns", name, "timeout") {
			base.Timeout = column.Timeout
		}
		if meta.IsDefined("columns", name, "worktree") {
			base.Worktree = column.Worktree
		}
		if meta.IsDefined("columns", name, "pr") {
			base.PR = column.PR
		}
		if cfg.Columns == nil {
			cfg.Columns = map[string]Column{}
		}
		cfg.Columns[name] = base
	}
	return nil
}

// Validate rejects a configuration that would fail at dispatch time: an unknown
// status, a routing destination VirtualBoard does not allow, or a forge kind
// hvb has no client for.
func (c *Config) Validate() error {
	if c.MaxAutoStages < 0 || c.MaxConcurrentRuns < 0 {
		return fmt.Errorf("config: automation and concurrency limits cannot be negative")
	}
	switch git.Kind(strings.ToLower(c.Forge.Kind)) {
	case "", git.GitHub, git.Forgejo, git.Gitea, git.GitLab, git.Unknown:
	default:
		return fmt.Errorf("config: forge.kind %q is not one of github, forgejo, gitea, gitlab", c.Forge.Kind)
	}
	for name, column := range c.Columns {
		status, ok := feature.ParseStatus(name)
		if !ok {
			return fmt.Errorf("config: unknown column %q (expected one of backlog, in-progress, blocked, review, done)", name)
		}
		for label, target := range map[string]string{"on_success": column.OnSuccess, "on_failure": column.OnFailure} {
			if target == "" {
				continue
			}
			parsed, ok := feature.ParseStatus(target)
			if !ok {
				return fmt.Errorf("config: column %q %s: unknown status %q", name, label, target)
			}
			if !feature.CanTransition(status, parsed) {
				return fmt.Errorf("config: column %q %s: VirtualBoard does not allow %s → %s", name, label, status, parsed)
			}
		}
	}
	return nil
}

// ResolveOwner returns the handle hvb claims features under.
func (c *Config) ResolveOwner() string {
	if c.Owner != "" {
		return c.Owner
	}
	if owner := os.Getenv("HVB_OWNER"); owner != "" {
		return owner
	}
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "hvb"
}
