#!/usr/bin/env bash
# The optional worktree path: a run on its own branch, pushed to a real remote,
# with a pull request attempted.
#
# The remote is a bare repository on disk, so the whole loop runs with no
# network, no credentials and no forge account.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "09-worktree-pr: an agent on its own branch, pushed to a local remote"
setup
add_spec "$ROOT" in-progress FTR-0001 "Add retry" '[backend]'

# Make the workspace a real git repository with a bare remote.
git -C "$ROOT" init -q -b main
git -C "$ROOT" config user.email t@example.com
git -C "$ROOT" config user.name T
git -C "$ROOT" add -A
git -C "$ROOT" commit -qm "initial" >/dev/null
BARE="$(mktemp -d)/origin.git"
TEMP_DIRS+=("$(dirname "$BARE")")
git init -q --bare "$BARE"
git -C "$ROOT" remote add origin "$BARE"
git -C "$ROOT" push -q --set-upstream origin main

out="$(hvb --json run start FTR-0001 --pr 2>&1)"
assert_contains "$out" '"branch": "feature/FTR-0001/add-retry"' "the branch follows the VirtualBoard convention"
assert_contains "$out" '"base": "main"' "it was cut from the default branch"
assert_contains "$out" '"pull_request"' "the run records that a PR was asked for"

run_id="$(printf '%s' "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
wt="$(printf '%s' "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin)["worktree"]["path"])')"

assert_equals "$([ -d "$wt" ] && echo yes || echo no)" "yes" "the worktree checkout exists on disk"
branch="$(git -C "$wt" rev-parse --abbrev-ref HEAD)"
assert_equals "$branch" "feature/FTR-0001/add-retry" "the checkout is on the feature branch"

argv="$(herdr_argv)"
assert_contains "$argv" "worktree create" "hvb used herdr's own worktree support"
assert_contains "$argv" "--env HVB_BRANCH=feature/FTR-0001/add-retry" "the branch reached the agent's pane"
assert_contains "$argv" "--env HVB_WORKTREE=$wt" "the checkout path reached the agent's pane"

# Play the agent: commit work on the branch, then report success.
echo "package retry" > "$wt/retry.go"
git -C "$wt" add -A
git -C "$wt" commit -qm "FTR-0001: add the retry helper" >/dev/null

out="$(hvb run done "$run_id" --outcome success --note "criteria met" 2>&1)"
assert_contains "$out" "review" "success still routes the feature to review"

out="$(hvb --json run show "$run_id")"
assert_contains "$out" '"pushed": true' "the branch was pushed to the remote"

pushed="$(git -C "$BARE" branch --list "feature/FTR-0001/add-retry")"
assert_contains "$pushed" "feature/FTR-0001/add-retry" "the remote really has the branch"

# A bare path remote is no forge, so there is nothing to open — and that must
# not have cost the run its outcome.
assert_contains "$out" '"opened": false' "a local remote has no forge to open a PR on"
assert_contains "$out" "no pull-request client" "and it says so"
out_state="$(hvb --json run show "$run_id")"
assert_contains "$out_state" '"state": "succeeded"' "the run still succeeded"

# A review must see the implementation branch, even when worktrees are off
# globally. The main checkout deliberately lacks retry.go.
review="$(hvb --json run start FTR-0001 2>&1)"
assert_contains "$review" '"review_of"' "review records its immutable implementation handoff"
assert_contains "$review" "\"path\": \"$wt\"" "review inherits the implementation checkout"
assert_equals "$([ -f "$ROOT/retry.go" ] && echo yes || echo no)" "no" "main checkout does not contain the implementation"
review_id="$(printf '%s' "$review" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
assert_exit 1 "cleanup protects a checkout used by an active reviewer" -- hvb run cleanup "$run_id"
hvb run cancel "$review_id" >/dev/null

# Cleanup refuses to discard work that exists nowhere else.
echo "scratch" > "$wt/uncommitted.txt"
assert_exit 1 "cleanup refuses a dirty worktree" -- hvb run cleanup "$run_id"
assert_exit 0 "--force discards it"               -- hvb run cleanup "$run_id" --force

finish
