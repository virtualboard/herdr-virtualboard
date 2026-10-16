# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`hvb doctor`** checks CLI compatibility, board metadata, leases, run records,
  audit integrity, and orphaned Herdr resources; it reports repair steps without
  mutating project files.
- **TUI search and filters** cover owner, label, status, and free text, with a
  collapsed completed-work section and asynchronous terminal-safe refreshes.
- **Bounded transition automation** can start the next workflow role after a
  feature transition, with duplicate, concurrency, and stage limits.
- **Optional worktree dispatch.** Moving a card into `in-progress` now asks
  whether to start an agent, and how isolated it should be: in the project, in a
  git worktree on the feature's own branch, or in a worktree with a pull request
  at the end. Declining is the default. `hvb run start --worktree` / `--pr` do
  the same from the shell.
- **Worktrees through Herdr's own support.** A worktree run uses
  `herdr worktree create`, so the checkout appears as a linked workspace grouped
  beside the project rather than as a directory nobody can see. The branch name
  follows VirtualBoard's convention, `feature/<ID>/<slug>`.
- **Pull requests.** A successful worktree run pushes its branch and opens a PR
  with the feature's acceptance criteria and the agent's commits in the body,
  then writes the link into the spec's Links section through `vb`. GitHub uses
  the `gh` CLI's existing credentials; Forgejo and Gitea use their REST API with
  a token from `forge.token` or `$HVB_FORGE_TOKEN`.
- **`hvb run cleanup`** removes a finished run's checkout, refusing while work is
  uncommitted unless forced.

### Notes

- A pull request that cannot be opened — no token, no client for the forge, no
  commits, a dirty checkout — never fails the run. The outcome stands, the
  feature still moves, and the reason is recorded with a compare URL where one
  can be built.
- Nothing here is on by default. No branch is cut and no forge is contacted
  unless a dispatch asks for it.

### Changed

- Run documents use a versioned format with quarantine-based corruption
  recovery. Unsupported future formats fail closed and retain cleanup records.
- Completion side effects are retryable and serialized. Cancellation and
  timeout stop the recorded pane before lease release; nonce ownership prevents
  an old run from releasing a successor's lease.
- Review runs inherit the implementation worktree revision, and cleanup
  protects active reviewer checkouts.

## [0.1.0] — 2026-09-15

First release.

### Added

- **The board.** `hvb tui` renders a VirtualBoard workspace as five lifecycle columns — backlog,
  in-progress, blocked, review, done — with card detail, run history, and a single-column layout
  below 108 terminal columns. Every mutation goes through `vb`, so the board and the repository
  cannot disagree.
- **Dispatch.** Pressing `d` starts a VirtualBoard role agent in a visible Herdr pane: one stable
  `ftr-####` tab per feature, a pane-first launch, and a prompt composed from the role charter, the
  agent contract, the column's stage instruction, and the spec.
- **Role selection** from `.virtualboard/agents/`, inferred from a feature's labels and status, with
  a `role:` label or `--role` overriding.
- **Outcome routing.** An agent reports once with `hvb run done`; the column's success and failure
  routes move the feature, and a blocked report goes to `blocked` wherever the lifecycle allows it.
- **Reconciliation without a daemon.** Every read path settles active runs against live Herdr state:
  a closed pane or a harness that finished without reporting parks the run as `awaiting` for a human,
  without moving the feature.
- **The CLI:** `feature list|show|new|move|set|note|delete|validate`, `run
  start|list|show|done|comment|cancel|focus|log`, `role`, `harness`, `doctor`, `skill`, `version`.
  `--json` on everything, with errors in a stable envelope on stderr and stdout left empty.
- **The agent contract** in `skill/SKILL.md`, printed verbatim by `hvb skill` and embedded in every
  dispatch prompt.
- **Plugin packaging:** `herdr-plugin.toml` with build steps, an overlay pane, and an idempotent
  open-or-focus-or-toggle launcher action.
- **Configuration** at `~/.config/herdr-virtualboard/config.toml` and `.hvb.toml`, merged field by
  field, with routes validated against the lifecycle at load time.

- **A notice screen** for every startup failure. An overlay pane whose command exits immediately is
  invisible, so a missing workspace, a missing `vb`, or an unsupported Herdr draws an explanation and
  waits rather than flickering and vanishing.

### Notes

- Requires exactly Herdr **0.9.0** (socket protocol 22). The version gate is policy, not
  negotiation; only cleanup of panes hvb already owns is exempt.
- A freshly split pane is not an available shell until its shell reaches a prompt, so `agent start`
  is retried for up to ten seconds.
- A plugin pane inherits the focused pane's working directory, so the board opens on whatever
  project you are looking at.

[0.1.0]: https://github.com/virtualboard/herdr-virtualboard/releases/tag/v0.1.0
