# Configuration

Everything here is optional. hvb works with no configuration at all; the defaults are the
VirtualBoard lifecycle with human gates everywhere and Claude Code as the harness.

## Files

Two layers, merged **field by field**, later winning:

| Path | Scope |
|---|---|
| `$HVB_CONFIG`, else `$XDG_CONFIG_HOME/herdr-virtualboard/config.toml`, else `~/.config/herdr-virtualboard/config.toml` | your defaults across every project |
| `<project root>/.hvb.toml` | this project |

Field-by-field means a project file naming one column does not erase the rest of the pipeline, and a
project file setting `harness` does not reset your global `role`.

The project file sits **beside** `.virtualboard/`, keeping project policy separate from
the managed template files.

An unknown key is an error rather than a silent no-op, so a typo is reported when you save the file.

## Top level

```toml
harness          = "claude"        # default agent kind; see `hvb harness list`
role             = "fullstack_dev" # fallback when labels and status suggest nothing
owner            = ""              # handle hvb claims features under; see below
lock_ttl_minutes = 60              # vb lock TTL a dispatch takes; 0 disables locking
start_timeout    = "90s"           # bounds `herdr agent start`
prompt_timeout   = "10m"           # bounds a waiting `herdr agent prompt`
max_auto_stages  = 8               # maximum stages in an automatic chain; 0 disables auto
max_concurrent_runs = 3            # project-wide active run limit; 0 removes the limit
placement        = "overlay"       # where `hvb tui` opens as a plugin pane
```

`owner` resolves at use time: the configured value, then `$HVB_OWNER`, then `$USER`, then the
hostname, then `hvb`. It is written to the feature's `owner` frontmatter and taken as the `vb` lock
holder, so it is how a teammate or another board sees that a feature is taken.

`lock_ttl_minutes = 0` turns locking off entirely, which is the right setting for a board only one
person drives.

## Columns

One table per lifecycle status. Every key is optional.

```toml
[columns.in-progress]
auto       = false        # dispatch after an hvb move or completion routes a feature here
role       = "backend_dev"# override the suggested role for this column
harness    = "claude"     # override the harness for this column
prompt     = "Implement this feature end to end."
on_success = "review"     # where a successful run moves the feature
on_failure = "blocked"    # where a failed run moves it
timeout    = "45m"        # bound a run here; 0 or unset means no bound
```

`prompt` is the column's stage instruction, prepended to the dispatch prompt the way a herdr-board
column's system prompt is. It is where you say *what this stage is for* — build it, check it, harden
it — as opposed to what the feature is.

Without `auto`, the column is a human gate: work stops there until somebody moves the card on.

`on_success` and `on_failure` **must be legal VirtualBoard transitions from that column**, and are
checked when the file loads. A column routing `in-progress` to `done` is rejected as you save it,
not hours later when an agent finishes.

### The defaults

```toml
[columns.backlog]                  # a gate: nothing happens until you move a card out
[columns.in-progress]
on_success = "review"
on_failure = "blocked"
prompt     = "Implement this feature end to end. When the acceptance criteria are met, report success; if you are blocked by something you cannot resolve, report failure with the reason."
[columns.blocked]                  # a gate
[columns.review]
role       = "qa"
on_success = "done"
on_failure = "in-progress"
prompt     = "Review this feature against its acceptance criteria. Verify the tests actually exercise the behaviour. Report success only if every criterion is met; otherwise report failure and say exactly what is missing."
[columns.done]                     # terminal
```

### Turning the pipeline on

The defaults never dispatch by themselves. To make moving a card into `in-progress` start an agent,
and a successful review close the feature:

```toml
[columns.in-progress]
auto = true

[columns.review]
auto = true
```

Automatic dispatch runs after explicit `hvb feature move`, TUI moves, and successful
completion routing. External `vb move` operations update the displayed board but do
not start agents. Repeated reports do not start duplicate stages. At the stage or
concurrency limit, automatic dispatch stops with a diagnostic; it does not queue
background work. Inspect the feature, then start it manually or retry the handoff.
Negative limits are invalid. Cancellation suppresses the cancelled run's handoff.

## Recovery and review

Completion saves the first reported outcome before publishing, linking, routing,
indexing, and releasing the lease. `hvb run retry [RUN-ID]` resumes pending phases
or retries a degraded PR attempt. A nonzero completion exit requires inspection;
it does not mean the agent's outcome was lost. Cancelled completion cannot resume.

Worktree success requires a clean checkout on its assigned branch. Review inherits
the exact implementation commit, even when the default worktree setting is off.
Cleanup refuses another active run using the same checkout. `--force` stops its
own run before removal and permits discarding uncommitted changes.

Dispatch leases are renewed during reconciliation and checked before completion
mutates a feature. A lost lease stops the managed pane and parks the run for human
review. Run timeouts also stop the pane before failure routing. Failed or interrupted
launches stop their recorded pane before releasing the lease; failed cleanup
retains the active ledger entry for reconciliation or `hvb run cancel`. Quitting
the board cancels pending I/O and allows a short cleanup interval before restoring
the terminal. Existing worktrees are kept for review or explicit cleanup.

An unreadable run store is preserved and reported as an error. Use `hvb run recover`
to quarantine corrupt bytes and initialize an empty store; inspect orphaned panes
and worktrees afterward. A newer unsupported store format requires a compatible
hvb binary and cannot be reset through recovery.

## Environment

| Variable | Meaning |
|---|---|
| `HVB_CONFIG` | override the global config path |
| `HVB_DATA_DIR` | where run files live (default `~/.local/share/herdr-virtualboard`) |
| `HVB_PROJECT_ROOT` | project root, as if `--root` had been passed |
| `HVB_OWNER` | the handle hvb claims features under |
| `HVB_VB_BIN` | the `vb` executable to use |
| `HERDR_BIN_PATH` | the `herdr` executable to use; Herdr injects this into panes it manages |
| `HVB_CLI_INSTALL_DIR` | where `scripts/install-cli.sh` puts `hvb` (default `~/.local/bin`) |
| `NO_COLOR` | draw the board without colour |

### Set inside a dispatched agent's pane

These are the run's contract, documented in [`skill/SKILL.md`](../skill/SKILL.md). An agent reads
them; nothing else should set them.

| Variable | Meaning |
|---|---|
| `HVB_FEATURE_ID` | the feature the agent owns |
| `HVB_RUN_ID` | this dispatch |
| `HVB_ROLE` | the charter it adopted |
| `HVB_STATUS` | the status it was dispatched from |
| `HVB_PROJECT_ROOT` | the board repository root; worktree runs use a separate cwd |
| `VIRTUALBOARD_ROOT` | the same, for VirtualBoard tooling |
| `HVB_ON_SUCCESS` / `HVB_ON_FAILURE` | where a report will move the feature; absent when the column routes nowhere |

| `VB_OWNER` | the run's lease owner; inherited by vb mutations |
| `VB_LOCK_TOKEN` | the run's lease token; preserve it and keep it out of reports |
| `HVB_WORKTREE` | isolated checkout path and pane cwd, when enabled |
| `HVB_BRANCH` / `HVB_BASE_BRANCH` | assigned checkout branch and its base, when enabled |

## Dispatch compatibility and validation

Manual and automatic dispatch require the interfaces advertised by
`vb doctor --capabilities-only`: `owner-leases`, `lease-tokens`, `lease-renewal`,
`raw-schema-validation`, and `validation-exit-status`. The versioned report must
be complete and agree with its exit status. Version numbers alone do not prove
compatibility. Run `hvb doctor` after upgrading either binary.

Before forced cancellation or lease acquisition, hvb refreshes the target from
disk, confirms its lifecycle status, and invokes `vb validate FTR-####` without
`--fix`. It checks the target again under its acquired lease before creating a
worktree or pane. Missing results, contradictory counts, wrong feature IDs,
stale statuses, or actual validation errors prevent launch. `--force` only
permits explicit replacement/ownership takeover; these checks still apply.

A new lease must identify the target feature and configured owner, carry a token
and positive TTL, and have a valid future expiry. An incomplete/mismatched response
stops dispatch with an inspection command; hvb does not infer ownership or print
the returned token.

## Awaiting-run ownership

Parking a run records its outcome once and tracks whether its lease release was
confirmed. A failed release is retried during reconciliation without renewing the
awaiting run or replacing its observed outcome. Cleanup can proceed when Herdr is
unavailable. A missing or successor nonce means the old run no longer holds the
lease; cleanup never releases that successor. Legacy leases without a nonce
require explicit ownership inspection/resolution before automatic cleanup.

A human completing an awaiting run obtains a fresh lease without `--force` before
routing. Another owner's live lease returns a lock conflict, preserving both that
lease and the awaiting run. Cancellation and the final completion-release phase
also observe nonce ownership before attempting release. Lease values are redacted
from formatted CLI errors.

The run store requires an explicit supported version and a runs array (legacy
empty `null` arrays remain readable). Incomplete JSON objects, unknown fields,
duplicate run IDs, invalid feature/status/start metadata, and unknown run states
are rejected before mutations can replace bytes. Explicit recovery quarantines
unreadable supported-format data; future versions are never reset.
