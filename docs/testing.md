# Testing

```bash
make gates
```

That is the whole contract. CI runs exactly that target, so a green local run means a green
pipeline, and there is no second list of steps to keep in sync.

`gates` is `fmt-check`, `vet`, `test-race`, and `e2e`.

## Unit tests

`make test` / `make test-race`. They cover the parts hvb owns:

| Package | What is pinned |
|---|---|
| `feature` | the whole transition graph, frontmatter parsing, section extraction, acceptance criteria |
| `workspace` | discovery, symlink-stable identity, directory-over-frontmatter status, broken specs surfacing |
| `vb` | the argv sent to `vb` and the mapping from its exit codes to typed errors |
| `herdrcli` | the exact argv sent to `herdr`, response decoding, the version gate |
| `runs` | idempotent completion, concurrent writes, pruning, Herdr's agent-name grammar |
| `roles` | charter loading, role suggestion precedence |
| `config` | field-wise merging, and rejecting routes the lifecycle forbids |
| `dispatch` | prompt composition, outcome routing, reconciliation, the pull-request path |
| `git` | branch naming, remote parsing, push — against real repositories |
| `forge` | GitHub through a stub `gh`, Forgejo through an in-memory HTTP handler |
| `tui` | geometry, overlays, search/filters, completed collapse, frozen snapshots, and responsive background I/O |
| `cli` | exit-code mapping, the JSON error envelope, command-tree completeness |

Two of these are worth knowing about because they exist to catch a specific mistake:

- **`TestAllowedTransitions`** asserts the entire lifecycle graph. `internal/feature/status.go`
  duplicates the table from vb-cli so the TUI can grey out impossible moves; if vb ever changes the
  graph, this is what should start failing.
- **`TestEmbeddedSkillMatchesThePublishedOne`** fails when `skill/SKILL.md` and
  `internal/dispatch/skill.md` drift. They are two files only because `go:embed` cannot reach out of
  its package directory. Run `make sync-skill`.

### Testing against stub binaries

`internal/vb` and `internal/herdrcli` are tested against shell stubs that record their argv. Testing
against the real tools would make the suite depend on a VirtualBoard install and on Herdr's release
cadence; testing against a stub pins **the argv hvb actually emits**, which is the part hvb owns.

## End-to-end

`make e2e`, or one scenario directly:

```bash
./e2e/03-dispatch.sh
```

Each scenario runs the real `hvb` binary against a real temporary VirtualBoard workspace, with stub
`vb` and `herdr` executables. The `vb` stub actually moves spec files between directories, so a
scenario can assert that the board changed rather than only that a command was called.

| Scenario | Covers |
|---|---|
| `01-board-basics` | discovery from a subdirectory, listing, filters, detail, `--json` |
| `02-lifecycle` | every legal and illegal transition, including that nothing leaves `done` |
| `03-dispatch` | the pane-first launch sequence, the injected environment, anchor cleanup |
| `04-agent-contract` | what a dispatched agent can do with only its run environment |
| `05-failure-routing` | where failure and blocked outcomes send a feature from each column |
| `06-reconciliation` | a vanished pane and a silent harness both parking as `awaiting` |
| `07-doctor` | workspace diagnostics, read-only run-store checks, compatibility refusal preserving active runs, and unsupported Herdr |
| `08-json-contract` | valid JSON on stdout, the error envelope on stderr, stdout empty on failure |
| `09-worktree-pr` | a worktree run, a local push, exact-revision review handoff, and safe cleanup |
| `10-automatic-pipeline` | automatic transitions, stage/concurrency limits, and duplicate protection |
| `11-terminal-ux` | real PTY search/filter controls, completed collapse, scrollable help, quit, terminal restoration, and unchanged specs |

### Why the stubs are not optional

The suite must never touch the developer's Herdr session. Note that **`HERDR_BIN_PATH` takes
precedence over `PATH`** — which is correct, because a plugin running inside Herdr should use the
binary that spawned it — so putting a stub on `PATH` is not enough. `e2e/lib.sh` sets
`HERDR_BIN_PATH` and `HVB_VB_BIN` explicitly. Removing either makes the suite create real panes and
real workspaces in whatever session you happen to be running.

This was not hypothetical: the first version of the suite did exactly that.

## Testing against a real Herdr

Sometimes you have to, because only a live server catches a wrong field name. Do it in a scratch
directory and clean up after yourself:

```bash
vb init
hvb doctor                    # prove the gate passes
hvb run start FTR-0001 --harness claude
herdr tab list --workspace <ws>
herdr workspace close <ws>    # close what you created, and only that
```

When you learn something from a live server, pin it in `internal/herdrcli/client_test.go` and record
it in [`docs/herdr.md`](herdr.md) so the next person does not have to rediscover it.

## Cross-repository contracts

The separate `integration` target uses real `vb` and `hvb` binaries with the
actual sibling `template-base` schemas and templates:

```bash
cd ../vb-cli
go build -o /tmp/virtualboard-contract-vb .
cd ../herdr-virtualboard
make integration VB_REAL_BIN=/tmp/virtualboard-contract-vb
```

Set the `TEMPLATE_SOURCE` make variable to use a different template checkout.
`integration/contracts.sh` tests rejected writes, owner leases, immutable IDs,
lifecycle-only status changes, role metadata, full/light feature templates, and
validation diagnostics through both CLIs. It explicitly disables Herdr access.
`integration/reports.sh` renders all 21 HTML templates twice to verify deterministic
output and default logo delivery, then renders the populated progress example.
Unit report tests separately exercise injection escaping, invalid input, include
traversal/cycles, resource limits, and preservation of existing output.

These checks are additional to `make gates`; the release workflow supplies the
exact refs from `.github/release-versions`. Running against stubs alone does not
prove compatibility with those revisions.

### TUI worker and input tests

`internal/tui/async_test.go` drives the real event loop with a frame sink and
explicitly gated backend calls. It verifies navigation, resize, and quit during
slow I/O; current selection and typed form preservation; serialized/coalesced
refresh and dispatch; cancellation propagation; and recovery of failed creation
forms without offering to repeat a partially successful creation.

`internal/tui/filters_test.go` checks combined search/owner/label semantics,
Unicode search, empty results, completed counts and collapse across reloads,
read-only filtering, narrow frames, and scrolling help to reach diagnostics.
No test opens a terminal or a real Herdr session.

`TestCancelledLaunchStopsPaneBeforeReleasingLease` checks cleanup after cancelled
launches against explicit stubs: the managed writer must stop before lease
release. If cleanup cannot confirm that the pane stopped, its active run record
and lease stay available for reconciliation and explicit cancellation.

### Reproduce terminal previews

The PTY scenario can save its monochrome terminal stream for documentation. The
optional renderer uses Pillow and a local monospace font; it is not a runtime or
CI dependency:

```bash
HVB_TUI_CAPTURE_DIR=/tmp/hvb-frames bash e2e/11-terminal-ux.sh
python3 scripts/render-terminal-preview.py --input /tmp/hvb-frames/filters.ansi --output docs/assets/filters.png
python3 scripts/render-terminal-preview.py --input /tmp/hvb-frames/completed-collapsed.ansi --output docs/assets/completed-collapsed.png
```

Use `--font PATH` if neither Menlo nor DejaVu Sans Mono is installed. The default
140×35 geometry matches the PTY scenario; `--columns` and `--rows` support other
captures. Previews render the tested terminal frame, not a simulated board.

The doctor unit tests also pin the `vb --json doctor` interface, preserve an
unhealthy report returned with exit 1, reject incomplete reports and contradictory
health/exit statuses, and verify that corrupt run-store bytes remain unchanged.
`TestOpenAndListAbsentStoreDoNotWrite` verifies lazy directory creation followed by
a successful first mutation.

Resource-diagnostic tests cover project-scoped untracked panes, retained historical
panes, missing checkouts/registrations, successor lease mismatches, pending
completion remedies, and token/error-text omission. Read-only stubs refuse every
command except inventory/status requests. Git worktree tests use real temporary
repositories and preserve paths containing spaces/newlines and missing registrations.
Pane-list protocol tests reject missing arrays and empty/duplicate IDs, so an
incomplete response cannot prove an agent disappeared.

Dispatch preflight tests preserve an active run byte-for-byte when a forced
replacement encounters an older CLI, missing safety capabilities, invalid or
incomplete validation, a wrong target, or stale status. A second validation failure
prevents agent creation, and automatic dispatch uses the same compatibility gate.
The CLI scenario also swaps in an incompatible binary and verifies that `--force`
leaves the existing run record unchanged.

Lease-claim tests reject incomplete metadata, missing tokens, wrong identities,
and expired responses while accepting a matching renewable lease. Error text must
omit token values. The end-to-end stub advertises capabilities and returns matching
feature/status validation results and renewable lease metadata.

Forge REST tests supply `forge.Options.HTTPClient` with a handler transport. The
public opener still constructs requests and handles response codes and JSON;
assertions cover HTTPS host/path, POST, authentication/content headers, payload,
drafts, rejected tokens, and conflicts. No global transport is replaced and no
socket is opened. TCP/TLS negotiation remains outside these unit tests.

## Shared workflow contract

The canonical `template-base/schemas/workflow-contract.json` is copied byte-for-byte
to each Go repository's `testdata/workflow-contract.json`. Unit tests compare actual
lifecycle rules, board ordering, role selection, CLI commands/flags/exit constants,
and doctor capabilities against it. Sync intentional changes with template-base's
`scripts/sync-workflow-contract.sh`, then review all consumers. Never change a
fixture simply to bless unintended behavior.

`make integration VB_REAL_BIN=/path/to/vb` also verifies matching fixture bytes and
runs all 25 lifecycle pairs through each real binary (50 attempts). Allowed moves
must reach the target; refused moves preserve source bytes and return each CLI's
documented refusal status: vb transition exit 3, hvb local usage exit 64. It checks
the real capability report too. `TEMPLATE_SOURCE` may select another template
checkout, and `VB_SOURCE` may select the vb source checkout whose fixture is
compared. Every Herdr invocation remains stubbed or explicitly unavailable.

Recovery tests cover incomplete store objects, unknown metadata, future-format
protection before field decoding, v1 compatibility, and validation before writes.
Lease tests simulate a release failure, reopen the store as a fresh process, and
finish cleanup with Herdr unavailable. Settled awaiting runs perform no repeated
release/renewal. Successor nonce preservation and token-redacted failures are
checked alongside a fresh lease before a human outcome.

Real CLI integration dispatches against the recording Herdr stub with actual vb
leases. It verifies parked lease removal, conflict with another owner, a fresh
nonce before human routing, and successor preservation during cancellation. No
real agent/session/provider is used.
