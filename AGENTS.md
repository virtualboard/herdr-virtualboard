# Working on herdr-virtualboard

Rules for agents and humans changing this repository. `CLAUDE.md` points here.

## The premise, in one line

**The board is the repository.** `vb` and the markdown specs under `.virtualboard/features/` are the
single source of truth; hvb renders them and writes every change back through `vb`. Any change that
makes hvb hold an opinion about a feature that `vb` does not share is wrong, however convenient.

## Non-negotiables

1. **Never write a feature spec directly.** Every mutation goes through `internal/vb`. vb owns id
   allocation, the transition rules, the lock file, the hash-chained audit log, and
   `features/INDEX.md`. Reading specs off disk is fine — they are plain markdown.
2. **Never invent a transition.** `backlog → in-progress → review → done`, with `in-progress ↔
   blocked`. Nothing leaves `done`. The table in `internal/feature/status.go` is a duplicate kept so
   the TUI can grey out impossible moves; vb remains the authority.
3. **Never take a Herdr command or JSON shape from memory.** Read it live from the installed binary
   (`herdr <group>`, `herdr api schema --json`), then pin the exact argv in a test. See
   [`docs/herdr.md`](docs/herdr.md).
4. **Never let a test touch a real Herdr session.** `HERDR_BIN_PATH` beats `PATH`, so a stub on
   `PATH` is not enough — set `HERDR_BIN_PATH` and `HVB_VB_BIN`. See [`docs/testing.md`](docs/testing.md).
5. **Never widen what hvb stores.** The run store holds which pane is running which feature and
   nothing else. If you find yourself adding a feature's title, priority, or status to it for
   anything but display of a deleted feature, the answer is to read the spec instead.
6. **`make gates` before every push.** It is what CI runs.

## Layout

```text
cmd/hvb/            the binary
internal/
  feature/          spec model: frontmatter, sections, the lifecycle graph
  workspace/        finding .virtualboard, loading specs, project identity
  vb/               the vb CLI wrapper — the only writer of specs
  herdrcli/         the herdr CLI wrapper, with the version gate
  roles/            VirtualBoard agent charters and role selection
  runs/             the run store: the only state hvb owns
  config/           config files and the per-column pipeline policy
  dispatch/         prompt composition, pane-first launch, routing, reconciliation
  tui/              the kanban board
  cli/              the hvb command tree
skill/SKILL.md      the contract a dispatched agent is held to
e2e/                hermetic end-to-end scenarios
```

## Changing the agent contract

`skill/SKILL.md` is published and embedded. `internal/dispatch/skill.md` is the embedded copy —
`go:embed` cannot reach out of its package directory. Edit `skill/SKILL.md`, then:

```bash
make sync-skill
```

`TestEmbeddedSkillMatchesThePublishedOne` fails if you forget.

The contract is what a dispatched agent is judged against, and `hvb skill` prints those exact bytes
so the agent can check. Treat a change to it as a change to a public interface: if you add a rule,
`docs/configuration.md` and the README summary need to agree, and `TestEnvMatchesTheDocumentedContract`
asserts that every variable hvb injects is documented there.

## Style

The surrounding code is the specification. Beyond that:

- **Comments say why, not what.** Every non-obvious decision in this repository has a comment
  explaining the alternative that was rejected. Preserve that when you edit around one.
- **Errors carry the fix.** `"VirtualBoard does not allow in-progress → done (from in-progress you
  may move to: blocked, review)"`, not `"invalid transition"`.
- **Exit codes are the interface.** The first five mirror vb's own; 64 is anything hvb refused
  itself. Scripts branch on `$?`, so adding a code is a breaking change.
- **Degrade rather than refuse.** A missing role charter weakens the prompt; it does not abort a run
  whose agent is already live. A failed `pane rename` is cosmetic. An unreadable run file is preserved and requires explicit `hvb run recover`;
  a future schema requires a compatible binary. But an unsupported Herdr fails closed, because the alternative is an untrackable agent.
- **No new dependencies without a strong reason.** The four current ones are cobra, toml, yaml, and
  `x/term`. The TUI is hand-drawn partly to keep it that way.

## VirtualBoard features in this repository

If `.virtualboard/` exists here, the usual VirtualBoard rules apply to it: claim a feature by moving
it to `in-progress` with your owner set, reference `FTR-####` in commits and PRs, move it through
`review` to `done`, and never move one out of `done`. Run `vb validate` before committing.
