#!/usr/bin/env bash
# `hvb doctor` is the first thing a user runs when something is wrong. It must
# be accurate about what is broken and about what still works anyway.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "07-doctor: the environment report"
setup
add_spec "$ROOT" backlog FTR-0001 "Something"

out="$(hvb doctor 2>&1)"
assert_contains "$out" "workspace" "doctor reports the workspace"
assert_contains "$out" "vb"        "doctor reports vb"
assert_contains "$out" "herdr"     "doctor reports herdr"
assert_contains "$out" "roles"     "doctor reports the role charters"
assert_contains "$out" "backend_dev" "doctor lists the charters it found"
assert_exit 0 "a healthy environment exits 0" -- hvb doctor

out="$(hvb --json doctor 2>&1)"
assert_contains "$out" '"ok": true' "--json reports overall health"

# An incompatible vb cannot replace a currently running agent, even with force.
hvb --json run start FTR-0001 > "$STUBS/started.json"
store="$(find "$HVB_DATA_DIR/runs" -name '*.json' | head -1)"
cp "$store" "$STUBS/store-before-vb-check.json"
cp "$STUBS/vb" "$STUBS/vb-compatible"
cat > "$STUBS/vb" <<'OLDVB'
#!/usr/bin/env bash
case "$*" in
 *version*) echo v0.9.1 ;;
 *doctor*) echo 'unknown command doctor' >&2; exit 1 ;;
 *) echo 'unexpected mutation via old vb' >&2; exit 99 ;;
esac
OLDVB
chmod +x "$STUBS/vb"
assert_exit 1 "an incompatible vb cannot force-replace an active run" -- hvb run start FTR-0001 --force
if cmp -s "$store" "$STUBS/store-before-vb-check.json"; then
  pass "compatibility refusal preserves the active run record"
else
  fail "compatibility refusal changed the active run record"
fi
mv "$STUBS/vb-compatible" "$STUBS/vb"
# Finish this scratch run before the independent Herdr-gate check below.
run_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$STUBS/started.json")"
hvb run cancel "$run_id" >/dev/null

# An unsupported Herdr must fail the gate rather than dispatch into it.
cat > "$STUBS/herdr" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *status*)
    printf 'client:\n  version: 0.8.0\n  protocol: 19\n\nserver:\n  status: running\n  version: 0.8.0\n  socket: /tmp/x.sock\n'
    ;;
  *) echo '{"id":"x","result":{}}' ;;
esac
STUB
chmod +x "$STUBS/herdr"

out="$(hvb doctor 2>&1 || true)"
assert_contains "$out" "0.9.0" "doctor names the herdr version it needs"
assert_exit 1 "an unsupported herdr fails the report" -- hvb doctor

out="$(hvb run start FTR-0001 2>&1 || true)"
assert_contains "$out" "herdr" "dispatch refuses an unsupported herdr"
assert_exit 6 "an unsupported herdr exits 6" -- hvb run start FTR-0001

finish
