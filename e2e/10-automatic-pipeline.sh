#!/usr/bin/env bash
# Opt-in automation must start real hvb runs, preserve stage identity, and stop
# at configured limits instead of silently doing nothing or looping forever.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT
setup
add_spec "$ROOT" backlog FTR-0001 "Automatic feature" '[backend]'
cat > "$ROOT/.hvb.toml" <<'CONFIG'
max_auto_stages = 2
max_concurrent_runs = 2
[columns.in-progress]
auto = true
[columns.review]
auto = true
CONFIG

echo '10-automatic-pipeline: opt-in transitions and bounded execution'
hvb feature move FTR-0001 in-progress >/dev/null
first="$(hvb --json run list --all)"
assert_contains "$first" '"auto_depth": 1' "moving into an automatic column dispatches a run"
id1="$(printf '%s' "$first" | python3 -c 'import json,sys; print(json.load(sys.stdin)["runs"][0]["id"])')" || { fail "run-list JSON could not be decoded"; finish; }
completed="$(hvb --json run done "$id1" --outcome success)"
id2="$(printf '%s' "$completed" | python3 -c 'import json,sys; print(json.load(sys.stdin)["run"]["auto_child_id"])')" || { fail "completion did not record an automatic child"; finish; }
review="$(hvb --json run show "$id2")"
assert_contains "$review" '"role": "qa"' "review-stage automation selects the QA role"
assert_contains "$review" '"state": "running"' "the automatic reviewer is running"
assert_contains "$review" '"auto_depth": 2' "the next stage records its chain depth"
assert_contains "$review" "$id1" "the next stage links back to its source run"

bounded="$(hvb --json run done "$id2" --outcome failure 2>&1)"
bounded_status=$?
assert_equals "$bounded_status" 1 "hitting the stage limit reports a failed automatic handoff"
assert_contains "$bounded" 'automatic stage limit 2 reached' "a review/build cycle stops at its configured stage limit"
all="$(hvb --json run list --all)"
count="$(printf '%s' "$all" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["runs"]))')"
assert_equals "$count" 2 "the bounded cycle created exactly two runs"
hvb run done "$id1" --outcome success >/dev/null
again="$(hvb --json run list --all | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["runs"]))')"
assert_equals "$again" 2 "a duplicate source outcome does not launch another child"

for id in FTR-0002 FTR-0003 FTR-0004; do add_spec "$ROOT" in-progress "$id" "Concurrent $id" '[backend]'; done
hvb run start FTR-0002 >/dev/null
hvb run start FTR-0003 >/dev/null
assert_exit 1 "manual dispatch respects the project concurrency limit" -- hvb run start FTR-0004
assert_exit 5 "a feature cannot gain a second active run" -- hvb run start FTR-0002
finish
