# Design

## The one decision everything else follows from

**VirtualBoard already has a board. It is the repository.**

`.virtualboard/features/<status>/FTR-####-slug.md` — the directory is the status, the frontmatter is
the card, and `vb` is the tool that moves them. That layout is committed, reviewed, and shared. Any
plugin that kept its own copy of a card would immediately face the question of which copy is right
when a teammate runs `vb move` from a shell, or when a `git pull` brings in five features somebody
else moved.

So hvb keeps no copy. It renders the repository and writes every change back through `vb`.

This is the difference from [herdr-board](https://github.com/nelsonPires5/herdr-board), which hvb is
otherwise modelled on. There, a card is a prompt the board invented, so the board must own it, which
means SQLite, which means a daemon to own the SQLite, which means a protocol between the daemon and
its clients. Here the card already exists and already has an owner, and none of that follows.

## What hvb does own

Exactly one thing the repository cannot hold: **which Herdr pane is currently running which
feature.**

That fact is machine-local, lives for minutes, and would be pure noise in a commit. It lives in one
JSON file per project under `~/.local/share/herdr-virtualboard/runs/<project-id>.json`, keyed on the
workspace's resolved absolute path.

The run store keeps execution evidence and recoverable completion phases, not a
second copy of feature status. Corrupt bytes are preserved and reported, and
`hvb run recover` explicitly quarantines them before starting an empty store.
Future schemas require a compatible binary. Recovery can lose local run tracking,
so inspect panes and worktrees rather than treating live resources as disposable.

## Why there is no daemon

A daemon earns its keep when something must be watched while no user-facing process is running.
herdr-board needs one: it owns card state, so it must react to Herdr events whether or not the TUI
is open.

hvb has no such state. What it does need — noticing that a run's pane closed, or that a harness
finished without reporting — it gets by *looking*, in `Reconcile`, which every read path calls
before answering:

```text
active run, pane gone from `herdr pane list`   → awaiting  (reason: pane_closed)
active run, agent reports `done`, no report    → awaiting  (reason: agent_done)
active run, past the column's timeout          → failed, routed
anything else                                  → left alone
```

`awaiting` is not a failure. It is the board saying *the agent stopped and nobody knows why yet*.
The feature keeps its status and its owner: a run that stopped unexplained has not earned a
transition in either direction, and only a human can say whether the work landed.

Two rules keep reconciliation honest:

- **It never guesses.** If `herdr pane list` fails, nothing is settled. Marking every run dead
  because Herdr was briefly unreachable would be far worse than doing nothing.
- **It never parks the caller's own run.** An agent shelling out to `hvb run show` reconciles on the
  way past. It is alive — it is the thing asking — and Herdr can legitimately report its pane as
  `done` between turns, so `$HVB_RUN_ID` is exempt.

## The lifecycle is the pipeline

VirtualBoard's transition graph is not a suggestion:

```text
backlog ──→ in-progress ──→ review ──→ done
                 ↑ ↓            │
              blocked ──────────┘  (review → in-progress)
```

Nothing leaves `done`. hvb duplicates this table in `internal/feature/status.go` so the TUI can grey
out impossible moves rather than offering them and failing — but `vb` remains the authority, and
every move still goes through it. The duplicate exists to make the lifecycle *visible*, not to
enforce it. If `vb` ever changes the graph, `TestAllowedTransitions` is what starts failing.

The same table validates configuration at load time. A column routing `in-progress` to `done` is
rejected when you edit the file, not hours later when an agent finishes and the move is refused.

## Dispatch

Roles come from `.virtualboard/agents/`, the charters VirtualBoard already ships. Using them rather
than inventing a prompt format means a dispatched agent adopts the identity VirtualBoard's own rules
of engagement expect it to announce.

Selection, in order: an explicit `--role`, the column's configured role, a `role:` label on the
feature, the label hint table, `qa` for anything in review, then the configured fallback.

The launch is **pane-first**, which is Herdr's documented contract rather than a choice:

1. gate on Herdr 0.9.0 / protocol 22;
2. find or create the workspace rooted at the project;
3. find or create the feature's `ftr-####` tab — one per feature, reused across runs, so three
   dispatches do not leave three tabs;
4. split a child pane with the project root as cwd and the run's variables in its environment;
5. `agent start` in **that** child, never the anchor, retrying briefly while the new shell reaches
   its prompt;
6. close the anchor, leaving exactly one pane per run;
7. `agent prompt` with the composed prompt.

A failed launch keeps the anchor. It is the evidence of what went wrong, and closing it would hide
the error.

### Why the prompt is ordered the way it is

Role charter, then contract, then stage instruction, then the spec — and the spec last, still inside
its `<untrusted-content>` delimiters, with an explicit line saying it is data.

That ordering mirrors how VirtualBoard expects an agent to come up to speed, and the delimiters are
the boundary its rules of engagement draw between *what to build* and *what you are allowed to be
told*. A spec body is user-authored text that an agent will read; keeping the marker means a
sentence in a feature description that looks like an instruction stays visibly data.

## Talking to Herdr

Through the `herdr` CLI, not the socket. The CLI is the contract Herdr documents for external
callers, it already emits the JSON this code decodes, and hvb needs no event subscription — there is
nothing a second protocol would buy.

The version gate is policy, not negotiation: exactly 0.9.0 and protocol 22. A different protocol may
have moved a field this code reads, and failing the dispatch beats launching an agent into a pane
hvb can no longer track. The one exception, as in herdr-board, is cleanup: closing a pane hvb
created stays ungated, so a Herdr upgrade cannot strand a run hvb is responsible for tidying up.

Every argv in `internal/herdrcli` was verified against a running Herdr 0.9.0 and is pinned by a test
that asserts the exact command line. Do not change one from memory — see
[`docs/herdr.md`](herdr.md).

## Rendering

The TUI is hand-drawn with ANSI escapes rather than built on a framework. Two things make that the
right trade for a plugin: the binary stays small and its dependency surface stays four modules, and
every view is a pure function from state to `[]string`, so the whole layout is unit-testable without
a terminal.

The cost is that width arithmetic is hvb's problem. `displayWidth` skips SGR sequences and counts
wide runes as two cells, and everything that lays anything out goes through it — an off-by-one there
corrupts the surrounding Herdr pane, not just the board. Frames are diffed line by line so a refresh
that moves one card repaints one row.

## Keeping the board responsive

The terminal loop owns all view state. One worker serializes backend reads,
reconciliation, and actions, then returns frozen snapshots and UI continuations.
Navigation, resize, search, and form entry continue while a command waits. Results
preserve the selection at delivery time and never replace a form the user opened
while the request was pending. At most one refresh and one action are outstanding;
periodic refreshes coalesce instead of creating an unbounded queue.

Filters and completed-card collapse operate only on the latest in-memory snapshot.
They retain full board data and global run counts, while selection and actions use
only matching, expanded cards. Background reloads rebuild that same filtered view.

Quitting cancels I/O and gives context-aware operations a bounded cleanup interval.
A failed dispatch stops its recorded pane before releasing the feature lease. If
that cannot be confirmed, the active record stays available for reconciliation or
explicit cancellation. This preserves the evidence needed to find a live writer.
