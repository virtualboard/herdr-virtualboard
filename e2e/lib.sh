#!/usr/bin/env bash
# Shared scaffolding for the end-to-end scenarios.
#
# Each scenario runs the real `hvb` binary against a real temporary VirtualBoard
# workspace, with stub `vb` and `herdr` executables on PATH. The stubs are what
# make the suite hermetic: it never touches the developer's Herdr session, never
# starts an agent, and never costs a provider call, while still exercising the
# argv hvb actually emits.
set -uo pipefail

E2E_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
REPO_ROOT="$(cd "$E2E_ROOT/.." && pwd)"
HVB="${HVB_BIN:-$REPO_ROOT/bin/hvb}"

FAILURES=0
CHECKS=0

pass() { CHECKS=$((CHECKS + 1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() {
  CHECKS=$((CHECKS + 1))
  FAILURES=$((FAILURES + 1))
  printf '  \033[31m✗\033[0m %s\n' "$1"
  [ "$#" -gt 1 ] && printf '      %s\n' "$2"
  return 0
}

# assert_contains <haystack> <needle> <description>
assert_contains() {
  if printf '%s' "$1" | grep -qF -- "$2"; then
    pass "$3"
  else
    fail "$3" "expected to find: $2"
    printf '      got: %s\n' "$(printf '%s' "$1" | head -5)"
  fi
}

# assert_not_contains <haystack> <needle> <description>
assert_not_contains() {
  if printf '%s' "$1" | grep -qF -- "$2"; then
    fail "$3" "did not expect: $2"
  else
    pass "$3"
  fi
}

# assert_equals <actual> <expected> <description>
assert_equals() {
  if [ "$1" = "$2" ]; then
    pass "$3"
  else
    fail "$3" "expected [$2], got [$1]"
  fi
}

# assert_exit <expected-code> <description> -- <command...>
assert_exit() {
  local expected="$1" description="$2"
  shift 3
  "$@" >/dev/null 2>&1
  assert_equals "$?" "$expected" "$description"
}

finish() {
  echo
  if [ "$FAILURES" -eq 0 ]; then
    printf '\033[32m%s: %d checks passed\033[0m\n' "$(basename "$0")" "$CHECKS"
    exit 0
  fi
  printf '\033[31m%s: %d of %d checks failed\033[0m\n' "$(basename "$0")" "$FAILURES" "$CHECKS"
  exit 1
}

# make_workspace creates a VirtualBoard layout and echoes the project root.
# It deliberately writes the specs directly rather than calling `vb new`: the
# suite must run without a real vb install.
make_workspace() {
  local root
  root="$(mktemp -d)"
  mkdir -p "$root/.virtualboard/features"/{backlog,in-progress,blocked,review,done}
  mkdir -p "$root/.virtualboard/agents"
  for role in backend_dev frontend_dev fullstack_dev qa devops_engineer; do
    cat >"$root/.virtualboard/agents/$role.md" <<ROLE
---
name: ${role//_/-}
description: charter for $role
---

# $role

Charter body for $role.
ROLE
  done
  cat >"$root/.virtualboard/agents/RULES.md" <<'RULES'
# Agent Rules of Engagement
RULES
  printf '%s\n' "$root"
}

# add_spec <root> <status> <id> <title> [labels-yaml] [priority]
add_spec() {
  local root="$1" status="$2" id="$3" title="$4" labels="${5:-[]}" priority="${6:-P2}"
  cat >"$root/.virtualboard/features/$status/$id-${title// /-}.md" <<SPEC
---
id: $id
title: $title
status: $status
owner: unassigned
priority: $priority
complexity: M
created: 2026-01-01
updated: 2026-01-02
labels: $labels
---

# Feature Spec: $title

<untrusted-content>

## Summary
Summary for $title.

## Acceptance Criteria (Testable)
- [ ] It works
- [ ] It is tested

</untrusted-content>
SPEC
}

# make_stubs <dir> installs stub `vb` and `herdr` binaries and echoes the dir.
# Both record their argv to <dir>/vb.argv and <dir>/herdr.argv.
make_stubs() {
  local dir="$1"
  mkdir -p "$dir"

  cat >"$dir/vb" <<'VBSTUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$(dirname "$0")/vb.argv"
case "$*" in
  *version*)  echo "v0.9.1"; exit 0 ;;
  *doctor*) echo '{"success":true,"data":{"format_version":1,"ok":true,"version":"test-vb","capabilities":["owner-leases","lease-tokens","lease-renewal","raw-schema-validation","validation-exit-status"],"checks":[{"name":"runtime","ok":true,"fatal":false,"detail":"hermetic stub"}]}}'; exit 0 ;;
  *" move "*)
    id=""; status=""; owner=""
    args=($*)
    for i in "${!args[@]}"; do
      case "${args[$i]}" in
        move) id="${args[$((i+1))]}"; status="${args[$((i+2))]}" ;;
        --owner) owner="${args[$((i+1))]}" ;;
      esac
    done
    # Move the spec file so the next `hvb` read sees the new status, the way
    # real vb does. Without this the suite would be testing hvb against a
    # board that never changes.
    root="${VB_STUB_ROOT:-}"
    if [ -n "$root" ]; then
      src="$(find "$root/.virtualboard/features" -name "$id-*.md" | head -1)"
      if [ -n "$src" ]; then
        dest="$root/.virtualboard/features/$status/$(basename "$src")"
        sed -e "s/^status: .*/status: $status/" \
            -e "s/^owner: .*/owner: ${owner:-unassigned}/" "$src" > "$dest.tmp"
        mv "$dest.tmp" "$dest"
        [ "$src" != "$dest" ] && rm -f "$src"
      fi
    fi
    echo "{\"success\":true,\"message\":\"moved\",\"data\":{\"id\":\"$id\",\"status\":\"$status\",\"owner\":\"$owner\",\"path\":\"features/$status/$id.md\",\"summary\":\"Moved\"}}"
    ;;
  *" new "*)
    echo '{"success":true,"message":"created","data":{"id":"FTR-0099","title":"stub","path":"features/backlog/FTR-0099.md","labels":[]}}'
    ;;
  *" lock "*)
    id=""
    args=("$@")
    for i in "${!args[@]}"; do
      if [ "${args[$i]}" = lock ]; then id="${args[$((i+1))]}"; fi
    done
    printf '{"success":true,"data":{"id":"%s","owner":"tester","token":"fixture-%s","expires_at":"2099-01-01T00:00:00Z","ttl_minutes":60,"expired":false}}\n' "$id" "$id"
    ;;
  *validate*)
    target=""
    args=("$@")
    for i in "${!args[@]}"; do
      if [ "${args[$i]}" = validate ]; then target="${args[$((i+1))]:-}"; fi
    done
    if [ -n "$target" ]; then
      path="$(find "$VB_STUB_ROOT/.virtualboard/features" -name "$target*.md" | head -1)"
      [ -n "$path" ] || exit 2
      status="$(sed -n 's/^status: //p' "$path" | head -1)"
      printf '{"success":true,"data":{"id":"%s","status":"%s","errors":[]}}\n' "$target" "$status"
    else
      echo '{"success":true,"data":{"features":{"total":0,"valid":0,"invalid":0,"results":{}},"specs":{"total":0,"valid":0,"invalid":0,"results":{}}}}'
    fi
    ;;
  *)
    echo '{"success":true,"message":"ok","data":{}}'
    ;;
esac
VBSTUB

  cat >"$dir/herdr" <<'HERDRSTUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$(dirname "$0")/herdr.argv"
state_dir="$(dirname "$0")"
# Model unique server-assigned pane IDs, preserving the first anchor/child as 2/3.
next_pane_id() {
  local current=1
  if [ -f "$state_dir/pane-counter" ]; then read -r current < "$state_dir/pane-counter"; fi
  current=$((current + 1))
  printf '%s\n' "$current" > "$state_dir/pane-counter"
  printf '%s' "$current"
}
case "$*" in
  *status*)
    cat <<'STATUS'
client:
  version: 0.9.0
  channel: stable
  protocol: 22

server:
  status: running
  version: 0.9.0
  socket: /tmp/herdr-stub.sock
STATUS
    ;;
  *"workspace list"*)
    echo "{\"id\":\"x\",\"result\":{\"workspaces\":[{\"workspace_id\":\"w1\",\"label\":\"project\"}]}}"
    ;;
  *"worktree list"*)
    if [ -f "$state_dir/worktrees.json" ]; then cat "$state_dir/worktrees.json"; else
      echo '{"id":"x","result":{"worktrees":[],"source":{"repo_root":"'"${HERDR_STUB_CWD:-/nonexistent}"'"}}}'
    fi
    ;;
  *"worktree create"*)
    # Cut the branch and the checkout for real, so the scenario can assert on
    # actual git state rather than on hvb having called a stub.
    branch=""; base=""
    set -- $*
    while [ $# -gt 0 ]; do
      case "$1" in
        --branch) branch="$2"; shift ;;
        --base) base="$2"; shift ;;
      esac
      shift
    done
    wt="$state_dir/worktrees/$(printf '%s' "$branch" | tr '/' '-')"
    mkdir -p "$(dirname "$wt")"
    git -C "${HERDR_STUB_CWD}" worktree add -q -b "$branch" "$wt" "${base:-main}" >/dev/null 2>&1 ||       git -C "${HERDR_STUB_CWD}" worktree add -q "$wt" "$branch" >/dev/null 2>&1
    n="$(next_pane_id)"
    echo "wG:p$n wG:t$n" >> "$state_dir/live_panes"
    printf '{"id":"x","result":{"workspace":{"workspace_id":"wG","label":"ftr","worktree":{"checkout_path":"%s","is_linked_worktree":true,"repo_root":"%s"}},"tab":{"tab_id":"wG:t%s","workspace_id":"wG"},"root_pane":{"pane_id":"wG:p%s","tab_id":"wG:t%s","workspace_id":"wG","cwd":"%s"},"worktree":{"path":"%s","branch":"%s","is_linked_worktree":true}}}\n' \
      "$wt" "${HERDR_STUB_CWD}" "$n" "$n" "$n" "$wt" "$wt" "$branch"
    ;;
  *"worktree remove"*)
    echo '{"id":"x","result":{"type":"worktree_removed"}}'
    ;;
  *"workspace create"*)
    echo '{"id":"x","result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}'
    ;;
  *"tab list"*)
    echo '{"id":"x","result":{"tabs":[]}}'
    ;;
  *"tab create"*)
    n="$(next_pane_id)"
    echo "w1:p$n w1:t$n" >> "$state_dir/live_panes"
    printf '{"id":"x","result":{"tab":{"tab_id":"w1:t%s","workspace_id":"w1","label":"ftr"},"root_pane":{"pane_id":"w1:p%s","tab_id":"w1:t%s","workspace_id":"w1"}}}\n' "$n" "$n" "$n"
    ;;
  *"pane list"*)
    # Render the panes this stub has actually created, so hvb's reconciliation
    # sees a live pane for a run it just dispatched instead of settling it as
    # abandoned the moment the dispatch returns.
    {
      printf '{"id":"x","result":{"panes":['
      printf '{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1","cwd":"%s"}' "${HERDR_STUB_CWD:-/nonexistent}"
      if [ -f "$state_dir/live_panes" ]; then
        while read -r pane tab; do
          [ -n "$pane" ] || continue
          printf ',{"pane_id":"%s","tab_id":"%s","workspace_id":"w1","cwd":"%s"}' \
            "$pane" "$tab" "${HERDR_STUB_CWD:-/nonexistent}"
        done < "$state_dir/live_panes"
      fi
      printf ']}}\n'
    }
    ;;
  *"pane split"*)
    n="$(next_pane_id)"
    parent=""
    args=("$@")
    for i in "${!args[@]}"; do
      if [ "${args[$i]}" = split ]; then parent="${args[$((i+1))]}"; fi
    done
    tab="${parent/:p/:t}"
    echo "w1:p$n $tab" >> "$state_dir/live_panes"
    printf '{"id":"x","result":{"pane":{"pane_id":"w1:p%s","tab_id":"%s","workspace_id":"w1"}}}\n' "$n" "$tab"
    ;;
  *"pane close"*)
    # Closing removes the pane, so a later `pane list` stops reporting it.
    closed="${*##* }"
    if [ -f "$state_dir/live_panes" ]; then
      grep -v "^$closed " "$state_dir/live_panes" > "$state_dir/live_panes.tmp" || true
      mv "$state_dir/live_panes.tmp" "$state_dir/live_panes"
    fi
    echo '{"id":"x","result":{}}'
    ;;
  *"agent list"*)
    if [ -f "$state_dir/agents.json" ]; then cat "$state_dir/agents.json"; else
      echo '{"id":"x","result":{"agents":[]}}'
    fi
    ;;
  *"agent start"*|*"agent prompt"*|*"agent focus"*)
    echo '{"id":"x","result":{}}'
    ;;
  *"pane read"*)
    # `pane read` prints terminal content, not a JSON envelope.
    echo "stub pane output"
    ;;
  *)
    echo '{"id":"x","result":{}}'
    ;;
esac
HERDRSTUB

  chmod +x "$dir/vb" "$dir/herdr"
  printf '%s\n' "$dir"
}

# setup creates a workspace plus stubs and exports the environment scenarios
# use. Callers get $ROOT, $STUBS, and a PATH with the stubs first.
setup() {
  ROOT="$(make_workspace)"
  STUBS="$(make_stubs "$(mktemp -d)")"
  export PATH="$STUBS:$PATH"
  # hvb prefers $HERDR_BIN_PATH over PATH, exactly as a plugin running inside
  # Herdr should. Without this the suite would drive the developer's real
  # session — creating panes and workspaces in it.
  export HERDR_BIN_PATH="$STUBS/herdr"
  export HVB_VB_BIN="$STUBS/vb"
  export VB_STUB_ROOT="$ROOT"
  export HERDR_STUB_CWD="$ROOT"
  export HVB_DATA_DIR
  HVB_DATA_DIR="$(mktemp -d)"
  export HVB_CONFIG="$STUBS/absent-config.toml"
  export HVB_OWNER="tester"
  TEMP_DIRS=("$ROOT" "$STUBS" "$HVB_DATA_DIR")
}

teardown() {
  for dir in "${TEMP_DIRS[@]:-}"; do
    [ -n "$dir" ] && [ -d "$dir" ] && rm -rf "$dir"
  done
}

hvb() { "$HVB" --root "$ROOT" "$@"; }

vb_argv()    { cat "$STUBS/vb.argv" 2>/dev/null || true; }
herdr_argv() { cat "$STUBS/herdr.argv" 2>/dev/null || true; }
