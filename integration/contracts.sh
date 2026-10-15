#!/usr/bin/env bash
# Cross-project contract: real vb, real hvb, and the actual template schema.
# No Herdr session, credentials, remote repository, or provider is used.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
template="${TEMPLATE_SOURCE:-$here/../template-base}"
: "${VB_REAL_BIN:?Set VB_REAL_BIN to the vb binary under test}"
: "${HVB_BIN:?Set HVB_BIN to the hvb binary under test}"
[ -f "$template/schemas/frontmatter.schema.json" ] || { echo 'TEMPLATE_SOURCE must point at the template repository' >&2; exit 1; }
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
root="$scratch/project"
mkdir -p "$root/.virtualboard/templates" "$root/.virtualboard/schemas"
for status in backlog in-progress blocked review done; do mkdir -p "$root/.virtualboard/features/$status"; done
cp "$template/templates/feature.md" "$root/.virtualboard/templates/feature.md"
cp "$template/templates/feature-light.md" "$root/.virtualboard/templates/feature-light.md"
cp "$template/schemas/frontmatter.schema.json" "$root/.virtualboard/schemas/frontmatter.schema.json"
export HVB_VB_BIN="$VB_REAL_BIN" HVB_DATA_DIR="$scratch/data" HVB_CONFIG="$scratch/no-config.toml" HVB_OWNER=alice
export HERDR_BIN_PATH="$scratch/no-herdr-allowed"
vb() { "$VB_REAL_BIN" --root "$root" "$@"; }
hvb() { "$HVB_BIN" --root "$root" "$@"; }
expect_exit() {
  local want="$1"; shift
  local actual=0
  "$@" >"$scratch/stdout" 2>"$scratch/stderr" || actual=$?
  [ "$actual" = "$want" ] || { cat "$scratch/stdout" "$scratch/stderr" >&2; echo "exit $actual, expected $want" >&2; exit 1; }
}

expect_exit 1 vb --json new x Invalid_Label
[ -z "$(find "$root/.virtualboard/features" -name 'FTR-*.md' -print)" ] || { echo 'invalid creation wrote a spec' >&2; exit 1; }
vb --json new 'Contract feature' backend >"$scratch/created.json"
vb --json lock FTR-0001 --owner alice >"$scratch/lease.json"
expect_exit 5 vb --actor bob move FTR-0001 in-progress --owner bob
vb --actor alice move FTR-0001 in-progress --owner alice >/dev/null
expect_exit 1 vb --actor alice update FTR-0001 --field status=done
expect_exit 1 vb --actor alice update FTR-0001 --field id=FTR-0002
vb --actor alice update FTR-0001 --field role=backend_dev >/dev/null
vb --json validate FTR-0001 >"$scratch/valid.json"
hvb --json feature validate FTR-0001 >"$scratch/hvb-valid.json"
python3 - "$scratch/hvb-valid.json" <<'PY'
import json,sys
result=json.load(open(sys.argv[1]))
assert result['features']['total']==1 and result['features']['valid']==1, result
PY

# Deliberately corrupt a scratch spec to verify the diagnostic boundary.
spec="$(find "$root/.virtualboard/features" -name 'FTR-0001-*.md' -print)"
python3 - "$spec" <<'PY'
from pathlib import Path
import sys
p=Path(sys.argv[1]); p.write_text(p.read_text().replace('title: Contract feature','title: Contract feature\nunknown_property: true'))
PY
expect_exit 1 vb --json validate FTR-0001
expect_exit 1 hvb --json feature validate FTR-0001
python3 - "$scratch/stdout" <<'PY'
import json,sys
result=json.load(open(sys.argv[1]))
assert result['features']['invalid']==1, result
assert any('unknown_property' in error for error in result['features']['results']['FTR-0001']['errors']), result
PY

# Real binaries must preserve a failed doctor report without changing the board
# or creating the machine-local run store. The scratch feature is still invalid.
python3 - "$root" "$scratch/before.json" <<'SNAPSHOT'
import hashlib,json,sys
from pathlib import Path
root=Path(sys.argv[1])
json.dump({str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in root.rglob('*') if p.is_file()},open(sys.argv[2],'w'),sort_keys=True)
SNAPSHOT
expect_exit 1 vb --json doctor
python3 - "$scratch/stdout" "$scratch/lease.json" <<'DOCTOR'
import json,sys
raw=open(sys.argv[1]).read(); result=json.loads(raw)
assert result['success'] is False and result['data']['ok'] is False, result
assert result['data']['format_version']==1
assert any(c['fatal'] and not c['ok'] and c.get('remedy') for c in result['data']['checks'])
assert json.load(open(sys.argv[2]))['data']['token'] not in raw
DOCTOR
expect_exit 1 hvb --json doctor
python3 - "$scratch/stdout" "$root" "$scratch/before.json" "$HVB_DATA_DIR" <<'DOCTOR'
import hashlib,json,sys
from pathlib import Path
result=json.load(open(sys.argv[1])); root=Path(sys.argv[2])
assert result['ok'] is False, result
assert any(c['name'].startswith('vb ') and c['fatal'] and not c['ok'] for c in result['checks']), result
assert any(c['name']=='run store' and c['ok'] for c in result['checks']), result
assert not Path(sys.argv[4]).exists(), 'doctor created run-store directories'
after={str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in root.rglob('*') if p.is_file()}
assert after==json.load(open(sys.argv[3])), 'doctor changed workspace bytes'
DOCTOR

token="$(python3 - "$scratch/lease.json" <<'PY'
import json,sys
print(json.load(open(sys.argv[1]))['data']['token'])
PY
)"
vb lock FTR-0001 --release --token "$token" >/dev/null
vb new 'Small contract change' --template light >/dev/null
vb --json validate FTR-0002 >"$scratch/light-valid.json"
python3 - "$root" <<'PYLIGHT'
from pathlib import Path
import sys
spec=next((Path(sys.argv[1])/'.virtualboard/features/backlog').glob('FTR-0002-*.md')).read_text()
assert '## Verification Evidence' in spec and '## Acceptance Criteria' in spec
assert '## Rollout & Migration' not in spec
PYLIGHT
echo 'real CLI contracts: invalid writes, ownership, lifecycle, roles, validation diagnostics, read-only doctor, lease release, and lightweight templates passed'

python3 "$here/integration/workflow-contract.py" "$root" "$template"

# Reuse only the Herdr recording stub; vb and hvb remain the real binaries.
# Run stub creation in a child shell to keep library globals out of this script.
bash -c 'source "$1/e2e/lib.sh"; make_stubs "$2"' bash "$here" "$scratch/stubs" >/dev/null
export HERDR_BIN_PATH="$scratch/stubs/herdr" HERDR_STUB_CWD="$root"
python3 "$here/integration/lease-recovery.py" "$root" "$scratch/stubs"
