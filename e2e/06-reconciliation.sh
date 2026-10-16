#!/usr/bin/env bash
# What the board does about a run that stopped without reporting.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "06-reconciliation: a run that stops unexplained parks for a human"
setup
add_spec "$ROOT" in-progress FTR-0001 "Abandoned work"

run_id="$(hvb --json run start FTR-0001 | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"

out="$(hvb --json run list)"
assert_contains "$out" '"state": "running"' "the run starts out running"

# The user closes the pane, or the harness exits. hvb finds out by looking.
: > "$STUBS/live_panes"

out="$(hvb --json run list --all)"
assert_contains "$out" '"state": "awaiting"'    "a vanished pane parks the run as awaiting"
assert_contains "$out" '"outcome": "pane_closed"' "the reason is recorded"
assert_not_contains "$out" '"moved_to"' "an unexplained stop does not move the feature"

# The feature stays where it was: only a human can say whether that work landed.
out="$(hvb feature list --status in-progress)"
assert_contains "$out" "FTR-0001" "the feature is still in progress"

# A harness that reports done without calling `hvb run done` parks the same way.
add_spec "$ROOT" in-progress FTR-0002 "Silent finisher"
started="$(hvb --json run start FTR-0002)"
run_id="$(printf '%s' "$started" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
printf '%s' "$started" | python3 -c 'import json,sys; run=json.load(sys.stdin); json.dump({"id":"x","result":{"agents":[{"pane_id":run["pane_id"],"agent":"claude","agent_status":"done"}]}},sys.stdout)' > "$STUBS/agents.json"

out="$(hvb --json run show "$run_id")"
assert_contains "$out" '"outcome": "agent_done"' "a finished-but-silent harness parks as agent_done"

finish
